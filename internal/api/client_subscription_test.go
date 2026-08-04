package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/clientsubscription"
	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
)

type clientSubscriptionAPIStub struct {
	rotated bool
	bundle  listener.ShareBundle
}

func (stub *clientSubscriptionAPIStub) Info(context.Context, string) (clientsubscription.Info, error) {
	return clientsubscription.Info{SharePath: "/sub/catalog-token", NodeCount: stub.bundle.NodeCount()}, nil
}

func (stub *clientSubscriptionAPIStub) Rotate(context.Context, string) (clientsubscription.Info, error) {
	stub.rotated = true
	return clientsubscription.Info{SharePath: "/sub/rotated-token", NodeCount: stub.bundle.NodeCount()}, nil
}

func (stub *clientSubscriptionAPIStub) ExportByToken(_ context.Context, token, _ string) (listener.ShareBundle, bool, error) {
	return stub.bundle, token == "catalog-token", nil
}

func TestUnifiedClientSubscriptionAPIAndPublicExport(t *testing.T) {
	export := listener.NewShareExport(
		"ordinary",
		"http",
		"proxy.example",
		8080,
		[]listener.ShareNode{{Name: "ordinary", Auth: &listener.Auth{Username: "user", Password: "secret"}}},
		listener.Transport{},
		listener.PublicEndpoint{},
	)
	stub := &clientSubscriptionAPIStub{bundle: listener.NewShareBundle("HX-ProxyGroup", []listener.ShareExport{export})}
	server, err := NewServer(
		&stubBundleService{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithClientSubscriptions(stub),
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/client-subscription", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"node_count":1`) {
		t.Fatalf("info status=%d body=%s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/client-subscription", strings.NewReader(`{"action":"rotate"}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !stub.rotated || !strings.Contains(response.Body.String(), "rotated-token") {
		t.Fatalf("rotate status=%d rotated=%t body=%s", response.Code, stub.rotated, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sub/catalog-token?format=clash", nil))
	if response.Code != http.StatusOK || response.Header().Get("X-HX-Subscription-Format") != "clash" || !strings.Contains(response.Body.String(), "proxy.example") {
		t.Fatalf("public export status=%d format=%q body=%s", response.Code, response.Header().Get("X-HX-Subscription-Format"), response.Body.String())
	}
}
