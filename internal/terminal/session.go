package terminal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// ptySession is one local PTY-backed shell. Lifecycle ownership lives in the
// trackedSession wrapper so local and helper-backed sessions behave alike.
type ptySession struct {
	pty     *os.File
	command *exec.Cmd

	mutex  sync.Mutex
	closed bool
}

// Mode describes the kernel-managed PTY input mode. Clients may only predict
// local echo while both flags are true; password prompts and full-screen
// programs disable at least Echo or Canonical.
type Mode struct {
	Echo      bool
	Canonical bool
}

// trackedSession adds lifecycle bookkeeping shared by all terminal backends.
// Its methods are intentionally small so lifecycle accounting is identical
// when a remote helper is selected.
type trackedSession struct {
	Session
	id         string
	startedAt  time.Time
	lastActive time.Time

	mutex   sync.Mutex
	closed  bool
	onClose func(*trackedSession, string)
}

func (s *trackedSession) Read(buffer []byte) (int, error) {
	count, err := s.Session.Read(buffer)
	if count > 0 {
		s.touch()
	}
	return count, err
}

func (s *trackedSession) Write(data []byte) (int, error) {
	s.touch()
	return s.Session.Write(data)
}

func (s *trackedSession) Close(cause string) {
	s.mutex.Lock()
	if s.closed {
		s.mutex.Unlock()
		return
	}
	s.closed = true
	onClose := s.onClose
	s.mutex.Unlock()

	s.Session.Close(cause)
	if onClose != nil {
		onClose(s, cause)
	}
}

// startShell launches the login shell inside a new PTY.
func startShell(shell string, environment []string, persistHistory bool) (*os.File, *exec.Cmd, error) {
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
	command.Env = safeShellEnvironment(environment, shell, persistHistory)
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

func newPTYSession(ptyFile *os.File, command *exec.Cmd) *ptySession {
	return &ptySession{pty: ptyFile, command: command}
}

// safeShellEnvironment prevents application credentials or deployment
// controls from leaking into an interactive shell. It receives only
// conventional locale and identity values, regardless of the PTY backend.
func safeShellEnvironment(environment []string, shell string, persistHistory bool) []string {
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
	if persistHistory {
		if strings.Contains(shell, "zsh") {
			if home := environmentValue(environment, "HOME"); home != "" {
				values["HISTFILE"] = filepath.Join(home, ".zsh_history")
				values["SAVEHIST"] = "10000"
				values["HISTSIZE"] = "2000"
			}
		}
		// Other shells (bash, sh) keep their built-in history defaults: bash
		// writes ~/.bash_history whenever HOME is set, so no HISTFILE override
		// is needed and the user's own rc settings still apply.
	} else {
		values["HISTFILE"] = "/dev/null"
	}
	result := make([]string, 0, len(values))
	for _, name := range []string{"HOME", "USER", "LOGNAME", "PATH", "LANG", "TZ", "TERM", "COLORTERM", "SHELL", "HISTFILE", "SAVEHIST", "HISTSIZE"} {
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
func (s *ptySession) Read(buffer []byte) (int, error) {
	return s.pty.Read(buffer)
}

// Write feeds user keystrokes into the shell.
func (s *ptySession) Write(data []byte) (int, error) {
	return s.pty.Write(data)
}

// TerminalMode reads the current line discipline flags directly from the PTY.
func (s *ptySession) TerminalMode() (Mode, error) {
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
func (s *ptySession) Resize(columns, rows int) error {
	if columns < 1 || columns > 1000 || rows < 1 || rows > 1000 {
		return errors.New("terminal size out of range")
	}
	return pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(columns), Rows: uint16(rows)})
}

// Close terminates the shell and the PTY. It is idempotent; the first cause
// wins and is reported to the audit hook. The shell is asked to exit
// gracefully first so bash/zsh can persist their history file, then killed
// after a short grace period so teardown stays bounded.  Interactive bash
// ignores SIGTERM, so SIGHUP is used instead: bash writes history on
// SIGHUP then exits cleanly.
func (s *ptySession) Close(_ string) {
	const waitGrace = 1200 * time.Millisecond
	s.mutex.Lock()
	if s.closed {
		s.mutex.Unlock()
		return
	}
	s.closed = true
	s.mutex.Unlock()

	_ = s.pty.Close()
	if s.command.Process == nil {
		return
	}
	_ = s.command.Process.Signal(unix.SIGHUP)
	done := make(chan struct{})
	go func() { _ = s.command.Wait(); close(done) }()
	select {
	case <-done:
		// Normal exit — shell saved history.
	case <-time.After(waitGrace):
		_ = s.command.Process.Kill()
		<-done
	}
}

func (s *trackedSession) touch() {
	s.mutex.Lock()
	s.lastActive = time.Now()
	s.mutex.Unlock()
}

func (s *trackedSession) idleSince() time.Time {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.lastActive
}

// watch enforces the optional idle timeout and the absolute lifetime cap with a
// single goroutine per session that exits when the session closes.
func (s *trackedSession) watch(ctx context.Context, idleTimeout, maxLifetime time.Duration) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	deadline := s.startedAt.Add(maxLifetime)
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
			if idleTimeout > 0 && now.Sub(lastActive) >= idleTimeout {
				s.Close("idle timeout")
				return
			}
			if maxLifetime > 0 && now.After(deadline) {
				s.Close("maximum session lifetime reached")
				return
			}
		}
	}
}
