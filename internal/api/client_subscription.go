package api

import "net/http"

func (s *Server) handleClientSubscription(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		info, err := s.clientSubscriptions.Info(request.Context(), request.Host)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, info)
	case http.MethodPost:
		var body struct {
			Action string `json:"action"`
		}
		if err := decodeJSONBody(writer, request, &body); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		if body.Action != "rotate" {
			s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "validation_failed", "action must be rotate")
			return
		}
		info, err := s.clientSubscriptions.Rotate(request.Context(), request.Host)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, info)
	default:
		methodNotAllowed(writer, request, http.MethodGet, http.MethodPost)
	}
}
