package terminal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestDisabledServiceRefusesSessions(t *testing.T) {
	service, err := NewService(Config{Enabled: false}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if service.Enabled() {
		t.Fatal("terminal must be disabled by default")
	}
	if _, err := service.Open(context.Background(), "admin", "127.0.0.1"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestSessionEchoAndClose(t *testing.T) {
	service, err := NewService(Config{Enabled: true, Shell: "/bin/sh"}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := service.Open(ctx, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	defer session.Close("test done")

	if _, err := session.Write([]byte("echo terminal-roundtrip-ok\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	var output strings.Builder
	buffer := make([]byte, 4096)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		count, readErr := session.Read(buffer)
		if count > 0 {
			output.WriteString(string(buffer[:count]))
			if strings.Contains(output.String(), "terminal-roundtrip-ok") {
				break
			}
		}
		if readErr != nil {
			break
		}
	}
	if !strings.Contains(output.String(), "terminal-roundtrip-ok") {
		t.Fatalf("shell output missing echo, got: %q", output.String())
	}

	if status := service.Status(); status.ActiveSessions != 1 {
		t.Fatalf("expected 1 active session, got %d", status.ActiveSessions)
	}
	session.Close("test done")
	// Close is synchronous for bookkeeping; the audit hook released the slot.
	if status := service.Status(); status.ActiveSessions != 0 {
		t.Fatalf("expected 0 active sessions after close, got %d", status.ActiveSessions)
	}
	// Idempotent close.
	session.Close("again")
}

func TestSessionLimit(t *testing.T) {
	service, err := NewService(Config{Enabled: true, Shell: "/bin/sh", MaxSessions: 1}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := service.Open(ctx, "admin", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close("test done")
	if _, err := service.Open(ctx, "admin", "127.0.0.1"); !errors.Is(err, ErrSessionLimit) {
		t.Fatalf("expected ErrSessionLimit, got %v", err)
	}
	first.Close("test done")
	second, err := service.Open(ctx, "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("slot must be released after close: %v", err)
	}
	second.Close("test done")
}

func TestResizeValidation(t *testing.T) {
	service, err := NewService(Config{Enabled: true, Shell: "/bin/sh"}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Open(context.Background(), "admin", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close("test done")
	if err := session.Resize(0, 10); err == nil {
		t.Fatal("zero columns must be rejected")
	}
	if err := session.Resize(5000, 10); err == nil {
		t.Fatal("oversized terminal must be rejected")
	}
	if err := session.Resize(80, 24); err != nil {
		t.Fatalf("valid resize failed: %v", err)
	}
}
