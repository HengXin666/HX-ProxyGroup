package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/nodeparse"
	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
	"github.com/HengXin666/HX-ProxyGroup/internal/subscription"
)

type fakeProber struct {
	latency int
	err     error
	applies int
}

func (prober *fakeProber) Apply(context.Context) error {
	prober.applies++
	return nil
}

func (prober *fakeProber) TestProxy(context.Context, string, string, time.Duration) (int, error) {
	return prober.latency, prober.err
}

func TestCheckUpdatesNodeLifecycle(t *testing.T) {
	ctx := context.Background()
	database, nodeID := createCandidateNode(t, ctx)
	defer database.Close()
	prober := &fakeProber{latency: 120}
	service, err := NewService(database, WithProber(prober))
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Check(ctx, nodeID)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !result.Success || result.LatencyMS == nil || *result.LatencyMS != 120 {
		t.Fatalf("unexpected successful result: %+v", result)
	}
	if result.Node.LifecycleState != "healthy" || result.Node.ConsecutiveProbeFailures != 0 {
		t.Fatalf("unexpected healthy node: %+v", result.Node)
	}

	prober.latency = 1800
	result, err = service.Check(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Node.LifecycleState != "degraded" {
		t.Fatalf("high-latency state = %q, want degraded", result.Node.LifecycleState)
	}

	prober.err = errors.New("connection refused")
	for attempt := 1; attempt <= 3; attempt++ {
		result, err = service.Check(ctx, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if result.Success {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
	}
	if result.Node.LifecycleState != "quarantined" || result.Node.ConsecutiveProbeFailures != 3 {
		t.Fatalf("unexpected quarantined node: %+v", result.Node)
	}

	prober.err = nil
	prober.latency = 80
	result, err = service.Check(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Node.LifecycleState != "healthy" || result.Node.ConsecutiveProbeFailures != 0 {
		t.Fatalf("successful recovery did not reset node: %+v", result.Node)
	}
	if prober.applies != 6 {
		t.Fatalf("Apply() calls = %d, want 6", prober.applies)
	}
}

func TestCheckResultKeepsNodeSources(t *testing.T) {
	ctx := context.Background()
	database, nodeID := createCandidateNode(t, ctx)
	defer database.Close()
	service, err := NewService(database, WithProber(&fakeProber{latency: 42}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Check(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Node.Sources) != 1 || result.Node.Sources[0].SubscriptionID == "" {
		t.Fatalf("sources = %+v, want active subscription source", result.Node.Sources)
	}
}

func createCandidateNode(t *testing.T, ctx context.Context) (*store.Store, string) {
	t.Helper()
	directory := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	service, err := subscription.NewService(
		database,
		box,
		subscription.WithRefresh(subscription.NewDefaultSourceLoader(), filepath.Join(directory, "snapshots")),
		subscription.WithParser(nodeparse.Parse),
	)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	created, err := service.Create(ctx, subscription.CreateRequest{
		Name:         "quality-node",
		SourceType:   subscription.SourceInline,
		SourceConfig: subscription.SourceConfig{Inline: "socks5://127.0.0.1:1080#quality"},
	})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := service.Refresh(ctx, created.ID); err != nil {
		database.Close()
		t.Fatal(err)
	}
	nodes, err := database.ListNodes(ctx, store.NodeFilter{Limit: 10})
	if err != nil || len(nodes) != 1 {
		database.Close()
		t.Fatalf("ListNodes() = (%+v, %v)", nodes, err)
	}
	return database, nodes[0].ID
}
