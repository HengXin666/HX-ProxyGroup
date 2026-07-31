package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/residential"
)

type stubResidentialRefresher struct {
	channels  []residential.Channel
	refreshed []string
	failOn    map[string]error
	listErr   error
}

func (stub *stubResidentialRefresher) ListChannels(context.Context) ([]residential.Channel, error) {
	return stub.channels, stub.listErr
}

func (stub *stubResidentialRefresher) RefreshChannelPool(_ context.Context, id string) error {
	if err, failing := stub.failOn[id]; failing {
		return err
	}
	stub.refreshed = append(stub.refreshed, id)
	return nil
}

func newTestScheduler(t *testing.T, stub *stubResidentialRefresher, config ResidentialConfig) *ResidentialScheduler {
	t.Helper()
	scheduler, err := NewResidentialScheduler(stub, slog.New(slog.NewTextHandler(io.Discard, nil)), config)
	if err != nil {
		t.Fatalf("NewResidentialScheduler() error = %v", err)
	}
	return scheduler
}

func TestResidentialSchedulerRejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, err := NewResidentialScheduler(nil, logger, ResidentialConfig{}); err == nil {
		t.Fatal("a nil service must be rejected")
	}
	cases := map[string]ResidentialConfig{
		"poll too short":      {PollInterval: time.Second},
		"session age too low": {MaximumSessionAge: time.Second},
		"refresh limit high":  {MaximumRefreshesPerPass: 1000},
	}
	for name, config := range cases {
		if _, err := NewResidentialScheduler(&stubResidentialRefresher{}, logger, config); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// An empty pool is unusable, so it must be refilled regardless of age.
func TestResidentialSchedulerRefillsEmptyPools(t *testing.T) {
	t.Parallel()
	stub := &stubResidentialRefresher{channels: []residential.Channel{
		{ID: "empty", Mode: residential.ModeSticky, PoolSize: 0, Enabled: true},
		{ID: "healthy", Mode: residential.ModeSticky, PoolSize: 4, Enabled: true},
	}}
	scheduler := newTestScheduler(t, stub, ResidentialConfig{})

	if err := scheduler.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}
	if len(stub.refreshed) != 1 || stub.refreshed[0] != "empty" {
		t.Fatalf("refreshed = %v, want only the empty pool", stub.refreshed)
	}
}

// A channel whose sessions have been in use past the vendor TTL must be
// regenerated so it cannot stay pinned to a dead exit.
func TestResidentialSchedulerRefreshesStaleSessions(t *testing.T) {
	t.Parallel()
	stale := time.Now().Add(-2 * time.Hour)
	fresh := time.Now().Add(-time.Minute)
	stub := &stubResidentialRefresher{channels: []residential.Channel{
		{ID: "stale", Mode: residential.ModeSticky, PoolSize: 4, Enabled: true, LastRotatedAt: &stale},
		{ID: "fresh", Mode: residential.ModeSticky, PoolSize: 4, Enabled: true, LastRotatedAt: &fresh},
	}}
	scheduler := newTestScheduler(t, stub, ResidentialConfig{MaximumSessionAge: time.Hour})

	if err := scheduler.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}
	if len(stub.refreshed) != 1 || stub.refreshed[0] != "stale" {
		t.Fatalf("refreshed = %v, want only the stale channel", stub.refreshed)
	}
}

func TestResidentialSchedulerSkipsDisabledPassthroughAndNeverRotated(t *testing.T) {
	t.Parallel()
	stale := time.Now().Add(-2 * time.Hour)
	stub := &stubResidentialRefresher{channels: []residential.Channel{
		{ID: "disabled", Mode: residential.ModeSticky, PoolSize: 4, Enabled: false, LastRotatedAt: &stale},
		{ID: "passthrough", Mode: residential.ModePassthrough, PoolSize: 1, Enabled: true, LastRotatedAt: &stale},
		{ID: "never-rotated", Mode: residential.ModeSticky, PoolSize: 4, Enabled: true},
	}}
	scheduler := newTestScheduler(t, stub, ResidentialConfig{MaximumSessionAge: time.Hour})

	if err := scheduler.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}
	if len(stub.refreshed) != 0 {
		t.Fatalf("refreshed = %v, want none", stub.refreshed)
	}
}

// Each refresh republishes a data plane configuration, so a single pass must be
// bounded rather than attempting every stale channel at once.
func TestResidentialSchedulerBoundsWorkPerPass(t *testing.T) {
	t.Parallel()
	stale := time.Now().Add(-2 * time.Hour)
	channels := make([]residential.Channel, 0, 10)
	for index := 0; index < 10; index++ {
		channels = append(channels, residential.Channel{
			ID:            string(rune('a' + index)),
			Mode:          residential.ModeSticky,
			PoolSize:      4,
			Enabled:       true,
			LastRotatedAt: &stale,
		})
	}
	stub := &stubResidentialRefresher{channels: channels}
	scheduler := newTestScheduler(t, stub, ResidentialConfig{
		MaximumSessionAge:       time.Hour,
		MaximumRefreshesPerPass: 3,
	})

	if err := scheduler.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}
	if len(stub.refreshed) != 3 {
		t.Fatalf("refreshed %d channels, want the per-pass limit of 3", len(stub.refreshed))
	}
}

// One failing channel must not prevent the remaining channels from refreshing.
func TestResidentialSchedulerContinuesAfterOneFailure(t *testing.T) {
	t.Parallel()
	stale := time.Now().Add(-2 * time.Hour)
	stub := &stubResidentialRefresher{
		channels: []residential.Channel{
			{ID: "broken", Mode: residential.ModeSticky, PoolSize: 4, Enabled: true, LastRotatedAt: &stale},
			{ID: "working", Mode: residential.ModeSticky, PoolSize: 4, Enabled: true, LastRotatedAt: &stale},
		},
		failOn: map[string]error{"broken": errors.New("gateway unreachable")},
	}
	scheduler := newTestScheduler(t, stub, ResidentialConfig{MaximumSessionAge: time.Hour})

	if err := scheduler.runOnce(context.Background()); err != nil {
		t.Fatalf("runOnce() error = %v", err)
	}
	if len(stub.refreshed) != 1 || stub.refreshed[0] != "working" {
		t.Fatalf("refreshed = %v, want the working channel", stub.refreshed)
	}
}

func TestResidentialSchedulerStopsOnContextCancel(t *testing.T) {
	t.Parallel()
	stub := &stubResidentialRefresher{}
	scheduler := newTestScheduler(t, stub, ResidentialConfig{PollInterval: 30 * time.Second})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}
}
