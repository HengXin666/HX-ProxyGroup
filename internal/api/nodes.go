package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/node"
)

func (s *Server) handleNodes(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost {
		var checkRequest struct {
			NodeIDs []string `json:"node_ids"`
		}
		if err := decodeJSONBody(writer, request, &checkRequest); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		results, err := s.nodes.CheckMany(request.Context(), checkRequest.NodeIDs)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": results})
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet, http.MethodPost)
		return
	}
	limit, err := parseIntegerQuery(request, "limit", 200, 1, 1000)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	offset, err := parseIntegerQuery(request, "offset", 0, 0, 1_000_000)
	if err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	items, err := s.nodes.List(request.Context(), node.Filter{
		Search:   request.URL.Query().Get("search"),
		Protocol: request.URL.Query().Get("protocol"),
		State:    request.URL.Query().Get("state"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"items":  items,
		"limit":  limit,
		"offset": offset,
	})
}

func (s *Server) handleNode(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/nodes/")
	if path == "" || path == request.URL.Path {
		http.NotFound(writer, request)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[1] == "check" {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		result, err := s.nodes.Check(request.Context(), parts[0])
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, result)
		return
	}
	if len(parts) == 2 && (parts[1] == "disable" || parts[1] == "enable") {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, request, http.MethodPost)
			return
		}
		var (
			item node.Node
			err  error
		)
		if parts[1] == "disable" {
			item, err = s.nodes.Disable(request.Context(), parts[0])
		} else {
			item, err = s.nodes.Enable(request.Context(), parts[0])
		}
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, item)
		return
	}
	if len(parts) != 1 || request.Method != http.MethodGet {
		if len(parts) == 1 {
			methodNotAllowed(writer, request, http.MethodGet)
		} else {
			http.NotFound(writer, request)
		}
		return
	}
	item, err := s.nodes.Get(request.Context(), parts[0])
	if errors.Is(err, node.ErrNotFound) {
		s.writeAPIError(writer, request, http.StatusNotFound, "node_not_found", "node not found")
		return
	}
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, item)
}
