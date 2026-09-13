package listener

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

// newSharedInboundService wires a listener service over a real database so the
// membership convergence runs against the same constraints production uses.
func newSharedInboundService(t *testing.T) (*Service, *store.Store, *testReconciler) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	reconciler := &testReconciler{}
	service, err := NewService(database, box, reconciler)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := database.CreateProxyGroup(ctx, store.ProxyGroupRecord{
		ID:             "group-a",
		Name:           "group-a",
		Strategy:       "manual",
		SourceSpecJSON: "{}",
		Enabled:        true,
		EmptyBehavior:  "fail-closed",
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil {
		t.Fatal(err)
	}
	return service, database, reconciler
}

func sharedSpec() SharedInboundSpec {
	return SharedInboundSpec{
		Enabled:          true,
		IncludeWebSocket: true,
		MixedBindAddress: "127.0.0.1",
		MixedPort:        7890,
		WSPort:           7891,
	}
}

func createSharedMember(t *testing.T, service *Service, name, kind string) Listener {
	t.Helper()
	auth := &Auth{Username: "hx-user", Password: "hx-pass"}
	if kind == "vless" || kind == "vmess" {
		auth.Password = "11111111-1111-1111-1111-111111111111"
	}
	created, err := service.Create(context.Background(), CreateRequest{
		Name:          name,
		Kind:          kind,
		BindAddress:   "127.0.0.1",
		Port:          19999,
		ProxyGroupID:  "group-a",
		Auth:          auth,
		SharedInbound: SharedInboundStandardOwner,
		Transport:     Transport{Type: "ws", WSPath: SharedInboundRoutePath()},
	})
	if err != nil {
		t.Fatalf("create shared member %q: %v", name, err)
	}
	return created
}

func TestEnsureSharedInboundsCreatesOneAggregatePerFamily(t *testing.T) {
	service, _, reconciler := newSharedInboundService(t)
	createSharedMember(t, service, "service-a", "mixed")
	createSharedMember(t, service, "service-b", "mixed")

	aggregates, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregates) != 1 {
		t.Fatalf("aggregates = %d, want one mixed aggregate", len(aggregates))
	}
	aggregate := aggregates[0]
	if aggregate.SharedInbound != SharedInboundStandardOwner || aggregate.Kind != "mixed" {
		t.Fatalf("aggregate = %+v", aggregate)
	}
	if aggregate.BindAddress != "127.0.0.1" || aggregate.Port != 7890 {
		t.Fatalf("aggregate endpoint = %s:%d, want 127.0.0.1:7890", aggregate.BindAddress, aggregate.Port)
	}
	if reconciler.calls == 0 {
		t.Fatal("creating an aggregate did not trigger a data-plane apply")
	}

	// Idempotent: a second convergence must not add rows or touch the port.
	before := len(aggregates)
	again, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != before {
		t.Fatalf("second convergence changed the aggregate count: %d -> %d", before, len(again))
	}
}

func TestEnsureSharedInboundsRepairsAggregateEndpoint(t *testing.T) {
	service, _, _ := newSharedInboundService(t)
	createSharedMember(t, service, "service-a", "mixed")
	if _, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil); err != nil {
		t.Fatal(err)
	}
	moved := sharedSpec()
	moved.MixedPort = 7999
	if _, err := service.EnsureSharedInbounds(context.Background(), moved, nil, nil); err != nil {
		t.Fatal(err)
	}
	aggregates, err := service.SharedInboundAggregates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregates) != 1 || aggregates[0].Port != 7999 {
		t.Fatalf("aggregate port was not repaired: %+v", aggregates)
	}
}

func TestEnsureSharedInboundsRemovesEmptyAggregate(t *testing.T) {
	service, database, _ := newSharedInboundService(t)
	member := createSharedMember(t, service, "service-a", "mixed")
	if _, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), member.ID, member.Version); err != nil {
		t.Fatal(err)
	}
	aggregates, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregates) != 0 {
		t.Fatalf("aggregates = %+v, want none after the last member left", aggregates)
	}
	// The port must really be free again.
	if _, err := database.GetListener(context.Background(), member.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("member still exists: %v", err)
	}
	recreated, err := service.Create(context.Background(), CreateRequest{
		Name: "replacement", Kind: "mixed", BindAddress: "127.0.0.1", Port: 7890,
		ProxyGroupID: "group-a", Auth: &Auth{Username: "u", Password: "p"},
	})
	if err != nil {
		t.Fatalf("port was not released: %v", err)
	}
	_ = recreated
}

// TestEnsureSharedInboundsCarriesEveryStandardProtocolAsOneMixedEntry is the
// regression test for the failure where an HTTP or SOCKS5 proxy service vanished
// from the data plane.
//
// The family was keyed by the member's own protocol, so an http member produced
// an aggregate row of kind "http" — and Mihomo has no aggregate HTTP listener, so
// the service was published nowhere while its row still claimed a shared entry
// point. Every standard protocol must converge on the single Mixed carrier.
func TestEnsureSharedInboundsCarriesEveryStandardProtocolAsOneMixedEntry(t *testing.T) {
	service, _, _ := newSharedInboundService(t)
	createSharedMember(t, service, "service-http", "http")
	createSharedMember(t, service, "service-socks", "socks")
	createSharedMember(t, service, "service-mixed", "mixed")

	aggregates, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregates) != 1 {
		t.Fatalf("aggregates = %d, want exactly one Mixed entry point: %+v", len(aggregates), aggregates)
	}
	if aggregates[0].Kind != "mixed" {
		t.Fatalf("aggregate kind = %q, want %q", aggregates[0].Kind, "mixed")
	}
}

// TestEnsureSharedInboundsRetiresPerProtocolAggregates covers an install that
// already stored one aggregate row per protocol. The stale rows must be removed,
// not left behind holding a port the family no longer uses.
func TestEnsureSharedInboundsRetiresPerProtocolAggregates(t *testing.T) {
	service, database, _ := newSharedInboundService(t)
	createSharedMember(t, service, "service-http", "http")
	if _, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil); err != nil {
		t.Fatal(err)
	}
	records, err := database.ListListeners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := 0
	for _, record := range records {
		if IsSharedInboundAggregate(record) {
			before++
		}
	}
	if before != 1 {
		t.Fatalf("aggregate rows = %d, want 1", before)
	}

	// Simulate the older layout by relabelling the aggregate row as the
	// family's per-protocol variant, then adding a mixed member.
	stale := records[0]
	for _, record := range records {
		if IsSharedInboundAggregate(record) {
			stale = record
		}
	}
	stale.Kind = "http"
	stale.Name = sharedAggregateName(SharedInboundStandardOwner, "http")
	if _, err := database.UpdateListener(context.Background(), stale, stale.Version); err != nil {
		t.Fatal(err)
	}
	createSharedMember(t, service, "service-mixed", "mixed")

	aggregates, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregates) != 1 || aggregates[0].Kind != "mixed" {
		t.Fatalf("aggregates = %+v, want one Mixed entry point", aggregates)
	}
	after, err := database.ListListeners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	aggregateRows := 0
	for _, record := range after {
		if IsSharedInboundAggregate(record) {
			aggregateRows++
		}
	}
	if aggregateRows != 1 {
		t.Fatalf("aggregate rows after repair = %d, want 1", aggregateRows)
	}
}

func TestEnsureSharedInboundsKeepsOneListenerPerWebSocketProtocol(t *testing.T) {
	service, _, _ := newSharedInboundService(t)
	for _, kind := range []string{"vless", "vmess", "trojan"} {
		created, err := service.Create(context.Background(), CreateRequest{
			Name: "ws-" + kind, Kind: kind, BindAddress: "127.0.0.1", Port: 19999,
			ProxyGroupID:  "group-a",
			Auth:          &Auth{Username: "hx-user", Password: "11111111-1111-1111-1111-111111111111"},
			SharedInbound: SharedInboundWebSocketOwner,
			Transport:     Transport{Type: "ws", WSPath: SharedInboundRoutePath()},
			// An advanced listener is only reachable through the edge relay, so
			// the service requires the public host it is published behind.
			PublicEndpoint: PublicEndpoint{Host: "proxy.example.com", Port: 443, TLS: true},
		})
		if err != nil {
			t.Fatalf("create %s member: %v", kind, err)
		}
		_ = created
	}
	aggregates, err := service.EnsureSharedInbounds(context.Background(), sharedSpec(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(aggregates) != 3 {
		t.Fatalf("aggregates = %d, want one listener per WebSocket protocol (they share a port, not a listener)", len(aggregates))
	}
}

// TestSharedInboundCarrierKind pins the family contract: the standard family is
// always carried by a Mixed listener, whatever protocol its members use.
func TestSharedInboundCarrierKind(t *testing.T) {
	for _, kind := range SharedInboundMemberKinds(SharedInboundStandardOwner) {
		if got := SharedInboundCarrierKind(SharedInboundStandardOwner, kind); got != "mixed" {
			t.Fatalf("standard carrier for %q = %q, want mixed", kind, got)
		}
		if !IsSharedInboundMemberKind(SharedInboundStandardOwner, kind) {
			t.Fatalf("%q is not recognised as a standard member kind", kind)
		}
	}
	for _, kind := range SharedInboundMemberKinds(SharedInboundWebSocketOwner) {
		if got := SharedInboundCarrierKind(SharedInboundWebSocketOwner, kind); got != kind {
			t.Fatalf("websocket carrier for %q = %q, want %q", kind, got, kind)
		}
	}
	if SharedInboundCarrierKind(SharedInboundStandardOwner, "vless") != "mixed" {
		t.Fatal("an advanced kind must not be reported as a WebSocket carrier of the standard family")
	}
	if IsSharedInboundMemberKind(SharedInboundStandardOwner, "vless") {
		t.Fatal("a VLESS listener is not a standard-family member")
	}
	if IsSharedInboundMemberKind(SharedInboundWebSocketOwner, "http") {
		t.Fatal("an HTTP listener is not a WebSocket-family member")
	}
}

func TestCreateRejectsUnknownSharedInboundOwner(t *testing.T) {
	service, _, _ := newSharedInboundService(t)
	_, err := service.Create(context.Background(), CreateRequest{
		Name: "bad", Kind: "mixed", BindAddress: "127.0.0.1", Port: 7890,
		ProxyGroupID: "group-a", Auth: &Auth{Username: "u", Password: "p"},
		SharedInbound: "not-a-family",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create() error = %v, want ErrInvalid", err)
	}
}
