package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/auth"
)

const sessionCookieName = "hx_session"

type AuthService interface {
	Configured(context.Context) (bool, error)
	Setup(ctx context.Context, setupToken, username, password string) error
	Login(ctx context.Context, clientKey, username, password string) (auth.Session, error)
	Authenticate(ctx context.Context, token string) (auth.Session, error)
	Logout(ctx context.Context, token string) error
	LogoutAll(ctx context.Context) error
	ChangePassword(ctx context.Context, currentPassword, newPassword string) error
	ChangeUsername(ctx context.Context, currentPassword, newUsername string) error
}

func WithAuth(service AuthService) Option {
	return func(server *Server) error {
		if service == nil {
			return errors.New("auth service is required")
		}
		server.auth = service
		return nil
	}
}

func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/api/v1/auth/setup", s.handleAuthSetup)
	mux.HandleFunc("/api/v1/auth/login", s.handleAuthLogin)
	mux.HandleFunc("/api/v1/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("/api/v1/auth/logout-all", s.handleAuthLogoutAll)
	mux.HandleFunc("/api/v1/auth/password", s.handleAuthPassword)
	mux.HandleFunc("/api/v1/auth/username", s.handleAuthUsername)
}

// requireAuth guards /api/v1/* once the administrator account exists.
// Before setup completes the API stays open, which is safe because the
// management listener is forced onto a loopback address until then.
// Mutating requests must also present the session's CSRF token.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	exempt := map[string]struct{}{
		"/api/v1/auth/status": {},
		"/api/v1/auth/setup":  {},
		"/api/v1/auth/login":  {},
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if s.auth == nil || !strings.HasPrefix(request.URL.Path, "/api/v1/") {
			next.ServeHTTP(writer, request)
			return
		}
		if _, isExempt := exempt[request.URL.Path]; isExempt {
			next.ServeHTTP(writer, request)
			return
		}
		configured, err := s.auth.Configured(request.Context())
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		if !configured {
			next.ServeHTTP(writer, request)
			return
		}
		session, err := s.auth.Authenticate(request.Context(), sessionToken(request))
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		switch request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if request.Header.Get("X-CSRF-Token") != session.CSRFToken {
				s.writeAPIError(writer, request, http.StatusForbidden, "csrf_token_mismatch", "missing or invalid CSRF token")
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

func (s *Server) handleAuthStatus(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet)
		return
	}
	configured, err := s.auth.Configured(request.Context())
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	response := map[string]any{"configured": configured, "authenticated": false}
	if configured {
		if session, err := s.auth.Authenticate(request.Context(), sessionToken(request)); err == nil {
			response["authenticated"] = true
			response["username"] = session.Username
			response["csrf_token"] = session.CSRFToken
		}
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) handleAuthSetup(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, request, http.MethodPost)
		return
	}
	var body struct {
		SetupToken string `json:"setup_token"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if err := decodeJSONBody(writer, request, &body); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.auth.Setup(request.Context(), body.SetupToken, body.Username, body.Password); err != nil {
		s.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]string{"status": "configured"})
}

func (s *Server) handleAuthLogin(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, request, http.MethodPost)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSONBody(writer, request, &body); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	session, err := s.auth.Login(request.Context(), clientAddress(request), body.Username, body.Password)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	http.SetCookie(writer, sessionCookie(request, session.Token, 0))
	writeJSON(writer, http.StatusOK, map[string]any{
		"username":   session.Username,
		"csrf_token": session.CSRFToken,
		"expires_at": session.ExpiresAt,
	})
}

func (s *Server) handleAuthLogout(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, request, http.MethodPost)
		return
	}
	if err := s.auth.Logout(request.Context(), sessionToken(request)); err != nil {
		s.handleError(writer, request, err)
		return
	}
	http.SetCookie(writer, sessionCookie(request, "", -1))
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuthLogoutAll(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, request, http.MethodPost)
		return
	}
	if err := s.auth.LogoutAll(request.Context()); err != nil {
		s.handleError(writer, request, err)
		return
	}
	http.SetCookie(writer, sessionCookie(request, "", -1))
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuthPassword(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		methodNotAllowed(writer, request, http.MethodPut)
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeJSONBody(writer, request, &body); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.auth.ChangePassword(request.Context(), body.CurrentPassword, body.NewPassword); err != nil {
		s.handleError(writer, request, err)
		return
	}
	http.SetCookie(writer, sessionCookie(request, "", -1))
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuthUsername(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPut {
		methodNotAllowed(writer, request, http.MethodPut)
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewUsername     string `json:"new_username"`
	}
	if err := decodeJSONBody(writer, request, &body); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := s.auth.ChangeUsername(request.Context(), body.CurrentPassword, body.NewUsername); err != nil {
		s.handleError(writer, request, err)
		return
	}
	http.SetCookie(writer, sessionCookie(request, "", -1))
	writer.WriteHeader(http.StatusNoContent)
}

func sessionToken(request *http.Request) string {
	cookie, err := request.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func sessionCookie(request *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   request.TLS != nil,
		SameSite: http.SameSiteStrictMode,
	}
}

func clientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}
