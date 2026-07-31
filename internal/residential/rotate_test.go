package residential

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRotateAdvancesThroughPoolWithoutRecompiling(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-rotate",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   3,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29201},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	// Creation performed one selector call and published the group once.
	applyCallsAfterCreate := harness.reconciler.calls
	selectorCallsAfterCreate := len(harness.selector.calls())

	pool, err := harness.store.ListResidentialSessionNodes(ctx, channel.ID)
	if err != nil {
		t.Fatalf("ListResidentialSessionNodes() error = %v", err)
	}
	first := nodeProxyName(pool[0].Fingerprint)
	second := nodeProxyName(pool[1].Fingerprint)
	third := nodeProxyName(pool[2].Fingerprint)

	result, err := harness.service.RotateChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("RotateChannel() error = %v", err)
	}
	if result.SessionIndex != 1 {
		t.Fatalf("SessionIndex = %d, want 1", result.SessionIndex)
	}
	if result.PoolRefreshed {
		t.Fatal("rotating inside the pool must not regenerate it")
	}
	// The decisive assertion: rotation moved the selector and did NOT trigger a
	// data-plane configuration apply.
	if harness.reconciler.calls != applyCallsAfterCreate {
		t.Fatalf("rotation triggered %d extra config applies, want 0",
			harness.reconciler.calls-applyCallsAfterCreate)
	}
	calls := harness.selector.calls()
	if len(calls) != selectorCallsAfterCreate+1 {
		t.Fatalf("selector calls = %d, want %d", len(calls), selectorCallsAfterCreate+1)
	}
	if calls[len(calls)-1][1] != second {
		t.Fatalf("selector switched to %q, want %q", calls[len(calls)-1][1], second)
	}

	result, err = harness.service.RotateChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("RotateChannel(second) error = %v", err)
	}
	if result.SessionIndex != 2 {
		t.Fatalf("SessionIndex = %d, want 2", result.SessionIndex)
	}
	calls = harness.selector.calls()
	if calls[len(calls)-1][1] != third {
		t.Fatalf("selector switched to %q, want %q", calls[len(calls)-1][1], third)
	}
	_ = first
}

// Once every pooled session has been used, the pool is regenerated so the next
// cycle draws fresh residential IPs rather than replaying the same ones.
func TestRotateRegeneratesPoolWhenExhausted(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-exhaust",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   2,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29202},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	before, err := harness.store.ListResidentialSessionNodes(ctx, channel.ID)
	if err != nil {
		t.Fatalf("ListResidentialSessionNodes() error = %v", err)
	}
	originalFingerprints := map[string]bool{}
	for _, node := range before {
		originalFingerprints[node.Fingerprint] = true
	}

	// Index 0 -> 1: still inside the pool.
	if result, err := harness.service.RotateChannel(ctx, channel.ID); err != nil {
		t.Fatalf("RotateChannel(1) error = %v", err)
	} else if result.PoolRefreshed {
		t.Fatal("first rotation must not refresh the pool")
	}
	// Index 1 -> wraps: the pool is exhausted and must be regenerated.
	result, err := harness.service.RotateChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("RotateChannel(2) error = %v", err)
	}
	if !result.PoolRefreshed {
		t.Fatal("wrapping past the pool end must regenerate the pool")
	}
	if result.SessionIndex != 0 {
		t.Fatalf("SessionIndex after refresh = %d, want 0", result.SessionIndex)
	}

	after, err := harness.store.ListResidentialSessionNodes(ctx, channel.ID)
	if err != nil {
		t.Fatalf("ListResidentialSessionNodes(after) error = %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("regenerated pool size = %d, want 2", len(after))
	}
	for _, node := range after {
		if originalFingerprints[node.Fingerprint] {
			t.Fatalf("regenerated pool reused session fingerprint %q", node.Fingerprint)
		}
	}
}

func TestRotateEnforcesPerChannelRateLimit(t *testing.T) {
	t.Parallel()
	harness := newHarness(t, WithRotateInterval(time.Hour))
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-limited",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   4,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29203},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	if _, err := harness.service.RotateChannel(ctx, channel.ID); err != nil {
		t.Fatalf("first RotateChannel() error = %v", err)
	}
	_, err = harness.service.RotateChannel(ctx, channel.ID)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second RotateChannel() error = %v, want ErrRateLimited", err)
	}

	// A second channel must not be blocked by the first channel's limit.
	other, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-other",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   4,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29204},
	})
	if err != nil {
		t.Fatalf("CreateChannel(other) error = %v", err)
	}
	if _, err := harness.service.RotateChannel(ctx, other.ID); err != nil {
		t.Fatalf("RotateChannel(other) error = %v", err)
	}
}

func TestRotateByTokenResolvesOnlyEligibleChannels(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-token",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   3,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29205},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	record, err := harness.store.GetResidentialChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("GetResidentialChannel() error = %v", err)
	}
	token := record.RotateToken

	result, err := harness.service.RotateChannelByToken(ctx, token)
	if err != nil {
		t.Fatalf("RotateChannelByToken() error = %v", err)
	}
	if result.ChannelID != channel.ID || result.SessionIndex != 1 {
		t.Fatalf("unexpected rotation result %+v", result)
	}

	status, err := harness.service.ChannelStatusByToken(ctx, token)
	if err != nil {
		t.Fatalf("ChannelStatusByToken() error = %v", err)
	}
	if status.SessionIndex != 1 || status.PoolSize != 3 || status.RotateCount != 1 {
		t.Fatalf("unexpected status %+v", status)
	}

	for _, unknown := range []string{"", "   ", "deadbeef"} {
		if _, err := harness.service.RotateChannelByToken(ctx, unknown); !errors.Is(err, ErrNotFound) {
			t.Fatalf("RotateChannelByToken(%q) error = %v, want ErrNotFound", unknown, err)
		}
	}
}

// A disabled channel's token must not reveal that the token exists.
func TestRotateByTokenRejectsDisabledChannel(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-disable",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   2,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29206},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	record, err := harness.store.GetResidentialChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("GetResidentialChannel() error = %v", err)
	}
	token := record.RotateToken

	if _, err := harness.service.UpdateChannel(ctx, channel.ID, UpdateChannelRequest{
		Version: channel.Version,
		Name:    channel.Name,
		Region:  channel.Region,
		Enabled: false,
	}); err != nil {
		t.Fatalf("UpdateChannel() error = %v", err)
	}
	if _, err := harness.service.RotateChannelByToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RotateChannelByToken(disabled) error = %v, want ErrNotFound", err)
	}
	if _, err := harness.service.ChannelStatusByToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ChannelStatusByToken(disabled) error = %v, want ErrNotFound", err)
	}
}

// Rotating a token replaces it, so previously distributed rotate URLs stop
// working immediately.
func TestRotateTokenInvalidatesPreviousToken(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-retoken",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   2,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29207},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	original, err := harness.store.GetResidentialChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("GetResidentialChannel() error = %v", err)
	}

	updated, err := harness.service.RotateChannelToken(ctx, channel.ID)
	if err != nil {
		t.Fatalf("RotateChannelToken() error = %v", err)
	}
	if updated.RotatePath == "/rot/"+original.RotateToken {
		t.Fatal("RotateChannelToken() did not change the token")
	}
	if _, err := harness.service.RotateChannelByToken(ctx, original.RotateToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token still works, error = %v", err)
	}
}

// The reachability check runs after the switch but must not fail the rotation:
// the selector has already moved and the consumer would see the same failure.
func TestRotateSurvivesReachabilityCheckFailure(t *testing.T) {
	t.Parallel()
	harness := newHarness(t, WithReachabilityChecker(&stubChecker{err: errors.New("unreachable")}))
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-unreachable",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   3,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29208},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	result, err := harness.service.RotateChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("RotateChannel() error = %v", err)
	}
	if result.LatencyMS != 0 {
		t.Fatalf("LatencyMS = %d, want 0 when the check failed", result.LatencyMS)
	}
	if result.SessionIndex != 1 {
		t.Fatalf("SessionIndex = %d, want 1", result.SessionIndex)
	}
}

func TestRotateReportsReachabilityLatency(t *testing.T) {
	t.Parallel()
	harness := newHarness(t, WithReachabilityChecker(&stubChecker{latency: 87}))
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-latency",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   3,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29209},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	result, err := harness.service.RotateChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("RotateChannel() error = %v", err)
	}
	if result.LatencyMS != 87 {
		t.Fatalf("LatencyMS = %d, want 87", result.LatencyMS)
	}
}

// A single-session pool cannot produce a different IP by switching, so the pool
// is regenerated in place instead of pointlessly reselecting the same session.
func TestRotateSingleSessionPoolStaysAtIndexZero(t *testing.T) {
	t.Parallel()
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)

	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:       "sticky-single",
		ProviderID: provider.ID,
		Mode:       ModeSticky,
		PoolSize:   1,
		Listener:   ChannelListenerRequest{Kind: "mixed", BindAddress: "127.0.0.1", Port: 29210},
	})
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	if channel.CanRotate {
		t.Fatal("a single-session pool must not advertise rotation")
	}
	result, err := harness.service.RotateChannel(ctx, channel.ID)
	if err != nil {
		t.Fatalf("RotateChannel() error = %v", err)
	}
	if result.SessionIndex != 0 {
		t.Fatalf("SessionIndex = %d, want 0", result.SessionIndex)
	}
}

type stubChecker struct {
	latency int
	err     error
}

func (checker *stubChecker) CheckProxyReachable(context.Context, string) (int, error) {
	return checker.latency, checker.err
}
