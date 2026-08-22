package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/HengXin666/HX-ProxyGroup/internal/terminal"
)

type TerminalService interface {
	Enabled() bool
	Status() terminal.Status
	Open(ctx context.Context, actor, remote string) (terminal.Session, error)
	// ReportCwd records the shell directory reported by the frontend so a
	// reconnect or a later login resumes in the previous directory.
	ReportCwd(ctx context.Context, actor, cwd string)
	// Execute runs one non-interactive command through the privileged helper
	// and returns its captured output (20260823 API remote-management path).
	Execute(ctx context.Context, command string, timeout time.Duration) (terminal.ExecResult, error)
	// File operations share the privilege domain of the shell: they are served
	// by the root PTY helper when one is configured, so the file manager can
	// browse directories (e.g. /home, /root) the sandboxed control plane
	// cannot read.
	ListFiles(ctx context.Context, path string) ([]terminal.FileEntry, error)
	StatFile(ctx context.Context, path string) (terminal.FileEntry, error)
	DownloadFile(ctx context.Context, path string, writer io.Writer) (int64, error)
	UploadFile(ctx context.Context, dir, name string, reader io.Reader, size int64) error
	Mkdir(ctx context.Context, path string) error
	RemoveFile(ctx context.Context, path string) error
}

func WithTerminal(service TerminalService) Option {
	return func(server *Server) error {
		if service == nil {
			return errors.New("terminal service is required")
		}
		server.terminal = service
		return nil
	}
}

func (s *Server) handleTerminalStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet)
		return
	}
	status := struct {
		terminal.Status
		TwoFactorConfigured bool `json:"two_factor_configured"`
		TwoFactorEnabled    bool `json:"two_factor_enabled"`
		TwoFactorVerified   bool `json:"two_factor_verified"`
		TwoFactorTTLSeconds int  `json:"two_factor_verification_ttl_seconds"`
	}{Status: s.terminal.Status()}
	if s.auth != nil {
		twoFactor, err := s.auth.TwoFactorStatus(request.Context(), sessionToken(request))
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		status.TwoFactorConfigured = twoFactor.Configured
		status.TwoFactorEnabled = twoFactor.Enabled
		status.TwoFactorVerified = twoFactor.Verified
		status.TwoFactorTTLSeconds = twoFactor.VerificationTTLSeconds
	}
	writeJSON(writer, http.StatusOK, status)
}

// terminalMessage is the client -> server control protocol. Server -> client
// traffic is raw binary PTY output rendered by xterm.js, plus lightweight JSON
// control frames (mode / pong).
type terminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cwd  string `json:"cwd,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type terminalModeMessage struct {
	Type      string `json:"type"`
	Echo      bool   `json:"echo"`
	Canonical bool   `json:"canonical"`
}

var (
	terminalAuthRevalidateInterval = 30 * time.Second
	// terminalHeartbeatInterval / terminalHeartbeatTimeout drive the server
	// heartbeat: the server pings and requires the browser's automatic pong
	// within the timeout. On lossy networks a silently dead peer is reaped in
	// about interval+timeout instead of leaking its session slot indefinitely.
	terminalHeartbeatInterval = 15 * time.Second
	terminalHeartbeatTimeout  = 15 * time.Second
)

// handleTerminalSocket bridges one WebSocket to one PTY session. Unlike the
// rest of the API, the terminal requires a fully configured and
// authenticated administrator unconditionally — there is no pre-setup
// bootstrap window for shell access.
func (s *Server) handleTerminalSocket(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet)
		return
	}
	if s.auth == nil {
		s.writeAPIError(writer, request, http.StatusForbidden, "terminal_requires_auth", "terminal requires administrator authentication")
		return
	}
	token := sessionToken(request)
	session, err := s.auth.Authenticate(request.Context(), token)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	if !s.terminal.Enabled() {
		s.writeAPIError(writer, request, http.StatusForbidden, "terminal_disabled", "terminal is disabled; set HX_PROXYGROUP_TERMINAL=1 or remove HX_PROXYGROUP_TERMINAL=0")
		return
	}
	twoFactor, err := s.auth.TwoFactorStatus(request.Context(), token)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	if !twoFactor.Enabled {
		s.writeAPIError(writer, request, http.StatusForbidden, "terminal_requires_two_factor", "configure and enable two-factor authentication before opening the terminal")
		return
	}
	if !twoFactor.Verified {
		s.writeAPIError(writer, request, http.StatusForbidden, "terminal_requires_two_factor_verification", "verify a current two-factor authentication code before opening the terminal")
		return
	}
	connection, err := websocket.Accept(writer, request, nil)
	if err != nil {
		return
	}
	defer connection.Close(websocket.StatusInternalError, "terminal closed")
	connection.SetReadLimit(64 << 10)

	socketCtx, cancel := context.WithCancel(request.Context())
	defer cancel()
	shell, err := s.terminal.Open(socketCtx, session.Username, clientAddress(request))
	if err != nil {
		message := "terminal unavailable"
		if errors.Is(err, terminal.ErrSessionLimit) {
			message = "too many concurrent terminal sessions"
		}
		_ = connection.Close(websocket.StatusPolicyViolation, message)
		return
	}
	defer shell.Close("connection closed")

	// PTY output -> WebSocket binary frames.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buffer := make([]byte, 16<<10)
		var lastMode terminal.Mode
		modeSent := false
		sendMode := func(force bool) bool {
			mode, modeErr := shell.TerminalMode()
			if modeErr != nil {
				// Unknown mode must disable prediction rather than risk echoing
				// a password or full-screen application input locally.
				mode = terminal.Mode{}
			}
			if !force && modeSent && mode == lastMode {
				return true
			}
			payload, _ := json.Marshal(terminalModeMessage{Type: "mode", Echo: mode.Echo, Canonical: mode.Canonical})
			writeCtx, writeCancel := context.WithTimeout(socketCtx, 15*time.Second)
			writeErr := connection.Write(writeCtx, websocket.MessageText, payload)
			writeCancel()
			if writeErr != nil {
				return false
			}
			lastMode = mode
			modeSent = true
			return true
		}
		if !sendMode(true) {
			return
		}
		for {
			count, readErr := shell.Read(buffer)
			if count > 0 {
				if !sendMode(false) {
					return
				}
				writeCtx, writeCancel := context.WithTimeout(socketCtx, 15*time.Second)
				writeErr := connection.Write(writeCtx, websocket.MessageBinary, buffer[:count])
				writeCancel()
				if writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Heartbeat the peer so a silently dead connection (packet loss, NAT drop)
	// is detected promptly: the session slot is freed and a later reconnect is
	// not blocked by the session cap. Browsers auto-respond to pings; when the
	// pong is not received within the timeout the peer is treated as gone.
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(terminalHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-socketCtx.Done():
				return
			case <-ticker.C:
				pingCtx, pingCancel := context.WithTimeout(socketCtx, terminalHeartbeatTimeout)
				pingErr := connection.Ping(pingCtx)
				pingCancel()
				if pingErr != nil {
					shell.Close("connection heartbeat lost")
					cancel()
					_ = connection.Close(websocket.StatusGoingAway, "connection heartbeat lost")
					return
				}
			}
		}
	}()

	// Revalidate the database-backed administrator session while the socket is
	// open. Logout-all, username changes, password changes, and expiry revoke
	// an existing terminal instead of only blocking the next connection.
	authDone := make(chan struct{})
	go func() {
		defer close(authDone)
		ticker := time.NewTicker(terminalAuthRevalidateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-socketCtx.Done():
				return
			case <-ticker.C:
				checkCtx, checkCancel := context.WithTimeout(socketCtx, 5*time.Second)
				_, authErr := s.auth.Authenticate(checkCtx, token)
				if authErr == nil {
					twoFactor, twoFactorErr := s.auth.TwoFactorStatus(checkCtx, token)
					if twoFactorErr != nil || !twoFactor.Enabled || !twoFactor.Verified {
						authErr = errors.New("administrator two-factor verification is no longer valid")
					} else if renewErr := s.auth.RenewTwoFactorVerification(checkCtx, token); renewErr != nil {
						// Renewal fails only when the window lapsed between the
						// status check and the write; treat it like an expired
						// verification so the session is closed cleanly.
						authErr = errors.New("administrator two-factor verification is no longer valid")
					}
				}
				checkCancel()
				if authErr != nil {
					shell.Close("administrator session revoked")
					cancel()
					_ = connection.Close(websocket.StatusPolicyViolation, "administrator session expired")
					return
				}
			}
		}
	}()

	// WebSocket control frames -> PTY.
	for {
		kind, payload, readErr := connection.Read(socketCtx)
		if readErr != nil {
			break
		}
		if kind != websocket.MessageText {
			continue
		}
		var message terminalMessage
		if json.Unmarshal(payload, &message) != nil {
			continue
		}
		switch message.Type {
		case "input":
			if _, writeErr := io.WriteString(shell, message.Data); writeErr != nil {
				break
			}
		case "resize":
			_ = shell.Resize(message.Cols, message.Rows)
		case "ping":
			// Application-level keepalive from the client (browsers cannot
			// send WebSocket ping frames). Reply so the client can detect a
			// half-dead connection without waiting for TCP to give up.
			pong, _ := json.Marshal(terminalMessage{Type: "pong"})
			writeCtx, writeCancel := context.WithTimeout(socketCtx, 5*time.Second)
			_ = connection.Write(writeCtx, websocket.MessageText, pong)
			writeCancel()
		case "cwd":
			// Persist the reported shell directory for resume. The service
			// validates and throttles; a failed persist must not kill the
			// terminal.
			s.terminal.ReportCwd(socketCtx, session.Username, message.Cwd)
		}
	}
	cancel()
	shell.Close("connection closed")
	<-outputDone
	<-authDone
	<-heartbeatDone
	_ = connection.Close(websocket.StatusNormalClosure, "bye")
}
