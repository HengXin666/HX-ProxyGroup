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
