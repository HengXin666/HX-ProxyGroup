package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/dataplane/mihomo"
)

type systemInfoDataPlane struct{}

func (systemInfoDataPlane) Apply(context.Context) error { return nil }
func (systemInfoDataPlane) Status() mihomo.Status {
	return mihomo.Status{Version: "Mihomo Meta 1.20.0"}
}

func TestSystemInfoAPI(t *testing.T) {
	t.Parallel()
	info := SystemInfo{
		Application:        "HX-ProxyGroup",
		Version:            "v1.2.3",
		RepositoryURL:      "https://github.com/HengXin666/HX-ProxyGroup",
		UpdateCommand:      "sudo hx-proxygroup-install upgrade",
		SupportedProtocols: []string{"VLESS", "TUIC"},
	}
	server, err := NewServer(
		&stubBundleService{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithSystemInfo(info),
		WithDataPlane(systemInfoDataPlane{}),
	)
	if err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET system info status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		SystemInfo
		DataPlaneVersion string `json:"dataplane_version"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Application != info.Application ||
		response.Version != info.Version ||
		response.RepositoryURL != info.RepositoryURL ||
		response.UpdateCommand != info.UpdateCommand ||
		!slices.Equal(response.SupportedProtocols, info.SupportedProtocols) ||
		response.DataPlaneVersion != "Mihomo Meta 1.20.0" {
		t.Fatalf("system info = %#v", response)
	}

	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/system/info", nil))
	if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("POST system info status = %d, Allow=%q", recorder.Code, recorder.Header().Get("Allow"))
	}
}

func TestSPAHandlerServesAssetsAndRouteFallback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<main>HX application</main>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("window.HX=true"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(
		&stubBundleService{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithWebRoot(root),
	)
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	for _, test := range []struct {
		name   string
		method string
		path   string
		status int
		body   string
	}{
		{name: "asset", method: http.MethodGet, path: "/assets/app.js", status: http.StatusOK, body: "window.HX=true"},
		{name: "route fallback", method: http.MethodGet, path: "/about", status: http.StatusOK, body: "HX application"},
		{name: "head fallback", method: http.MethodHead, path: "/subscriptions", status: http.StatusOK},
		{name: "write rejected", method: http.MethodPost, path: "/about", status: http.StatusMethodNotAllowed, body: "method not allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil))
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.body) {
				t.Fatalf("%s %s status=%d body=%q", test.method, test.path, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestWithWebRootRequiresIndex(t *testing.T) {
	t.Parallel()
	_, err := NewServer(
		&stubBundleService{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		WithWebRoot(t.TempDir()),
	)
	if err == nil {
		t.Fatal("WithWebRoot accepted a directory without index.html")
	}
}
