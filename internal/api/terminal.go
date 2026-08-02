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
// traffic is raw binary PTY output rendered by xterm.js.
type terminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`
}

type terminalModeMessage struct {
	Type      string `json:"type"`
	Echo      bool   `json:"echo"`
	Canonical bool   `json:"canonical"`
}

var terminalAuthRevalidateInterval = 30 * time.Second

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
		sendMode := func() bool {
			mode, modeErr := shell.TerminalMode()
			if modeErr != nil {
				// Unknown mode must disable prediction rather than risk echoing
				// a password or full-screen application input locally.
				mode = terminal.Mode{}
			}
			payload, _ := json.Marshal(terminalModeMessage{Type: "mode", Echo: mode.Echo, Canonical: mode.Canonical})
			writeCtx, writeCancel := context.WithTimeout(socketCtx, 15*time.Second)
			writeErr := connection.Write(writeCtx, websocket.MessageText, payload)
			writeCancel()
			if writeErr != nil {
				return false
			}
			return true
		}
		if !sendMode() {
			return
		}
		for {
			count, readErr := shell.Read(buffer)
			if count > 0 {
				if !sendMode() {
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
		}
	}
	cancel()
	<-outputDone
	<-authDone
	_ = connection.Close(websocket.StatusNormalClosure, "bye")
}
