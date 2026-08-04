package api

import (
	"context"
	"net/http"
	"time"
)

func (s *Server) handleSystemUpdate(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, request, http.MethodPost)
		return
	}
	if s.auth == nil {
		s.writeAPIError(writer, request, http.StatusForbidden, "update_requires_auth", "automatic update requires administrator authentication")
		return
	}
	token := sessionToken(request)
	session, err := s.auth.Authenticate(request.Context(), token)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	twoFactor, err := s.auth.TwoFactorStatus(request.Context(), token)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	if !twoFactor.Enabled || !twoFactor.Verified {
		s.writeAPIError(writer, request, http.StatusForbidden, "update_requires_two_factor", "verify a current two-factor authentication code before updating")
		return
	}

	updateContext, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()
	if err := s.updater.TriggerUpdate(updateContext); err != nil {
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "update_unavailable", err.Error())
		return
	}
	s.logger.Info(
		"automatic update scheduled",
		"audit", "system_update",
		"actor", session.Username,
		"remote", clientAddress(request),
	)
	writeJSON(writer, http.StatusAccepted, map[string]bool{"accepted": true})
}
