package residential

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
)

func TestDeclaredSessionsExportAndControlUseStableCredentials(t *testing.T) {
	harness := newHarness(t)
	ctx := context.Background()
	provider := harness.createProvider(t)
	channel, err := harness.service.CreateChannel(ctx, CreateChannelRequest{
		Name:         "residential-us",
		ProviderID:   provider.ID,
		Mode:         ModeSticky,
		SessionCount: 2,
		Listener: ChannelListenerRequest{
			Kind:        "vless",
			BindAddress: "127.0.0.1",
			Port:        29501,
			Auth:        &listener.Auth{Username: "bootstrap", Password: "00000000-0000-4000-8000-000000000000"},
			Transport:   listener.Transport{Type: "ws", WSPath: "/residential-us"},
		},
		DirectListener: &ChannelListenerRequest{
			Kind:        "mixed",
			BindAddress: "203.0.113.10",
			Port:        29502,
			Auth:        &listener.Auth{Username: "bootstrap", Password: "bootstrap-secret"},
		},
		PublicEndpoint: listener.PublicEndpoint{Host: "proxy.example.com", Port: 443, TLS: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(channel.Endpoint.SharePath, "/sub/")
	bundle, matched, err := harness.service.ExportByShareToken(ctx, token, "proxy.example.com")
	if err != nil || !matched {
		t.Fatalf("ExportByShareToken() = (%+v, %t, %v)", bundle, matched, err)
	}
	if bundle.NodeCount() != 4 || len(bundle.Exports) != 2 {
		t.Fatalf("bundle = %+v", bundle)
	}
	if bundle.Exports[0].Nodes[0].Name != "residential-us-01-ws" ||
		bundle.Exports[1].Nodes[1].Name != "residential-us-02-direct" {
		t.Fatalf("exported node names = %+v", bundle.Exports)
	}
	firstPassword := bundle.Exports[0].Nodes[0].Auth.Password
	secondPassword := bundle.Exports[0].Nodes[1].Auth.Password
	if firstPassword == "" || firstPassword == secondPassword {
		t.Fatal("declared sessions did not receive independent credentials")
	}

	controlToken := strings.TrimPrefix(channel.ControlPath, "/ctl/")
	control, err := harness.service.ControlNodesByToken(ctx, controlToken)
	if err != nil {
		t.Fatal(err)
	}
	if len(control.Nodes) != 2 || control.Nodes[0].ProxyURL == nil {
		t.Fatalf("control nodes = %+v", control.Nodes)
	}
	parsed, err := url.Parse(*control.Nodes[0].ProxyURL)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if parsed.Scheme != "http" || parsed.Host != "203.0.113.10:29502" || password != firstPassword {
		t.Fatalf("control proxy URL = %s", parsed.Redacted())
	}

	before, err := harness.store.GetResidentialClientSession(ctx, channel.ID, "s01")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := harness.service.RotateDeclaredSessionByControlToken(ctx, controlToken, 1)
	if err != nil {
		t.Fatal(err)
	}
	after, err := harness.store.GetResidentialClientSession(ctx, channel.ID, "s01")
	if err != nil {
		t.Fatal(err)
	}
	if before.NodeFingerprint == after.NodeFingerprint || rotated.ProxyURL == nil || *rotated.ProxyURL != *control.Nodes[0].ProxyURL {
		t.Fatalf("rotation changed client identity or kept allocation: before=%+v after=%+v node=%+v", before, after, rotated)
	}
}
