package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	storage, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { storage.Close() })
	return storage
}

func createTestProvider(t *testing.T, storage *Store, id, name string) ResidentialProviderRecord {
	t.Helper()
	now := time.Now().UTC()
	record, err := storage.CreateResidentialProvider(context.Background(), ResidentialProviderRecord{
		ID:                   id,
		Name:                 name,
		Vendor:               "bestproxy",
		Protocol:             "http",
		GatewayHost:          "gate.example.com",
		GatewayPort:          8000,
		CredentialsEncrypted: []byte{0x01, 0x02},
		UsernameTemplate:     "{user}-session-{session}",
		RotationMode:         "session-template",
		SessionTTLSeconds:    600,
		PoolSize:             4,
		Enabled:              true,
		Version:              1,
		CreatedAt:            now,
		UpdatedAt:            now,
	})
	if err != nil {
		t.Fatalf("CreateResidentialProvider() error = %v", err)
	}
	return record
}

func TestResidentialProviderLifecycle(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t)

	created := createTestProvider(t, storage, "provider-1", "bestproxy-main")
	fetched, err := storage.GetResidentialProvider(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetResidentialProvider() error = %v", err)
	}
	if fetched.Name != "bestproxy-main" || fetched.GatewayPort != 8000 {
		t.Fatalf("unexpected provider %+v", fetched)
	}
	if string(fetched.CredentialsEncrypted) != string([]byte{0x01, 0x02}) {
		t.Fatalf("credentials were not persisted verbatim")
	}

	// A duplicate name must conflict rather than silently create a second row.
	duplicate := created
	duplicate.ID = "provider-2"
	if _, err := storage.CreateResidentialProvider(ctx, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name error = %v, want ErrConflict", err)
	}

	fetched.PoolSize = 8
	fetched.UpdatedAt = time.Now().UTC()
	updated, err := storage.UpdateResidentialProvider(ctx, fetched, fetched.Version)
	if err != nil {
		t.Fatalf("UpdateResidentialProvider() error = %v", err)
	}
	if updated.PoolSize != 8 || updated.Version != fetched.Version+1 {
		t.Fatalf("unexpected update result %+v", updated)
	}
	// A stale version must be rejected.
	if _, err := storage.UpdateResidentialProvider(ctx, fetched, fetched.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want ErrConflict", err)
	}

	if err := storage.DeleteResidentialProvider(ctx, updated.ID, updated.Version); err != nil {
		t.Fatalf("DeleteResidentialProvider() error = %v", err)
	}
	if _, err := storage.GetResidentialProvider(ctx, updated.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete error = %v, want ErrNotFound", err)
	}
}

func TestResidentialChannelRotationState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t)
	now := time.Now().UTC()

	provider := createTestProvider(t, storage, "provider-1", "bestproxy-main")
	group, err := storage.CreateProxyGroup(ctx, ProxyGroupRecord{
		ID:               "group-1",
		Name:             "residential-sticky",
		Strategy:         "manual",
		SourceSpecJSON:   `{"node_ids":[],"include_direct":false}`,
		RulePipelineJSON: "{}",
		Enabled:          true,
		EmptyBehavior:    "fail-closed",
		Version:          1,
		CreatedAt:        now,
		UpdatedAt:        now,
	})
	if err != nil {
		t.Fatalf("CreateProxyGroup() error = %v", err)
	}
	listenerRecord, err := storage.CreateListener(ctx, ListenerRecord{
		ID:                  "listener-1",
		Name:                "residential-entry",
		Kind:                "mixed",
		BindAddress:         "127.0.0.1",
		Port:                29080,
		ProxyGroupID:        group.ID,
		AuthMode:            "none",
		TransportJSON:       "{}",
		PublicEndpointJSON:  "{}",
		ShareToken:          "share-token-1",
		AuthConfigEncrypted: nil,
		Enabled:             true,
		Version:             1,
		CreatedAt:           now,
		UpdatedAt:           now,
	})
	if err != nil {
		t.Fatalf("CreateListener() error = %v", err)
	}

	channel, err := storage.CreateResidentialChannel(ctx, ResidentialChannelRecord{
		ID:           "channel-1",
		Name:         "sticky-us",
		ProviderID:   provider.ID,
		Mode:         "sticky",
		ProxyGroupID: group.ID,
		ListenerID:   listenerRecord.ID,
		Region:       "us",
		RotateToken:  "rotate-token-1",
		Enabled:      true,
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		t.Fatalf("CreateResidentialChannel() error = %v", err)
	}
	if channel.LastRotatedAt != nil {
		t.Fatalf("a new channel must not carry a rotation timestamp")
	}

	byToken, err := storage.GetResidentialChannelByRotateToken(ctx, "rotate-token-1")
	if err != nil {
		t.Fatalf("GetResidentialChannelByRotateToken() error = %v", err)
	}
	if byToken.ID != channel.ID {
		t.Fatalf("token lookup returned %q, want %q", byToken.ID, channel.ID)
	}
	if _, err := storage.GetResidentialChannelByRotateToken(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token error = %v, want ErrNotFound", err)
	}

	// Rotation is runtime state: it records progress without consuming the
	// optimistic-concurrency version used by configuration edits.
	rotated, err := storage.SetResidentialChannelRotation(ctx, channel.ID, 3, "203.0.113.7", time.Now().UTC())
	if err != nil {
		t.Fatalf("SetResidentialChannelRotation() error = %v", err)
	}
	if rotated.ActiveSessionIndex != 3 || rotated.LastExitIP != "203.0.113.7" {
		t.Fatalf("unexpected rotation state %+v", rotated)
	}
	if rotated.RotateCount != 1 || rotated.LastRotatedAt == nil {
		t.Fatalf("rotation counters were not updated: %+v", rotated)
	}
	if rotated.Version != channel.Version {
		t.Fatalf("rotation changed the config version to %d", rotated.Version)
	}

	retokened, err := storage.RotateResidentialChannelToken(ctx, channel.ID, "rotate-token-2")
	if err != nil {
		t.Fatalf("RotateResidentialChannelToken() error = %v", err)
	}
	if retokened.RotateToken != "rotate-token-2" {
		t.Fatalf("token was not replaced: %+v", retokened)
	}
	if _, err := storage.GetResidentialChannelByRotateToken(ctx, "rotate-token-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token still resolves, error = %v", err)
	}

	// A provider that still has channels must not be deletable.
	if err := storage.DeleteResidentialProvider(ctx, provider.ID, provider.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete referenced provider error = %v, want ErrConflict", err)
	}
}

func TestResidentialSessionPoolReplaceAndCandidates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t)
	now := time.Now().UTC()

	sessions := []ResidentialSessionNode{
		{ID: "node-r1", Fingerprint: "fp-1", DisplayName: "us-session-1", Protocol: "http", CanonicalConfigEncrypted: []byte{0x11}},
		{ID: "node-r2", Fingerprint: "fp-2", DisplayName: "us-session-2", Protocol: "http", CanonicalConfigEncrypted: []byte{0x12}},
	}
	ids, err := storage.ReplaceResidentialSessionPool(ctx, "channel-1", sessions, now)
	if err != nil {
		t.Fatalf("ReplaceResidentialSessionPool() error = %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("pool returned %d ids, want 2", len(ids))
	}

	pooled, err := storage.ListResidentialSessionNodes(ctx, "channel-1")
	if err != nil {
		t.Fatalf("ListResidentialSessionNodes() error = %v", err)
	}
	if len(pooled) != 2 {
		t.Fatalf("pool contains %d nodes, want 2", len(pooled))
	}

	// Residential nodes have no active snapshot, so they must still surface as
	// group candidates and must not carry a subscription id.
	candidates, err := storage.ListGroupNodeCandidates(ctx)
	if err != nil {
		t.Fatalf("ListGroupNodeCandidates() error = %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %d, want 2", len(candidates))
	}
	for _, candidate := range candidates {
		if len(candidate.SubscriptionIDs) != 0 {
			t.Fatalf("residential candidate %q reported subscriptions %v", candidate.ID, candidate.SubscriptionIDs)
		}
	}

	// Replacing the pool retires sessions that are no longer present and keeps
	// the identity of sessions whose fingerprint is unchanged.
	refreshed := []ResidentialSessionNode{
		{ID: "node-r1", Fingerprint: "fp-1", DisplayName: "us-session-1", Protocol: "http", CanonicalConfigEncrypted: []byte{0x11}},
		{ID: "node-r3", Fingerprint: "fp-3", DisplayName: "us-session-3", Protocol: "http", CanonicalConfigEncrypted: []byte{0x13}},
	}
	if _, err := storage.ReplaceResidentialSessionPool(ctx, "channel-1", refreshed, now); err != nil {
		t.Fatalf("ReplaceResidentialSessionPool(refresh) error = %v", err)
	}
	pooled, err = storage.ListResidentialSessionNodes(ctx, "channel-1")
	if err != nil {
		t.Fatalf("ListResidentialSessionNodes(after refresh) error = %v", err)
	}
	if len(pooled) != 2 {
		t.Fatalf("refreshed pool contains %d nodes, want 2", len(pooled))
	}
	names := map[string]bool{}
	for _, node := range pooled {
		names[node.DisplayName] = true
	}
	if !names["us-session-1"] || !names["us-session-3"] || names["us-session-2"] {
		t.Fatalf("unexpected refreshed pool contents: %v", names)
	}

	if err := storage.DeleteResidentialSessionPool(ctx, "channel-1"); err != nil {
		t.Fatalf("DeleteResidentialSessionPool() error = %v", err)
	}
	pooled, err = storage.ListResidentialSessionNodes(ctx, "channel-1")
	if err != nil {
		t.Fatalf("ListResidentialSessionNodes(after delete) error = %v", err)
	}
	if len(pooled) != 0 {
		t.Fatalf("pool still contains %d nodes after delete", len(pooled))
	}
}
