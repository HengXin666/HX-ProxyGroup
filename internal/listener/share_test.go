package listener

import (
	"context"
	"encoding/base64"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

type shareTestReconciler struct{}

func (shareTestReconciler) Apply(context.Context) error { return nil }

func newShareTestService(t *testing.T) (*Service, *store.Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(database, box, shareTestReconciler{})
	if err != nil {
		t.Fatal(err)
	}
	return service, database, ctx
}

func createShareTestGroup(t *testing.T, ctx context.Context, database *store.Store) string {
	t.Helper()
	record := store.ProxyGroupRecord{
		ID:               "group-share-test",
		Name:             "share-test",
		Strategy:         "manual",
		SourceSpecJSON:   `{"node_ids":[],"include_direct":true}`,
		RulePipelineJSON: "{}",
		Enabled:          true,
		EmptyBehavior:    "direct",
		Version:          1,
	}
	if _, err := database.CreateProxyGroup(ctx, record); err != nil {
		t.Fatal(err)
	}
	return record.ID
}

func TestShareExportRendersAuthenticatedURIs(t *testing.T) {
	service, database, ctx := newShareTestService(t)
	groupID := createShareTestGroup(t, ctx, database)

	created, err := service.Create(ctx, CreateRequest{
		Name:         "边缘出口",
		Kind:         "mixed",
		BindAddress:  "127.0.0.1",
		Port:         7890,
		ProxyGroupID: groupID,
		Auth:         &Auth{Username: "user", Password: "pa:ss@word"},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.SharePath == "" || !strings.HasPrefix(created.SharePath, "/sub/") {
		t.Fatalf("share path missing: %+v", created)
	}
	token := strings.TrimPrefix(created.SharePath, "/sub/")

	export, err := service.ExportByShareToken(ctx, token, "192.168.1.20:19090")
	if err != nil {
		t.Fatalf("ExportByShareToken() error = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(export.Body), "\n")
	if len(lines) != 2 {
		t.Fatalf("mixed listener should export two URIs, got %v", lines)
	}
	// Loopback bind must be replaced with the host the admin API was
	// reached on, and credentials must be URL-escaped.
	if !strings.Contains(lines[0], "192.168.1.20:7890") {
		t.Fatalf("unexpected host in %q", lines[0])
	}
	if !strings.Contains(lines[0], "user:pa%3Ass%40word@") {
		t.Fatalf("credentials not escaped in %q", lines[0])
	}
	if !strings.HasPrefix(lines[0], "http://") || !strings.HasPrefix(lines[1], "socks5://") {
		t.Fatalf("unexpected schemes: %v", lines)
	}

	decoded, err := base64.StdEncoding.DecodeString(export.EncodeSubscription())
	if err != nil || string(decoded) != export.Body {
		t.Fatalf("EncodeSubscription() roundtrip failed: %v", err)
	}

	if _, err := service.ExportByShareToken(ctx, "0000000000000000", "host"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token error = %v, want ErrNotFound", err)
	}
}

func TestShareExportRejectsDisabledListenerAndRotates(t *testing.T) {
	service, database, ctx := newShareTestService(t)
	groupID := createShareTestGroup(t, ctx, database)

	created, err := service.Create(ctx, CreateRequest{
		Name:         "rotate-me",
		Kind:         "http",
		BindAddress:  "127.0.0.1",
		Port:         7891,
		ProxyGroupID: groupID,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldToken := strings.TrimPrefix(created.SharePath, "/sub/")

	rotated, err := service.RotateShareToken(ctx, created.ID)
	if err != nil {
		t.Fatalf("RotateShareToken() error = %v", err)
	}
	newToken := strings.TrimPrefix(rotated.SharePath, "/sub/")
	if newToken == oldToken || len(newToken) != 32 {
		t.Fatalf("token not rotated: old=%s new=%s", oldToken, newToken)
	}
	if _, err := service.ExportByShareToken(ctx, oldToken, "host"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old token error = %v, want ErrNotFound", err)
	}

	updated, err := service.Update(ctx, created.ID, UpdateRequest{
		Version:      rotated.Version,
		Name:         created.Name,
		Kind:         created.Kind,
		BindAddress:  created.BindAddress,
		Port:         created.Port,
		ProxyGroupID: groupID,
		Enabled:      false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Fatal("listener should be disabled")
	}
	if _, err := service.ExportByShareToken(ctx, newToken, "host"); !errors.Is(err, ErrShareDisabled) {
		t.Fatalf("disabled listener error = %v, want ErrShareDisabled", err)
	}
}
