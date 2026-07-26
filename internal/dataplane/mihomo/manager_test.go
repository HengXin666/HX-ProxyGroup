package mihomo

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	listenerservice "github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func TestManagerRunsDirectMixedListener(t *testing.T) {
	binary, err := exec.LookPath("mihomo")
	if err != nil {
		t.Skip("mihomo is not available")
	}
	ctx := context.Background()
	directory := t.TempDir()
	database, err := store.Open(ctx, filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	compiler, err := NewCompiler(database, box)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(
		compiler,
		binary,
		filepath.Join(directory, "runtime", "active.yaml"),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := manager.Close(closeContext); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()
	groupService, err := proxygroup.NewService(database, manager)
	if err != nil {
		t.Fatal(err)
	}
	listenerService, err := listenerservice.NewService(database, box, manager)
	if err != nil {
		t.Fatal(err)
	}
	group, err := groupService.Create(ctx, proxygroup.CreateRequest{
		Name:     "direct-test",
		Strategy: "manual",
		SourceSpec: proxygroup.SourceSpec{
			IncludeDirect: true,
		},
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	port := reservePort(t)
	createdListener, err := listenerService.Create(ctx, listenerservice.CreateRequest{
		Name:         "direct-mixed",
		Kind:         "mixed",
		BindAddress:  "127.0.0.1",
		Port:         port,
		ProxyGroupID: group.ID,
	})
	if err != nil {
		t.Fatalf("create listener: %v", err)
	}
	if createdListener.Port != port {
		t.Fatalf("listener port = %d, want %d", createdListener.Port, port)
	}
	status := manager.Status()
	if !status.Available || !status.Running || status.ListenerCount != 1 {
		t.Fatalf("unexpected manager status: %+v", status)
	}

	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/plain")
		_, _ = writer.Write([]byte("proxied-through-mihomo"))
	}))
	defer origin.Close()
	proxyURL, err := url.Parse("http://127.0.0.1:" + portString(port))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
	}
	response, err := client.Get(origin.URL)
	if err != nil {
		t.Fatalf("request through Mihomo listener: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "proxied-through-mihomo" {
		t.Fatalf("response body = %q", body)
	}
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func portString(port int) string {
	return fmt.Sprintf("%d", port)
}
