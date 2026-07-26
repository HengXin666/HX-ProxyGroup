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
	command.Env = append(environment, "TERM=xterm-256color")
	ptyFile, err := pty.Start(command)
	if err != nil {
		return nil, nil, fmt.Errorf("start shell pty: %w", err)
	}
	return ptyFile, command, nil
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
