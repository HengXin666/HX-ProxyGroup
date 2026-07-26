package api

import (
	"bytes"
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

	"github.com/HengXin666/HX-ProxyGroup/internal/auth"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func newAuthTestServer(t *testing.T) (*httptest.Server, *auth.Service, string) {
	t.Helper()
	root := t.TempDir()
	database, err := store.Open(context.Background(), filepath.Join(root, "api-auth.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	tokenPath := filepath.Join(root, "admin-setup-token")
	authService, err := auth.NewService(database, tokenPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new auth service: %v", err)
	}
	if err := authService.EnsureSetupToken(context.Background()); err != nil {
		t.Fatalf("ensure setup token: %v", err)
	}
	server, err := NewServer(&stubBundleService{}, slog.New(slog.NewTextHandler(io.Discard, nil)), WithAuth(authService))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	testServer := httptest.NewServer(server.Handler())
	t.Cleanup(testServer.Close)
	return testServer, authService, tokenPath
}

func postJSON(t *testing.T, client *http.Client, url string, body map[string]string, headers map[string]string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestAuthBootstrapAndEnforcement(t *testing.T) {
	testServer, _, tokenPath := newAuthTestServer(t)
	client := testServer.Client()

	// Before setup, the loopback-bound API stays reachable.
	response, err := client.Get(testServer.URL + "/api/v1/backups")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pre-setup API must remain open, got %d", response.StatusCode)
	}

	// Status reports not configured.
	response, err = client.Get(testServer.URL + "/api/v1/auth/status")
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]any
	_ = json.NewDecoder(response.Body).Decode(&status)
	response.Body.Close()
	if status["configured"] != false {
		t.Fatalf("expected configured=false, got %v", status)
	}

	// Complete setup with the on-disk token.
	rawToken, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, client, testServer.URL+"/api/v1/auth/setup", map[string]string{
		"setup_token": strings.TrimSpace(string(rawToken)),
		"username":    "admin",
		"password":    "correct horse battery",
	}, nil)
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("setup failed with %d", response.StatusCode)
	}

	// After setup, unauthenticated requests are rejected.
	response, err = client.Get(testServer.URL + "/api/v1/backups")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 after setup, got %d", response.StatusCode)
	}

	// Login yields a session cookie and CSRF token.
	response = postJSON(t, client, testServer.URL+"/api/v1/auth/login", map[string]string{
		"username": "admin",
		"password": "correct horse battery",
	}, nil)
	var login struct {
		CSRFToken string `json:"csrf_token"`
	}
	_ = json.NewDecoder(response.Body).Decode(&login)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || login.CSRFToken == "" {
		t.Fatalf("login failed: %d", response.StatusCode)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly {
		t.Fatal("login must set an HttpOnly session cookie")
	}

	authedRequest, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/v1/backups", nil)
	authedRequest.AddCookie(sessionCookie)
	response, err = client.Do(authedRequest)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated GET must succeed, got %d", response.StatusCode)
	}

	// Mutating request without CSRF header is rejected.
	response = postJSON(t, client, testServer.URL+"/api/v1/backups", map[string]string{}, map[string]string{
		"Cookie": sessionCookie.String(),
	})
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF token, got %d", response.StatusCode)
	}

	// Mutating request with CSRF header passes the auth layer.
	response = postJSON(t, client, testServer.URL+"/api/v1/backups", map[string]string{}, map[string]string{
		"Cookie":       sessionCookie.String(),
		"X-CSRF-Token": login.CSRFToken,
	})
	response.Body.Close()
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusUnauthorized {
		t.Fatalf("CSRF-carrying request must pass auth, got %d", response.StatusCode)
	}

	// Health endpoints stay open for systemd probes.
	response, err = client.Get(testServer.URL + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health must stay unauthenticated, got %d", response.StatusCode)
	}
}
