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
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/HengXin666/HX-ProxyGroup/internal/auth"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
	terminalservice "github.com/HengXin666/HX-ProxyGroup/internal/terminal"
)

func TestTerminalWebSocketRejectsCrossOriginAndTracksSessionRevocation(t *testing.T) {
	root := t.TempDir()
	database, err := store.Open(context.Background(), filepath.Join(root, "terminal-api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tokenPath := filepath.Join(root, "admin-setup-token")
	authService, err := auth.NewService(database, tokenPath, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := authService.EnsureSetupToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	setupToken, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := authService.Setup(context.Background(), strings.TrimSpace(string(setupToken)), "admin", "correct horse battery"); err != nil {
		t.Fatal(err)
	}
	authSession, err := authService.Login(context.Background(), "127.0.0.1", "admin", "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	terminalService, err := terminalservice.NewService(terminalservice.Config{Enabled: true, Shell: "/bin/sh"}, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer terminalService.Shutdown()
	server, err := NewServer(&stubBundleService{}, logger, WithAuth(authService), WithTerminal(terminalService))
	if err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()
	websocketURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/api/v1/terminal/ws"

	headers := http.Header{}
	headers.Set("Cookie", sessionCookieName+"="+authSession.Token)
	headers.Set("Origin", "https://attacker.invalid")
	if connection, response, err := websocket.Dial(context.Background(), websocketURL, &websocket.DialOptions{HTTPHeader: headers}); err == nil {
		connection.CloseNow()
		t.Fatal("cross-origin terminal WebSocket must be rejected")
	} else if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin response = %#v, err = %v", response, err)
	}

	headers.Set("Origin", testServer.URL)
	previousInterval := terminalAuthRevalidateInterval
	terminalAuthRevalidateInterval = 20 * time.Millisecond
	defer func() { terminalAuthRevalidateInterval = previousInterval }()
	connection, _, err := websocket.Dial(context.Background(), websocketURL, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		t.Fatalf("same-origin terminal dial: %v", err)
	}
	defer connection.CloseNow()

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelRead()
	modeReceived := false
	inputSent := false
	for !modeReceived {
		kind, payload, err := connection.Read(readCtx)
		if err != nil {
			t.Fatalf("read terminal mode: %v", err)
		}
		if kind != websocket.MessageText {
			continue
		}
		var mode terminalModeMessage
		if json.Unmarshal(payload, &mode) == nil && mode.Type == "mode" {
			if mode.Echo && mode.Canonical {
				modeReceived = true
			} else if !inputSent {
				input, _ := json.Marshal(terminalMessage{Type: "input", Data: "x"})
				if err := connection.Write(readCtx, websocket.MessageText, input); err != nil {
					t.Fatalf("write terminal input: %v", err)
				}
				inputSent = true
			}
		}
	}

	deadline := time.Now().Add(time.Second)
	for terminalService.Status().ActiveSessions != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if terminalService.Status().ActiveSessions != 1 {
		t.Fatal("terminal session did not become active")
	}

	if err := authService.LogoutAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	closedCtx, cancelClosed := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelClosed()
	for {
		if _, _, err := connection.Read(closedCtx); err != nil {
			break
		}
	}
	deadline = time.Now().Add(time.Second)
	for terminalService.Status().ActiveSessions != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if terminalService.Status().ActiveSessions != 0 {
		t.Fatal("revoked administrator session left terminal running")
	}
}
