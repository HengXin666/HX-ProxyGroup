package terminal

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrivilegedHelperRoundTrip(t *testing.T) {
	root := t.TempDir()
	socketPath := filepath.Join(root, "terminal.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	helperErrors := make(chan error, 1)
	go func() {
		helperErrors <- RunHelper(ctx, HelperConfig{
			SocketPath:  socketPath,
			Shell:       "/bin/sh",
			MaxSessions: 1,
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	waitForSocket(t, socketPath)

	service, err := NewService(Config{
		Enabled:          true,
		PrivilegedSocket: socketPath,
		MaxSessions:      1,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Open(context.Background(), "admin", "127.0.0.1")
	if err != nil {
		t.Fatalf("open helper session: %v", err)
	}
	if status := service.Status(); !status.Privileged || status.ActiveSessions != 1 {
		t.Fatalf("unexpected privileged status: %+v", status)
	}
	defer session.Close("test done")

	if _, err := session.Write([]byte("printf 'helper-roundtrip-ok\\n'\n")); err != nil {
		t.Fatalf("write helper session: %v", err)
	}
	var output strings.Builder
	buffer := make([]byte, 1024)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		count, readErr := session.Read(buffer)
		if count > 0 {
			output.Write(buffer[:count])
			if strings.Contains(output.String(), "helper-roundtrip-ok") {
				break
			}
		}
		if readErr != nil {
			t.Fatalf("read helper session: %v", readErr)
		}
	}
	if !strings.Contains(output.String(), "helper-roundtrip-ok") {
		t.Fatalf("helper output missing roundtrip marker: %q", output.String())
	}

	session.Close("test done")
	if status := service.Status(); status.ActiveSessions != 0 {
		t.Fatalf("helper session slot was not released: %+v", status)
	}
	cancel()
	select {
	case err := <-helperErrors:
		if err != nil {
			t.Fatalf("stop helper: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not stop after context cancellation")
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("terminal helper socket was not created: %s", path)
}
