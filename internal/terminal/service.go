// Package terminal implements the v2 in-browser terminal: PTY sessions on
// the local server bridged to the web UI over WebSocket. It never implements
// a terminal protocol itself — the PTY comes from creack/pty and rendering
// from xterm.js in the frontend.
//
// Safety model (docs/V1_CORE.md 9.3):
//   - independent feature gate, disabled by default;
//   - administrator authentication is required unconditionally;
//   - idle timeout and an absolute lifetime cap per session;
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
	"sync"
	"time"
)

var (
	ErrDisabled     = errors.New("terminal is disabled")
	ErrSessionLimit = errors.New("too many concurrent terminal sessions")
)

const (
	defaultIdleTimeout   = 10 * time.Minute
	defaultMaxLifetime   = 2 * time.Hour
	defaultMaxSessions   = 2
	defaultShellSizeCols = 120
	defaultShellSizeRows = 32
)

type Config struct {
	// Enabled gates the whole feature. Off by default.
	Enabled bool
	// Shell overrides the login shell; empty uses $SHELL then /bin/bash.
	Shell string
	// IdleTimeout closes a session with no input/output activity.
	IdleTimeout time.Duration
	// MaxLifetime is the absolute per-session cap.
	MaxLifetime time.Duration
	// MaxSessions bounds concurrently open sessions.
	MaxSessions int
}

type Service struct {
	config Config
	logger *slog.Logger

	mutex    sync.Mutex
	sessions map[string]*Session
}

func NewService(config Config, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if config.IdleTimeout <= 0 {
		config.IdleTimeout = defaultIdleTimeout
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
		sessions: make(map[string]*Session),
	}, nil
}

func (s *Service) Enabled() bool { return s.config.Enabled }

// Status is the API view of the terminal feature.
type Status struct {
	Enabled        bool `json:"enabled"`
	ActiveSessions int  `json:"active_sessions"`
	MaxSessions    int  `json:"max_sessions"`
	IdleTimeoutSec int  `json:"idle_timeout_seconds"`
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
	}
}

// Open starts a new shell session. ctx cancellation (the WebSocket closing)
// terminates the session. actor and remote are audit metadata only.
func (s *Service) Open(ctx context.Context, actor, remote string) (*Session, error) {
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
	ptyFile, command, err := startShell(s.config.Shell, os.Environ())
	if err != nil {
		release()
		return nil, err
	}
	now := time.Now()
	session := &Session{
		ID:         id,
		StartedAt:  now,
		pty:        ptyFile,
		command:    command,
		lastActive: now,
	}
	session.onClose = func(closed *Session, cause string) {
		release()
		s.logger.Info("terminal session closed",
			"audit", "terminal",
			"session_id", closed.ID,
			"actor", actor,
			"remote", remote,
			"cause", cause,
			"duration_ms", time.Since(closed.StartedAt).Milliseconds(),
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
		"shell", command.Path,
	)
	go session.watch(ctx, s.config.IdleTimeout, s.config.MaxLifetime)
	return session, nil
}

// Shutdown closes every open session (service stop).
func (s *Service) Shutdown() {
	s.mutex.Lock()
	open := make([]*Session, 0, len(s.sessions))
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
