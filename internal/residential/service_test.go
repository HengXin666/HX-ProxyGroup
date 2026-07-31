package residential

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

type stubReconciler struct{ calls int }

func (reconciler *stubReconciler) Apply(context.Context) error {
	reconciler.calls++
	return nil
}

// recordingSelector captures the data-plane selector switches a rotation makes,
// which is how we assert that rotation avoids a configuration recompile.
type recordingSelector struct {
	mu       sync.Mutex
	selects  [][2]string
	failWith error
}

func (selector *recordingSelector) SelectProxy(_ context.Context, group, proxy string) error {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if selector.failWith != nil {
		return selector.failWith
	}
	selector.selects = append(selector.selects, [2]string{group, proxy})
	return nil
}

func (selector *recordingSelector) calls() [][2]string {
	selector.mu.Lock()
	defer selector.mu.Unlock()
	return append([][2]string(nil), selector.selects...)
}

type stubProber struct {
	ip  string
	err error
}

func (prober *stubProber) ProbeExitIP(context.Context, string) (string, error) {
	return prober.ip, prober.err
}

type testHarness struct {
	service    *Service
	store      *store.Store
	box        *secret.Box
	selector   *recordingSelector
	reconciler *stubReconciler
}

func newHarness(t *testing.T, options ...Option) *testHarness {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	t.Cleanup(func() { database.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("secret.New() error = %v", err)
	}
	reconciler := &stubReconciler{}
	groups, err := proxygroup.NewService(database, reconciler)
	if err != nil {
		t.Fatalf("proxygroup.NewService() error = %v", err)
	}
	listeners, err := listener.NewService(database, box, reconciler)
	if err != nil {
		t.Fatalf("listener.NewService() error = %v", err)
	}
	selector := &recordingSelector{}
	options = append([]Option{WithSelector(selector), WithRotateInterval(time.Nanosecond)}, options...)
	service, err := NewService(database, box, groups, listeners, options...)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return &testHarness{
		service:    service,
		store:      database,
		box:        box,
		selector:   selector,
		reconciler: reconciler,
	}
}

func (harness *testHarness) createProvider(t *testing.T) Provider {
	t.Helper()
	provider, err := harness.service.CreateProvider(context.Background(), CreateProviderRequest{
		Name:             "bestproxy-main",
		Vendor:           "bestproxy",
		Protocol:         "http",
		GatewayHost:      "gate.bestproxy.com",
		GatewayPort:      8000,
		Credentials:      &Credentials{Username: "acct123", Password: "s3cret"},
		UsernameTemplate: "{user}-region-{region}-session-{session}",
		RotationMode:     RotationSessionTemplate,
		PoolSize:         4,
		DefaultRegion:    "us",
	})
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	return provider
}

func TestCreateProviderEncryptsCredentialsAndHidesPassword(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	provider := harness.createProvider(t)

	if provider.CredentialsConfigured != true {
		t.Fatal("provider should report configured credentials")
	}
	if provider.GatewayUsername != "acct123" {
		t.Fatalf("GatewayUsername = %q, want the account login", provider.GatewayUsername)
	}
	// The DTO must have no field capable of carrying the password back out.
	encoded, err := json.Marshal(provider)
	if err != nil {
		t.Fatalf("marshal provider: %v", err)
	}
	if strings.Contains(string(encoded), "s3cret") {
		t.Fatalf("provider JSON leaked the gateway password: %s", encoded)
	}

	record, err := harness.store.GetResidentialProvider(context.Background(), provider.ID)
	if err != nil {
		t.Fatalf("GetResidentialProvider() error = %v", err)
	}
	if strings.Contains(string(record.CredentialsEncrypted), "s3cret") {
		t.Fatal("credentials were stored in cleartext")
	}
	if !provider.SupportsSticky {
		t.Fatal("a session template provider must support sticky mode")
	}
}

func TestCreateProviderRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	base := CreateProviderRequest{
		Name:             "vendor",
		Protocol:         "http",
		GatewayHost:      "gate.example.com",
		GatewayPort:      8000,
		Credentials:      &Credentials{Username: "acct", Password: "pw"},
		UsernameTemplate: "{user}-session-{session}",
		RotationMode:     RotationSessionTemplate,
	}
	cases := map[string]func(*CreateProviderRequest){
		"empty name":             func(r *CreateProviderRequest) { r.Name = " " },
		"bad protocol":           func(r *CreateProviderRequest) { r.Protocol = "vmess" },
		"loopback gateway":       func(r *CreateProviderRequest) { r.GatewayHost = "127.0.0.1" },
		"localhost gateway":      func(r *CreateProviderRequest) { r.GatewayHost = "localhost" },
		"unspecified gateway":    func(r *CreateProviderRequest) { r.GatewayHost = "0.0.0.0" },
		"bad port":               func(r *CreateProviderRequest) { r.GatewayPort = 0 },
		"bad template":           func(r *CreateProviderRequest) { r.UsernameTemplate = "{nope}" },
		"sticky without session": func(r *CreateProviderRequest) { r.UsernameTemplate = "{user}" },
		"missing credentials":    func(r *CreateProviderRequest) { r.Credentials = nil },
		"colon in username":      func(r *CreateProviderRequest) { r.Credentials = &Credentials{Username: "a:b", Password: "pw"} },
		"api-list unsupported":   func(r *CreateProviderRequest) { r.RotationMode = RotationAPIList },
		"bad region":             func(r *CreateProviderRequest) { r.DefaultRegion = "us west" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := base
			mutate(&request)
			if _, err := harness.service.CreateProvider(context.Background(), request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("CreateProvider() error = %v, want ErrInvalid", err)
			}
		})
	}
}

// A per-request gateway has no session to pin, so its pool collapses to one
// upstream and sticky channels must be refused.
func TestPerRequestProviderCannotBackStickyChannel(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()

	provider, err := harness.service.CreateProvider(ctx, CreateProviderRequest{
		Name:             "rotating",
		Protocol:         "http",
		GatewayHost:      "gate.example.com",
		GatewayPort:      8000,
		Credentials:      &Credentials{Username: "acct", Password: "pw"},
		UsernameTemplate: "{user}",
		RotationMode:     RotationPerRequest,
		PoolSize:         8,
	})
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if provider.PoolSize != 1 {
		t.Fatalf("per-request PoolSize = %d, want 1", provider.PoolSize)
	}
	if provider.SupportsSticky {
		t.Fatal("per-request provider must not advertise sticky support")
	}

	_, err = harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-attempt",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29101},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreateChannel(sticky) error = %v, want ErrInvalid", err)
	}
}

func TestCreateStickyChannelProvisionsPoolGroupAndListener(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-us",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		Region:     "us",
		PoolSize:   4,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29102},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	if channel.PoolSize != 4 {
		t.Fatalf("PoolSize = %d, want 4", channel.PoolSize)
	}
	if !channel.CanRotate {
		t.Fatal("a sticky channel with a pool must be rotatable")
	}
	if !strings.HasPrefix(channel.RotatePath, "/rot/") {
		t.Fatalf("RotatePath = %q, want a /rot/ path", channel.RotatePath)
	}
	if channel.Endpoint.Port != 29102 || channel.Endpoint.Kind != "mixed" {
		t.Fatalf("unexpected endpoint %+v", channel.Endpoint)
	}

	// The pool must be materialized as selectable group candidates, each with a
	// distinct sticky session encoded in its username.
	pool, err := harness.store.ListResidentialSessionNodes(ctx, channel.ID)
	if err != nil {
		t.Fatalf("ListResidentialSessionNodes() error = %v", err)
	}
	if len(pool) != 4 {
		t.Fatalf("pool size = %d, want 4", len(pool))
	}
	usernames := map[string]bool{}
	for _, node := range pool {
		plaintext, err := harness.box.Open(node.CanonicalConfigEncrypted, []byte("node:"+node.Fingerprint))
		if err != nil {
			t.Fatalf("decrypt pooled node: %v", err)
		}
		var canonical map[string]any
		if err := json.Unmarshal(plaintext, &canonical); err != nil {
			t.Fatalf("decode pooled node: %v", err)
		}
		username, _ := canonical["username"].(string)
		if !strings.HasPrefix(username, "acct123-region-us-session-") {
			t.Fatalf("pooled username %q does not follow the template", username)
		}
		if usernames[username] {
			t.Fatalf("duplicate session username %q in pool", username)
		}
		usernames[username] = true
		if canonical["server"] != "gate.bestproxy.com" {
			t.Fatalf("pooled node server = %v", canonical["server"])
		}
	}

	// Creation should immediately pin the first session.
	calls := harness.selector.calls()
	if len(calls) != 1 {
		t.Fatalf("selector calls = %d, want 1", len(calls))
	}
	if !strings.HasPrefix(calls[0][1], "hx-node-") {
		t.Fatalf("selector targeted %q, want a compiled node name", calls[0][1])
	}
}

// Passthrough channels leave rotation to the vendor: one upstream, no rotate
// token, and the rotate API must refuse them.
func TestPassthroughChannelHasSingleUpstreamAndCannotRotate(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "passthrough-us",
		ProviderID: provider.ID,
		Mode:       ModePassthrough,
		PoolSize:   8,
		Listener: ChannelListenerRequest{
			Kind:        "mixed",
			BindAddress: "0.0.0.0",
			Port:        29103,
			Auth:        &listener.Auth{Username: "consumer", Password: "consumer-pass"},
		},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	if channel.PoolSize != 1 {
		t.Fatalf("passthrough PoolSize = %d, want 1", channel.PoolSize)
	}
	if channel.CanRotate || channel.RotatePath != "" {
		t.Fatalf("passthrough channel must not expose rotation: %+v", channel)
	}
	if !channel.Endpoint.AuthEnabled {
		t.Fatal("a non-loopback passthrough listener must require authentication")
	}
	if _, err := harness.service.RotateChannel(ctx, channel.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("RotateChannel(passthrough) error = %v, want ErrInvalid", err)
	}
}

func TestCreateChannelRejectsUnauthenticatedPublicListener(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	provider := harness.createProvider(t)

	_, err := harness.service.CreateChannel(context.Background(), CreateChannelRequest{
		Name:       "public-open",
		ProviderID: provider.ID,
		Mode:       ModePassthrough,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "0.0.0.0", Port: 29104},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreateChannel() error = %v, want ErrInvalid", err)
	}
	// The compensating delete must leave nothing behind.
	channels, err := harness.service.ListChannels(context.Background())
	if err != nil {
		t.Fatalf("ListChannels() error = %v", err)
	}
	if len(channels) != 0 {
		t.Fatalf("a failed create left %d channels behind", len(channels))
	}
	groups, err := harness.store.ListProxyGroups(context.Background())
	if err != nil {
		t.Fatalf("ListProxyGroups() error = %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("a failed create left %d proxy groups behind", len(groups))
	}
	nodes, err := harness.store.ListNodeConfigs(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListNodeConfigs() error = %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("a failed create left %d pooled nodes behind", len(nodes))
	}
}

func TestDeleteChannelRemovesEverythingItProvisioned(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-delete",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   2,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29105},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	if err := harness.service.DeleteChannel(ctx, channel.ID, channel.Version); err != nil {
		t.Fatalf("DeleteChannel() error = %v", err)
	}
	if _, err := harness.service.GetChannel(ctx, channel.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetChannel after delete error = %v, want ErrNotFound", err)
	}
	nodes, err := harness.store.ListResidentialSessionNodes(ctx, channel.ID)
	if err != nil {
		t.Fatalf("ListResidentialSessionNodes() error = %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("delete left %d pooled nodes behind", len(nodes))
	}
	listeners, err := harness.store.ListListeners(ctx)
	if err != nil {
		t.Fatalf("ListListeners() error = %v", err)
	}
	if len(listeners) != 0 {
		t.Fatalf("delete left %d listeners behind", len(listeners))
	}
	groups, err := harness.store.ListProxyGroups(ctx)
	if err != nil {
		t.Fatalf("ListProxyGroups() error = %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("delete left %d proxy groups behind", len(groups))
	}
}
