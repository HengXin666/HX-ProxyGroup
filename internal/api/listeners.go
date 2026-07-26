package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
)

func (s *Server) handleListeners(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		items, err := s.listeners.List(request.Context())
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": items})
	case http.MethodPost:
		var createRequest listener.CreateRequest
		if err := decodeJSONBody(writer, request, &createRequest); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		created, err := s.listeners.Create(request.Context(), createRequest)
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

// handleListenerShare serves the public token-addressed subscription export.
// The default response body is the conventional base64 subscription payload;
// ?format=uri returns the plain URI list.
func (s *Server) handleListenerShare(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet)
		return
	}
	token := strings.TrimPrefix(request.URL.Path, "/sub/")
	if token == "" || strings.Contains(token, "/") {
		http.NotFound(writer, request)
		return
	}
	export, err := s.listeners.ExportByShareToken(request.Context(), token, request.Host)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	body := export.EncodeSubscription()
	if request.URL.Query().Get("format") == "uri" {
		body = export.Body
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Content-Disposition", `attachment; filename="`+export.FileName+`"`)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(body))
}

func (s *Server) handleListener(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/listeners/")
	if path == "" || path == request.URL.Path {
		http.NotFound(writer, request)
		return
	}
	if identifier, action, found := strings.Cut(path, "/"); found {
		if action == "rotate-share" && identifier != "" {
			if request.Method != http.MethodPost {
				methodNotAllowed(writer, request, http.MethodPost)
				return
			}
			updated, err := s.listeners.RotateShareToken(request.Context(), identifier)
			if err != nil {
				s.handleError(writer, request, err)
				return
			}
			writeJSON(writer, http.StatusOK, updated)
			return
		}
		http.NotFound(writer, request)
		return
	}
	id := path
	switch request.Method {
	case http.MethodGet:
		item, err := s.listeners.Get(request.Context(), id)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, item)
	case http.MethodPut:
		var updateRequest listener.UpdateRequest
		if err := decodeJSONBody(writer, request, &updateRequest); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		updated, err := s.listeners.Update(request.Context(), id, updateRequest)
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
		if err := s.listeners.Delete(request.Context(), id, version); err != nil {
			s.handleError(writer, request, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(writer, request, http.MethodGet, http.MethodPut, http.MethodDelete)
	}
}

func isListenerError(err error) bool {
	return errors.Is(err, listener.ErrNotFound) ||
		errors.Is(err, listener.ErrConflict) ||
		errors.Is(err, listener.ErrInvalid)
}
