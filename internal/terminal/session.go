package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Session is one PTY-backed shell. It is created by the Service and closed
// either explicitly, by idle timeout, or by the absolute lifetime cap.
type Session struct {
	ID        string
	StartedAt time.Time

	pty     *os.File
	command *exec.Cmd

	mutex      sync.Mutex
	lastActive time.Time
	closed     bool
	closeCause string
	onClose    func(*Session, string)
}

// Mode describes the kernel-managed PTY input mode. Clients may only predict
// local echo while both flags are true; password prompts and full-screen
// programs disable at least Echo or Canonical.
type Mode struct {
	Echo      bool
	Canonical bool
}

// startShell launches the login shell inside a new PTY.
func startShell(shell string, environment []string) (*os.File, *exec.Cmd, error) {
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		shell = "/bin/bash"
	}
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}
	command := exec.Command(shell)
	command.Env = safeShellEnvironment(environment, shell)
	if home := environmentValue(environment, "HOME"); home != "" {
		if info, err := os.Stat(home); err == nil && info.IsDir() {
			command.Dir = home
		}
	}
	ptyFile, err := pty.Start(command)
	if err != nil {
		return nil, nil, fmt.Errorf("start shell pty: %w", err)
	}
	return ptyFile, command, nil
}

// safeShellEnvironment prevents application credentials or deployment
// controls from leaking into an interactive shell. The terminal runs as the
// service account and receives only conventional locale and identity values.
func safeShellEnvironment(environment []string, shell string) []string {
	allowed := map[string]struct{}{
		"HOME": {}, "USER": {}, "LOGNAME": {}, "PATH": {}, "LANG": {}, "TZ": {},
	}
	values := make(map[string]string)
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if _, ok := allowed[name]; ok || strings.HasPrefix(name, "LC_") {
			values[name] = value
		}
	}
	values["TERM"] = "xterm-256color"
	values["COLORTERM"] = "truecolor"
	values["SHELL"] = shell
	values["HISTFILE"] = "/dev/null"
	result := make([]string, 0, len(values))
	for _, name := range []string{"HOME", "USER", "LOGNAME", "PATH", "LANG", "TZ", "TERM", "COLORTERM", "SHELL", "HISTFILE"} {
		if value, ok := values[name]; ok {
			result = append(result, name+"="+value)
			delete(values, name)
		}
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

func environmentValue(environment []string, target string) string {
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found && name == target {
			return value
		}
	}
	return ""
}

// Read streams PTY output; it blocks like a file read and returns an error
// after Close.
func (s *Session) Read(buffer []byte) (int, error) {
	count, err := s.pty.Read(buffer)
	if count > 0 {
		s.touch()
	}
	return count, err
}

// Write feeds user keystrokes into the shell.
func (s *Session) Write(data []byte) (int, error) {
	s.touch()
	return s.pty.Write(data)
}

// TerminalMode reads the current line discipline flags directly from the PTY.
func (s *Session) TerminalMode() (Mode, error) {
	settings, err := unix.IoctlGetTermios(int(s.pty.Fd()), unix.TCGETS)
	if err != nil {
		return Mode{}, fmt.Errorf("read terminal mode: %w", err)
	}
	return Mode{
		Echo:      settings.Lflag&unix.ECHO != 0,
		Canonical: settings.Lflag&unix.ICANON != 0,
	}, nil
}

// Resize adjusts the PTY window.
func (s *Session) Resize(columns, rows int) error {
	if columns < 1 || columns > 1000 || rows < 1 || rows > 1000 {
		return errors.New("terminal size out of range")
	}
	return pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(columns), Rows: uint16(rows)})
}

// Close terminates the shell and the PTY. It is idempotent; the first cause
// wins and is reported to the audit hook.
func (s *Session) Close(cause string) {
	s.mutex.Lock()
	if s.closed {
		s.mutex.Unlock()
		return
	}
	s.closed = true
	s.closeCause = cause
	onClose := s.onClose
	s.mutex.Unlock()

	_ = s.pty.Close()
	if s.command.Process != nil {
		_ = s.command.Process.Kill()
	}
	_ = s.command.Wait()
	if onClose != nil {
		onClose(s, cause)
	}
}

func (s *Session) touch() {
	s.mutex.Lock()
	s.lastActive = time.Now()
	s.mutex.Unlock()
}

func (s *Session) idleSince() time.Time {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.lastActive
}

// watch enforces the idle timeout and the absolute lifetime cap with a
// single goroutine per session that exits when the session closes.
func (s *Session) watch(ctx context.Context, idleTimeout, maxLifetime time.Duration) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	deadline := s.StartedAt.Add(maxLifetime)
	for {
		select {
		case <-ctx.Done():
			s.Close("connection closed")
			return
		case <-ticker.C:
			s.mutex.Lock()
			closed := s.closed
			lastActive := s.lastActive
			s.mutex.Unlock()
			if closed {
				return
			}
			now := time.Now()
			if now.Sub(lastActive) >= idleTimeout {
				s.Close("idle timeout")
				return
			}
			if now.After(deadline) {
				s.Close("maximum session lifetime reached")
				return
			}
		}
	}
}
