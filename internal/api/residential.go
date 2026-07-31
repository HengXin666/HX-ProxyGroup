package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/residential"
)

// handleResidentialPresets serves the vendor preset catalog. Presets carry a
// `verified` flag so the UI can warn that an unverified gateway syntax must be
// confirmed with a test connection before it is trusted.
func (s *Server) handleResidentialPresets(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items":           residential.Presets(),
		"placeholders":    residential.SupportedPlaceholders(),
		"protocols":       residential.SupportedProtocols(),
		"rotation_modes":  residential.SupportedRotationModes(),
		"exit_ip_default": residential.DefaultExitIPEndpoint,
	})
}

func (s *Server) handleResidentialProviders(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		items, err := s.residential.ListProviders(request.Context())
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var createRequest residential.CreateProviderRequest
		if err := decodeJSONBody(writer, request, &createRequest); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		created, err := s.residential.CreateProvider(request.Context(), createRequest)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writer.Header().Set("Location", request.URL.Path+"/"+created.ID)
		writeJSON(writer, http.StatusCreated, created)
	default:
		methodNotAllowed(writer, request, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleResidentialProvider(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/residential/providers/")
	if path == "" || path == request.URL.Path {
		http.NotFound(writer, request)
		return
	}
	if identifier, action, found := strings.Cut(path, "/"); found {
		if action != "test" || identifier == "" {
			http.NotFound(writer, request)
			return
		}
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		var testRequest struct {
			ExitIPEndpoint string `json:"exit_ip_endpoint,omitempty"`
		}
		if err := decodeJSONBody(writer, request, &testRequest); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		result, err := s.residential.TestProvider(request.Context(), identifier, testRequest.ExitIPEndpoint)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
		return
	}
	id := path
	switch request.Method {
	case http.MethodGet:
		item, err := s.residential.GetProvider(request.Context(), id)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, item)
	case http.MethodPut:
		var updateRequest residential.UpdateProviderRequest
		if err := decodeJSONBody(writer, request, &updateRequest); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		updated, err := s.residential.UpdateProvider(request.Context(), id, updateRequest)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, updated)
	case http.MethodDelete:
		version, err := parseIntegerQuery(request, "version", 0, 1, 1_000_000_000)
		if err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if err := s.residential.DeleteProvider(request.Context(), id, version); err != nil {
			s.handleError(writer, request, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(writer, request, http.MethodGet, http.MethodPut, http.MethodDelete)
	}
}

func (s *Server) handleResidentialChannels(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		items, err := s.residential.ListChannels(request.Context())
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var createRequest residential.CreateChannelRequest
		if err := decodeJSONBody(writer, request, &createRequest); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		created, err := s.residential.CreateChannel(request.Context(), createRequest)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writer.Header().Set("Location", request.URL.Path+"/"+created.ID)
		writeJSON(writer, http.StatusCreated, created)
	default:
		methodNotAllowed(writer, request, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleResidentialChannel(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/residential/channels/")
	if path == "" || path == request.URL.Path {
		http.NotFound(writer, request)
		return
	}
	if identifier, action, found := strings.Cut(path, "/"); found {
		if identifier == "" {
			http.NotFound(writer, request)
			return
		}
		s.handleResidentialChannelAction(writer, request, identifier, action)
		return
	}
	id := path
	switch request.Method {
	case http.MethodGet:
		item, err := s.residential.GetChannel(request.Context(), id)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, item)
	case http.MethodPut:
		var updateRequest residential.UpdateChannelRequest
		if err := decodeJSONBody(writer, request, &updateRequest); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		updated, err := s.residential.UpdateChannel(request.Context(), id, updateRequest)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, updated)
	case http.MethodDelete:
		version, err := parseIntegerQuery(request, "version", 0, 1, 1_000_000_000)
		if err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if err := s.residential.DeleteChannel(request.Context(), id, version); err != nil {
			s.handleError(writer, request, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(writer, request, http.MethodGet, http.MethodPut, http.MethodDelete)
	}
}

// handleResidentialChannelAction serves the administrator-side channel actions.
func (s *Server) handleResidentialChannelAction(
	writer http.ResponseWriter,
	request *http.Request,
	id string,
	action string,
) {
	switch action {
	case "rotate":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		result, err := s.residential.RotateChannel(request.Context(), id)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
	case "rotate-token":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		updated, err := s.residential.RotateChannelToken(request.Context(), id)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, updated)
	case "refresh-pool":
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		if err := s.residential.RefreshChannelPool(request.Context(), id); err != nil {
			s.handleError(writer, request, err)
			return
		}
		item, err := s.residential.GetChannel(request.Context(), id)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, item)
	default:
		http.NotFound(writer, request)
	}
}

// handleResidentialRotatePublic serves the consumer-facing rotation API.
//
//	GET  /rot/<token>       -> current session index and pool size
//	POST /rot/<token>/next  -> advance to the next residential IP
//
// The token is the only credential, so this route is intentionally outside the
// authenticated /api/v1 namespace. Responses never include the token, the
// gateway credentials or the pool contents, and rotation is rate limited per
// channel by the service.
func (s *Server) handleResidentialRotatePublic(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/rot/")
	if path == "" || path == request.URL.Path {
		http.NotFound(writer, request)
		return
	}
	token, action, hasAction := strings.Cut(path, "/")
	if token == "" || (hasAction && action != "next") {
		http.NotFound(writer, request)
		return
	}

	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")

	if !hasAction {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer, request, http.MethodGet)
			return
		}
		status, err := s.residential.ChannelStatusByToken(request.Context(), token)
		if err != nil {
			s.handleRotateError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, status)
		return
	}
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, request, http.MethodPost)
		return
	}
	result, err := s.residential.RotateChannelByToken(request.Context(), token)
	if err != nil {
		s.handleRotateError(writer, request, err)
		return
	}
	// The channel id identifies a private resource, so the public response omits
	// it and reports only what the consumer needs.
	writeJSON(writer, http.StatusOK, map[string]any{
		"session_index":  result.SessionIndex,
		"pool_size":      result.PoolSize,
		"exit_ip":        result.ExitIP,
		"latency_ms":     result.LatencyMS,
		"rotated_at":     result.RotatedAt,
		"pool_refreshed": result.PoolRefreshed,
	})
}

// handleRotateError keeps public rotation failures opaque: an unknown, disabled
// or non-rotatable token must be indistinguishable from a wrong one so the route
// cannot be used to enumerate channels.
func (s *Server) handleRotateError(writer http.ResponseWriter, request *http.Request, err error) {
	if isResidentialLookupFailure(err) {
		http.NotFound(writer, request)
		return
	}
	s.handleError(writer, request, err)
}

func isResidentialLookupFailure(err error) bool {
	return errors.Is(err, residential.ErrNotFound) || errors.Is(err, residential.ErrInvalid)
}
