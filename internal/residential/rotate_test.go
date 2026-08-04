package residential

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRotateClientSessionAllocatesFreshNodeAndKeepsClientCredentials(t *testing.T) {
	t.Parallel()
	router := &recordingSessionRouter{}
	harness := newHarness(t, WithSessionRouter(router))
	ctx := context.Background()
	provider := harness.createProvider(t)
	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name: "client-rotate", ProviderID: provider.ID, Mode: ModeSticky,
		PublicEndpoint: managedPublicEndpoint(),
	})
	if err != nil {
		t.Fatal(err)
	}
	channelRecord, _ := harness.store.GetResidentialChannel(ctx, channel.ID)
	created, err := harness.service.EnsureClientSessionByToken(ctx, channelRecord.RotateToken, "window-01")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := harness.store.GetResidentialClientSession(ctx, channel.ID, created.SessionID)
	rotated, err := harness.service.RotateClientSessionByToken(ctx, channelRecord.RotateToken, created.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := harness.store.GetResidentialClientSession(ctx, channel.ID, created.SessionID)
	if before.NodeFingerprint == after.NodeFingerprint {
		t.Fatal("manual rotation reused the previous node")
	}
	if rotated.ProxyUsername != created.ProxyUsername || after.RotateCount != 1 {
		t.Fatalf("rotated session = %+v", rotated)
	}
	if len(router.closed) != 1 || router.closed[0] != created.ProxyUsername {
		t.Fatalf("closed users = %v", router.closed)
	}
}

func TestRotateClientSessionIsRateLimitedPerLogicalSession(t *testing.T) {
	t.Parallel()
	harness := newHarness(t, WithRotateInterval(time.Minute))
	ctx := context.Background()
	provider := harness.createProvider(t)
	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name: "client-rate-limit", ProviderID: provider.ID, Mode: ModeSticky,
		PublicEndpoint: managedPublicEndpoint(),
	})
	if err != nil {
		t.Fatal(err)
	}
	channelRecord, _ := harness.store.GetResidentialChannel(ctx, channel.ID)
	created, err := harness.service.EnsureClientSessionByToken(ctx, channelRecord.RotateToken, "window-01")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.RotateClientSessionByToken(ctx, channelRecord.RotateToken, created.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.service.RotateClientSessionByToken(ctx, channelRecord.RotateToken, created.SessionID); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second rotation error = %v, want ErrRateLimited", err)
	}
}
