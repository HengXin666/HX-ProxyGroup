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

func TestDefaultLifecycleDisablesIdleTimeoutAndKeepsAbsoluteCap(t *testing.T) {
	service, err := NewService(Config{}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	status := service.Status()
	if status.IdleTimeoutSec != 0 {
		t.Fatalf("idle timeout = %d, want disabled", status.IdleTimeoutSec)
	}
	if status.MaxLifetimeSec != int((2*time.Hour)/time.Second) {
		t.Fatalf("max lifetime = %d", status.MaxLifetimeSec)
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

func TestTerminalModeTracksEchoAndCanonicalState(t *testing.T) {
	service, err := NewService(Config{Enabled: true, Shell: "/bin/sh"}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Open(context.Background(), "admin", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close("test done")

	mode, err := session.TerminalMode()
	if err != nil {
		t.Fatal(err)
	}
	if !mode.Echo || !mode.Canonical {
		t.Fatalf("initial terminal mode = %+v, want echo + canonical", mode)
	}

	if _, err := session.Write([]byte("stty -echo\n")); err != nil {
		t.Fatal(err)
	}
	mode = waitTerminalMode(t, session, func(mode Mode) bool { return !mode.Echo })
	if mode.Echo {
		t.Fatalf("disabled terminal mode = %+v, want echo disabled", mode)
	}
}

func waitTerminalMode(t *testing.T, session Session, matches func(Mode) bool) Mode {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last Mode
	for time.Now().Before(deadline) {
		mode, err := session.TerminalMode()
		if err != nil {
			t.Fatal(err)
		}
		last = mode
		if matches(mode) {
			return mode
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal mode did not reach expected state; last = %+v", last)
	return Mode{}
}

func TestSafeShellEnvironmentDropsApplicationSecrets(t *testing.T) {
	environment := safeShellEnvironment([]string{
		"HOME=/srv/hx",
		"USER=hx-proxygroup",
		"PATH=/usr/bin:/bin",
		"LANG=C.UTF-8",
		"LC_TIME=C",
		"HX_PROXYGROUP_MASTER_KEY=must-not-leak",
		"DATABASE_URL=sqlite-secret",
		"AUTHORIZATION=Bearer-secret",
	}, "/bin/sh")
	joined := strings.Join(environment, "\n")
	for _, secret := range []string{"must-not-leak", "sqlite-secret", "Bearer-secret", "HX_PROXYGROUP_MASTER_KEY", "DATABASE_URL", "AUTHORIZATION"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("safe environment leaks %q: %s", secret, joined)
		}
	}
	for _, expected := range []string{"HOME=/srv/hx", "PATH=/usr/bin:/bin", "LC_TIME=C", "TERM=xterm-256color", "HISTFILE=/dev/null"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("safe environment missing %q: %s", expected, joined)
		}
	}
}
