package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/proxyservice"
)

func (s *Server) handleProxyServices(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, request, http.MethodPost)
		return
	}
	var createRequest proxyservice.CreateRequest
	if err := decodeJSONBody(writer, request, &createRequest); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	created, err := s.proxyServices.Create(request.Context(), createRequest)
	if err != nil {
		if errors.Is(err, proxyservice.ErrCreateFailed) {
			s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "proxy_service_create_failed", err.Error())
			return
		}
		s.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusCreated, created)
}

func (s *Server) handleProxyService(writer http.ResponseWriter, request *http.Request) {
	id := strings.TrimPrefix(request.URL.Path, "/api/v1/proxy-services/")
	if id == "" || id == request.URL.Path || strings.Contains(id, "/") {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPut {
		methodNotAllowed(writer, request, http.MethodPut)
		return
	}
	var updateRequest proxyservice.UpdateRequest
	if err := decodeJSONBody(writer, request, &updateRequest); err != nil {
		s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	updated, err := s.proxyServices.Update(request.Context(), updateRequest)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, updated)
}
