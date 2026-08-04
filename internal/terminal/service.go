// Package terminal implements the v2 in-browser terminal: PTY sessions on
// the local server bridged to the web UI over WebSocket. It never implements
// a terminal protocol itself — the PTY comes from creack/pty and rendering
// from xterm.js in the frontend.
//
// Safety model (docs/V1_CORE.md 9.3):
//   - enabled by default, with an emergency environment kill switch;
//   - administrator authentication is required unconditionally;
//   - no idle disconnect, with an absolute lifetime cap per session;
//   - bounded concurrent sessions;
//   - every session start and end is audit-logged with cause and duration.
package terminal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var (
	ErrDisabled     = errors.New("terminal is disabled")
	ErrSessionLimit = errors.New("too many concurrent terminal sessions")
)

const (
	defaultMaxLifetime   = 2 * time.Hour
	defaultMaxSessions   = 2
	defaultShellSizeCols = 120
	defaultShellSizeRows = 32
)

type Config struct {
	// Enabled gates the whole feature. The control-plane default is enabled;
	// callers may explicitly disable it for emergency lockdown.
	Enabled bool
	// Shell overrides the login shell; empty uses $SHELL then /bin/bash.
	Shell string
	// IdleTimeout closes a session with no input/output activity. Zero disables
	// idle disconnection, which is the production default for weak networks.
	IdleTimeout time.Duration
	// MaxLifetime is the absolute per-session cap.
	MaxLifetime time.Duration
	// MaxSessions bounds concurrently open sessions.
	MaxSessions int
	// PrivilegedSocket is a local Unix socket served by the optional root PTY
	// helper. When set, terminal sessions are created by that helper so the
	// administrator can use su/sudo without running the control plane as root.
	PrivilegedSocket string
	// UpdaterPath enables the fixed-command privileged update request. The
	// helper validates this root-owned executable before scheduling it.
	UpdaterPath string
}

// Session is the PTY-like surface shared by local and helper-backed shells.
// The control plane owns lifecycle and authentication; implementations only
// provide terminal I/O and window management.
type Session interface {
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	TerminalMode() (Mode, error)
	Resize(columns, rows int) error
	Close(cause string)
}

type Service struct {
	config Config
	logger *slog.Logger

	mutex    sync.Mutex
	sessions map[string]Session
}

func NewService(config Config, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if config.IdleTimeout < 0 {
		return nil, errors.New("terminal idle timeout cannot be negative")
	}
	if config.MaxLifetime <= 0 {
		config.MaxLifetime = defaultMaxLifetime
	}
	if config.MaxSessions <= 0 {
		config.MaxSessions = defaultMaxSessions
	}
	return &Service{
		config:   config,
		logger:   logger,
		sessions: make(map[string]Session),
	}, nil
}

func (s *Service) Enabled() bool { return s.config.Enabled }

func (s *Service) TriggerUpdate(ctx context.Context) error {
	if strings.TrimSpace(s.config.PrivilegedSocket) == "" || strings.TrimSpace(s.config.UpdaterPath) == "" {
		return errors.New("automatic update is unavailable outside a production systemd installation")
	}
	return requestRemoteUpdate(ctx, s.config.PrivilegedSocket)
}

// Status is the API view of the terminal feature.
type Status struct {
	Enabled        bool `json:"enabled"`
	ActiveSessions int  `json:"active_sessions"`
	MaxSessions    int  `json:"max_sessions"`
	IdleTimeoutSec int  `json:"idle_timeout_seconds"`
	MaxLifetimeSec int  `json:"max_lifetime_seconds"`
	Privileged     bool `json:"privileged"`
}

func (s *Service) Status() Status {
	s.mutex.Lock()
	active := len(s.sessions)
	s.mutex.Unlock()
	return Status{
		Enabled:        s.config.Enabled,
		ActiveSessions: active,
		MaxSessions:    s.config.MaxSessions,
		IdleTimeoutSec: int(s.config.IdleTimeout / time.Second),
		MaxLifetimeSec: int(s.config.MaxLifetime / time.Second),
		Privileged:     strings.TrimSpace(s.config.PrivilegedSocket) != "",
	}
}

// Open starts a new shell session. ctx cancellation (the WebSocket closing)
// terminates the session. actor and remote are audit metadata only.
func (s *Service) Open(ctx context.Context, actor, remote string) (Session, error) {
	if !s.config.Enabled {
		return nil, ErrDisabled
	}
	s.mutex.Lock()
	if len(s.sessions) >= s.config.MaxSessions {
		s.mutex.Unlock()
		return nil, ErrSessionLimit
	}
	// Reserve the slot before the fork so parallel opens cannot exceed the cap.
	id := newSessionID()
	s.sessions[id] = nil
	s.mutex.Unlock()

	release := func() {
		s.mutex.Lock()
		delete(s.sessions, id)
		s.mutex.Unlock()
	}
	now := time.Now()
	var base Session
	var shellName string
	var err error
	if socketPath := strings.TrimSpace(s.config.PrivilegedSocket); socketPath != "" {
		base, err = openRemoteSession(ctx, socketPath)
		shellName = "root PTY helper"
	} else {
		var ptyFile *os.File
		var command *exec.Cmd
		ptyFile, command, err = startShell(s.config.Shell, os.Environ())
		if err == nil {
			base = newPTYSession(ptyFile, command)
			shellName = command.Path
		}
	}
	if err != nil {
		release()
		return nil, err
	}
	session := &trackedSession{
		Session:    base,
		id:         id,
		startedAt:  now,
		lastActive: now,
	}
	session.onClose = func(closed *trackedSession, cause string) {
		release()
		s.logger.Info("terminal session closed",
			"audit", "terminal",
			"session_id", closed.id,
			"actor", actor,
			"remote", remote,
			"cause", cause,
			"duration_ms", time.Since(closed.startedAt).Milliseconds(),
		)
	}
	_ = session.Resize(defaultShellSizeCols, defaultShellSizeRows)

	s.mutex.Lock()
	s.sessions[id] = session
	s.mutex.Unlock()

	s.logger.Info("terminal session opened",
		"audit", "terminal",
		"session_id", id,
		"actor", actor,
		"remote", remote,
		"shell", shellName,
		"privileged", strings.TrimSpace(s.config.PrivilegedSocket) != "",
	)
	go session.watch(ctx, s.config.IdleTimeout, s.config.MaxLifetime)
	return session, nil
}

// Shutdown closes every open session (service stop).
func (s *Service) Shutdown() {
	s.mutex.Lock()
	open := make([]Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session != nil {
			open = append(open, session)
		}
	}
	s.mutex.Unlock()
	for _, session := range open {
		session.Close("service shutdown")
	}
}

func newSessionID() string {
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return fmt.Sprintf("term-%d", time.Now().UnixNano())
	}
	return "term-" + hex.EncodeToString(buffer[:])
}
