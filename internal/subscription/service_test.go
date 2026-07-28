package subscription

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func TestSubscriptionLifecycleEncryptsSourceConfiguration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	databasePath := filepath.Join(root, "hx-proxygroup.db")
	database, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer database.Close()
	box, err := secret.New(bytes.Repeat([]byte{0x23}, 32))
	if err != nil {
		t.Fatalf("secret.New() error = %v", err)
	}
	service, err := NewService(database, box)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	secretURL := "https://subscription.example.invalid/list?token=secret-token-value"
	secretHeader := "Bearer private-header-value"
	created, err := service.Create(ctx, CreateRequest{
		Name:       "primary-airport",
		SourceType: SourceRemote,
		SourceConfig: SourceConfig{
			URL:       secretURL,
			Headers:   map[string]string{"Authorization": secretHeader},
			UserAgent: "HX-ProxyGroup-Test",
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.Version != 1 || !created.Enabled || !created.SourceConfigured {
		t.Fatalf("created subscription = %#v", created)
	}

	configured, err := service.SourceConfig(ctx, created.ID)
	if err != nil {
		t.Fatalf("SourceConfig() error = %v", err)
	}
	if configured.URL != secretURL || configured.Headers["Authorization"] != secretHeader {
		t.Fatalf("decrypted source config = %#v", configured)
	}

	assertDatabaseDoesNotContain(t, root, []byte("secret-token-value"))
	assertDatabaseDoesNotContain(t, root, []byte("private-header-value"))

	updated, err := service.Update(ctx, created.ID, UpdateRequest{
		Version:    created.Version,
		Name:       "primary-airport-renamed",
		SourceType: SourceInline,
		SourceConfig: &SourceConfig{
			Inline: "vless://redacted-node",
		},
		Enabled:                false,
		RefreshIntervalSeconds: 7200,
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if updated.Version != 2 || updated.Enabled || updated.Name != "primary-airport-renamed" {
		t.Fatalf("updated subscription = %#v", updated)
	}
	kept, err := service.Update(ctx, created.ID, UpdateRequest{
		Version:                updated.Version,
		Name:                   "metadata-only-update",
		SourceType:             SourceInline,
		Enabled:                true,
		RefreshIntervalSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("metadata-only Update() error = %v", err)
	}
	keptConfig, err := service.SourceConfig(ctx, created.ID)
	if err != nil || keptConfig.Inline != "vless://redacted-node" || kept.SourceType != SourceInline {
		t.Fatalf("metadata-only source = %#v, subscription = %#v, error = %v", keptConfig, kept, err)
	}
	if _, err := service.Update(ctx, created.ID, UpdateRequest{
		Version:                1,
		Name:                   "stale-update",
		SourceType:             SourceInline,
		SourceConfig:           &SourceConfig{Inline: "vmess://stale"},
		Enabled:                true,
		RefreshIntervalSeconds: 3600,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Update() error = %v, want ErrConflict", err)
	}

	items, err := service.List(ctx, 100, 0)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("List() = %#v", items)
	}
	if err := service.Delete(ctx, created.ID, kept.Version); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := service.Get(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after delete error = %v, want ErrNotFound", err)
	}
}

func TestSubscriptionRejectsDuplicateNameAndInvalidSources(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	defer database.Close()
	box, err := secret.New(bytes.Repeat([]byte{0x45}, 32))
	if err != nil {
		t.Fatalf("secret.New() error = %v", err)
	}
	service, err := NewService(database, box)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	request := CreateRequest{
		Name:         "duplicate",
		SourceType:   SourceRemote,
		SourceConfig: SourceConfig{URL: "https://example.invalid/subscription"},
	}
	if _, err := service.Create(ctx, request); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	if _, err := service.Create(ctx, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate Create() error = %v, want ErrConflict", err)
	}

	invalidRequests := []CreateRequest{
		{Name: "bad-scheme", SourceType: SourceRemote, SourceConfig: SourceConfig{URL: "ftp://example.invalid/sub"}},
		{Name: "bad-header", SourceType: SourceRemote, SourceConfig: SourceConfig{URL: "https://example.invalid", Headers: map[string]string{"X-Test": "bad\r\nvalue"}}},
		{Name: "relative-file", SourceType: SourceFile, SourceConfig: SourceConfig{FilePath: "relative/path"}},
		{Name: "empty-inline", SourceType: SourceInline, SourceConfig: SourceConfig{}},
	}
	for _, invalid := range invalidRequests {
		if _, err := service.Create(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Errorf("Create(%s) error = %v, want ErrInvalid", invalid.Name, err)
		}
	}
}

func assertDatabaseDoesNotContain(t *testing.T, root string, plaintext []byte) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", entry.Name(), err)
		}
		if bytes.Contains(content, plaintext) {
			t.Fatalf("database artifact %q contains secret plaintext %q", entry.Name(), plaintext)
		}
	}
}
