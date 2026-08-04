package clientsubscription

import (
	"context"
	"errors"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

type memoryRepository struct{ values map[string]string }

func (repository *memoryRepository) GetMetadata(_ context.Context, key string) (string, error) {
	value, exists := repository.values[key]
	if !exists {
		return "", store.ErrNotFound
	}
	return value, nil
}

func (repository *memoryRepository) SetMetadata(_ context.Context, key, value string) error {
	repository.values[key] = value
	return nil
}

type catalogListeners struct {
	exports map[string]listener.ShareExport
	calls   []string
	listErr error
}

func (source *catalogListeners) List(context.Context) ([]listener.Listener, error) {
	if source.listErr != nil {
		return nil, source.listErr
	}
	return []listener.Listener{
		{ID: "residential-listener", Name: "internal", Enabled: true},
		{ID: "normal-listener", Name: "normal", Enabled: true},
	}, nil
}

func TestRotateKeepsPublishedTokenWhenCandidateBuildFails(t *testing.T) {
	repository := &memoryRepository{values: map[string]string{catalogTokenMetadataKey: "0123456789abcdef0123456789abcdef"}}
	source := &catalogListeners{listErr: errors.New("render failed")}
	service, err := NewService(repository, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Rotate(context.Background(), "proxy.example.com"); err == nil {
		t.Fatal("Rotate() succeeded while the catalog could not be built")
	}
	if got := repository.values[catalogTokenMetadataKey]; got != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("published token changed to %q", got)
	}
}

func (source *catalogListeners) ExportByID(_ context.Context, id, _ string) (listener.ShareExport, error) {
	source.calls = append(source.calls, id)
	export, exists := source.exports[id]
	if !exists {
		return listener.ShareExport{}, listener.ErrNotFound
	}
	return export, nil
}

type catalogContributor struct{ export listener.ShareExport }

func (contributor catalogContributor) CatalogEntries(context.Context, string) ([]listener.ShareExport, []string, error) {
	return []listener.ShareExport{contributor.export}, []string{"residential-listener"}, nil
}

func TestUnifiedCatalogExcludesOwnedListenersAndRotatesToken(t *testing.T) {
	repository := &memoryRepository{values: make(map[string]string)}
	normal := listener.NewShareExport("normal", "http", "203.0.113.10", 8080, []listener.ShareNode{{Name: "normal"}}, listener.Transport{}, listener.PublicEndpoint{})
	residential := listener.NewShareExport("residential", "vless", "proxy.example.com", 443, []listener.ShareNode{{Name: "residential-01", Auth: &listener.Auth{Password: "11111111-1111-4111-8111-111111111111"}}}, listener.Transport{Type: "ws", WSPath: "/__hx-proxy__/residential"}, listener.PublicEndpoint{Host: "proxy.example.com", Port: 443, TLS: true})
	source := &catalogListeners{exports: map[string]listener.ShareExport{"normal-listener": normal}}
	service, err := NewService(repository, source, catalogContributor{export: residential})
	if err != nil {
		t.Fatal(err)
	}
	info, err := service.Info(context.Background(), "proxy.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if info.NodeCount != 2 || len(source.calls) != 1 || source.calls[0] != "normal-listener" {
		t.Fatalf("info = %+v, listener calls = %v", info, source.calls)
	}
	token := info.SharePath[len("/sub/"):]
	bundle, matched, err := service.ExportByToken(context.Background(), token, "proxy.example.com")
	if err != nil || !matched || bundle.NodeCount() != 2 {
		t.Fatalf("ExportByToken() = (%+v, %t, %v)", bundle, matched, err)
	}
	rotated, err := service.Rotate(context.Background(), "proxy.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.SharePath == info.SharePath {
		t.Fatal("Rotate() kept the old catalog token")
	}
	_, matched, err = service.ExportByToken(context.Background(), token, "proxy.example.com")
	if err != nil || matched {
		t.Fatalf("old token match = %t, err = %v", matched, err)
	}
}
