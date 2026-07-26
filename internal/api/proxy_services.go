package api

import (
	"errors"
	"net/http"

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
