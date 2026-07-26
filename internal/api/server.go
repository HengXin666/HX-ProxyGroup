package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/alert"
	"github.com/HengXin666/HX-ProxyGroup/internal/artifact"
	"github.com/HengXin666/HX-ProxyGroup/internal/auth"
	"github.com/HengXin666/HX-ProxyGroup/internal/bundle"
	"github.com/HengXin666/HX-ProxyGroup/internal/dataplane/mihomo"
	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/node"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxyservice"
	"github.com/HengXin666/HX-ProxyGroup/internal/subscription"
)

type BundleService interface {
	Create(context.Context, artifact.Kind, bundle.CreateOptions) (artifact.Record, error)
	List(artifact.Kind) ([]artifact.Record, error)
	Open(string) (artifact.Record, *os.File, error)
	Delete(string) error
	Verify(context.Context, string) (bundle.VerifyResult, error)
}

type SubscriptionService interface {
	Create(context.Context, subscription.CreateRequest) (subscription.Subscription, error)
	Get(context.Context, string) (subscription.Subscription, error)
	List(context.Context, int, int) ([]subscription.Subscription, error)
	Update(context.Context, string, subscription.UpdateRequest) (subscription.Subscription, error)
	Delete(context.Context, string, int) error
	Refresh(context.Context, string) (subscription.RefreshResult, error)
	RefreshMany(context.Context, []string) ([]subscription.BatchRefreshResult, error)
}

type NodeService interface {
	List(context.Context, node.Filter) ([]node.Node, error)
	Get(context.Context, string) (node.Node, error)
	Check(context.Context, string) (node.CheckResult, error)
	CheckMany(context.Context, []string) ([]node.CheckResult, error)
	Disable(context.Context, string) (node.Node, error)
	Enable(context.Context, string) (node.Node, error)
}

type ProxyGroupService interface {
	Create(context.Context, proxygroup.CreateRequest) (proxygroup.Group, error)
	Get(context.Context, string) (proxygroup.Group, error)
	List(context.Context) ([]proxygroup.Group, error)
	Update(context.Context, string, proxygroup.UpdateRequest) (proxygroup.Group, error)
	Delete(context.Context, string, int) error
}

type ListenerService interface {
	Create(context.Context, listener.CreateRequest) (listener.Listener, error)
	Get(context.Context, string) (listener.Listener, error)
	List(context.Context) ([]listener.Listener, error)
	Update(context.Context, string, listener.UpdateRequest) (listener.Listener, error)
	Delete(context.Context, string, int) error
}

type DataPlaneService interface {
	Apply(context.Context) error
	Status() mihomo.Status
}

type ProxyServiceService interface {
	Create(context.Context, proxyservice.CreateRequest) (proxyservice.ServiceRecord, error)
}

type Option func(*Server) error

func WithSubscriptions(service SubscriptionService) Option {
	return func(server *Server) error {
		if service == nil {
			return errors.New("subscription service is required")
		}
		server.subscriptions = service
		return nil
	}
}

func WithNodes(service NodeService) Option {
	return func(server *Server) error {
		if service == nil {
			return errors.New("node service is required")
		}
		server.nodes = service
		return nil
	}
}

func WithProxyGroups(service ProxyGroupService) Option {
	return func(server *Server) error {
		if service == nil {
			return errors.New("proxy group service is required")
		}
		server.proxyGroups = service
		return nil
	}
}

func WithListeners(service ListenerService) Option {
	return func(server *Server) error {
		if service == nil {
			return errors.New("listener service is required")
		}
		server.listeners = service
		return nil
	}
}

func WithDataPlane(service DataPlaneService) Option {
	return func(server *Server) error {
		if service == nil {
			return errors.New("dataplane service is required")
		}
		server.dataplane = service
		return nil
	}
}

func WithProxyServices(service ProxyServiceService) Option {
	return func(server *Server) error {
		if service == nil {
			return errors.New("proxy service application service is required")
		}
		server.proxyServices = service
		return nil
	}
}

type Server struct {
	bundles       BundleService
	subscriptions SubscriptionService
	nodes         NodeService
	proxyGroups   ProxyGroupService
	listeners     ListenerService
	proxyServices ProxyServiceService
	dataplane     DataPlaneService
	auth          AuthService
	alerts        AlertService
	logger        *slog.Logger
	ready         atomic.Bool
}

type errorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func NewServer(bundles BundleService, logger *slog.Logger, options ...Option) (*Server, error) {
	if bundles == nil {
		return nil, errors.New("bundle service is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	server := &Server{bundles: bundles, logger: logger}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(server); err != nil {
			return nil, err
		}
	}
	server.ready.Store(true)
	return server, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health/live", s.handleLive)
	mux.HandleFunc("/health/ready", s.handleReady)
	mux.HandleFunc("/api/v1/backups", func(writer http.ResponseWriter, request *http.Request) {
		s.handleCollection(writer, request, artifact.KindBackup)
	})
	mux.HandleFunc("/api/v1/backups/", func(writer http.ResponseWriter, request *http.Request) {
		s.handleItem(writer, request, artifact.KindBackup, "/api/v1/backups/")
	})
	mux.HandleFunc("/api/v1/exports", func(writer http.ResponseWriter, request *http.Request) {
		s.handleCollection(writer, request, artifact.KindExport)
	})
	mux.HandleFunc("/api/v1/exports/", func(writer http.ResponseWriter, request *http.Request) {
		s.handleItem(writer, request, artifact.KindExport, "/api/v1/exports/")
	})
	if s.subscriptions != nil {
		mux.HandleFunc("/api/v1/subscriptions", s.handleSubscriptions)
		mux.HandleFunc("/api/v1/subscriptions/", s.handleSubscription)
	}
	if s.nodes != nil {
		mux.HandleFunc("/api/v1/nodes", s.handleNodes)
		mux.HandleFunc("/api/v1/nodes/", s.handleNode)
	}
	if s.proxyGroups != nil {
		mux.HandleFunc("/api/v1/proxy-groups", s.handleProxyGroups)
		mux.HandleFunc("/api/v1/proxy-groups/", s.handleProxyGroup)
	}
	if s.listeners != nil {
		mux.HandleFunc("/api/v1/listeners", s.handleListeners)
		mux.HandleFunc("/api/v1/listeners/", s.handleListener)
	}
	if s.proxyServices != nil {
		mux.HandleFunc("/api/v1/proxy-services", s.handleProxyServices)
	}
	if s.dataplane != nil {
		mux.HandleFunc("/api/v1/dataplane/status", s.handleDataPlaneStatus)
		mux.HandleFunc("/api/v1/dataplane/apply", s.handleDataPlaneApply)
	}
	if s.alerts != nil {
		mux.HandleFunc("/api/v1/alerts", s.handleAlerts)
		mux.HandleFunc("/api/v1/alerts/", s.handleAlertItem)
	}
	var handler http.Handler = mux
	if s.auth != nil {
		s.registerAuthRoutes(mux)
		handler = s.requireAuth(handler)
	}
	return s.requestContext(s.securityHeaders(handler))
}

func (s *Server) SetReady(ready bool) {
	s.ready.Store(ready)
}

func (s *Server) handleLive(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "live"})
}

func (s *Server) handleReady(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet)
		return
	}
	if !s.ready.Load() {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleCollection(writer http.ResponseWriter, request *http.Request, kind artifact.Kind) {
	switch request.Method {
	case http.MethodGet:
		records, err := s.bundles.List(kind)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"items": records})
	case http.MethodPost:
		var options bundle.CreateOptions
		if err := decodeJSONBody(writer, request, &options); err != nil {
			s.writeAPIError(writer, request, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		record, err := s.bundles.Create(request.Context(), kind, options)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		writer.Header().Set("Location", request.URL.Path+"/"+record.ID)
		writeJSON(writer, http.StatusCreated, record)
	default:
		methodNotAllowed(writer, request, http.MethodGet, http.MethodPost)
	}
}

func (s *Server) handleItem(writer http.ResponseWriter, request *http.Request, kind artifact.Kind, prefix string) {
	remainder := strings.TrimPrefix(request.URL.Path, prefix)
	if remainder == "" || remainder == request.URL.Path {
		http.NotFound(writer, request)
		return
	}
	parts := strings.Split(remainder, "/")
	if len(parts) > 2 || parts[0] == "" {
		http.NotFound(writer, request)
		return
	}
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch action {
	case "":
		s.handleMetadata(writer, request, kind, id)
	case "download":
		s.handleDownload(writer, request, kind, id)
	case "verify":
		s.handleVerify(writer, request, kind, id)
	default:
		http.NotFound(writer, request)
	}
}

func (s *Server) handleMetadata(writer http.ResponseWriter, request *http.Request, kind artifact.Kind, id string) {
	switch request.Method {
	case http.MethodGet:
		record, file, err := s.bundles.Open(id)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		_ = file.Close()
		if record.Kind != kind {
			s.handleError(writer, request, artifact.ErrNotFound)
			return
		}
		writeJSON(writer, http.StatusOK, record)
	case http.MethodDelete:
		record, file, err := s.bundles.Open(id)
		if err != nil {
			s.handleError(writer, request, err)
			return
		}
		_ = file.Close()
		if record.Kind != kind {
			s.handleError(writer, request, artifact.ErrNotFound)
			return
		}
		if err := s.bundles.Delete(id); err != nil {
			s.handleError(writer, request, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(writer, request, http.MethodGet, http.MethodDelete)
	}
}

func (s *Server) handleDownload(writer http.ResponseWriter, request *http.Request, kind artifact.Kind, id string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, request, http.MethodGet)
		return
	}
	record, file, err := s.bundles.Open(id)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	defer file.Close()
	if record.Kind != kind {
		s.handleError(writer, request, artifact.ErrNotFound)
		return
	}
	writer.Header().Set("Content-Type", record.ContentType)
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, path.Base(record.Filename)))
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", record.Size))
	writer.Header().Set("ETag", `"sha256:`+record.SHA256+`"`)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	if _, err := io.Copy(writer, file); err != nil {
		s.logger.WarnContext(request.Context(), "artifact download interrupted", "artifact_id", id, "error", err)
	}
}

func (s *Server) handleVerify(writer http.ResponseWriter, request *http.Request, kind artifact.Kind, id string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, request, http.MethodPost)
		return
	}
	record, file, err := s.bundles.Open(id)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	_ = file.Close()
	if record.Kind != kind {
		s.handleError(writer, request, artifact.ErrNotFound)
		return
	}
	result, err := s.bundles.Verify(request.Context(), id)
	if err != nil {
		s.handleError(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) handleError(writer http.ResponseWriter, request *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrSessionExpired):
		s.writeAPIError(writer, request, http.StatusUnauthorized, "unauthenticated", "authentication required")
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.writeAPIError(writer, request, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
	case errors.Is(err, auth.ErrLockedOut):
		s.writeAPIError(writer, request, http.StatusTooManyRequests, "login_locked_out", "too many failed logins, retry later")
	case errors.Is(err, auth.ErrInvalidSetupToken):
		s.writeAPIError(writer, request, http.StatusForbidden, "invalid_setup_token", "setup token is missing or wrong")
	case errors.Is(err, auth.ErrAlreadyConfigured):
		s.writeAPIError(writer, request, http.StatusConflict, "already_configured", "administrator account already exists")
	case errors.Is(err, auth.ErrNotConfigured):
		s.writeAPIError(writer, request, http.StatusConflict, "admin_not_configured", "administrator account is not configured yet")
	case errors.Is(err, auth.ErrWeakPassword):
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "weak_password", err.Error())
	case errors.Is(err, alert.ErrNotFound):
		s.writeAPIError(writer, request, http.StatusNotFound, "not_found", "alert not found")
	case errors.Is(err, alert.ErrInvalidSetting):
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, alert.ErrNoChannel):
		s.writeAPIError(writer, request, http.StatusConflict, "alert_channel_not_configured", "configure SMTP settings first")
	case errors.Is(err, artifact.ErrNotFound):
		s.writeAPIError(writer, request, http.StatusNotFound, "not_found", "artifact not found")
	case errors.Is(err, bundle.ErrSecretBundleDisabled):
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "secret_export_disabled", err.Error())
	case errors.Is(err, proxygroup.ErrInvalid), errors.Is(err, listener.ErrInvalid):
		s.writeAPIError(writer, request, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, proxygroup.ErrNotFound), errors.Is(err, listener.ErrNotFound), errors.Is(err, node.ErrNotFound):
		s.writeAPIError(writer, request, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, node.ErrCheckUnavailable), errors.Is(err, mihomo.ErrNotRunning):
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "node_check_unavailable", "node quality check is unavailable")
	case errors.Is(err, proxygroup.ErrConflict), errors.Is(err, listener.ErrConflict):
		s.writeAPIError(writer, request, http.StatusConflict, "conflict", "resource changed or conflicts with existing configuration")
	case errors.Is(err, mihomo.ErrUnavailable):
		s.writeAPIError(writer, request, http.StatusServiceUnavailable, "dataplane_unavailable", "mihomo is not installed or configured")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.writeAPIError(writer, request, http.StatusRequestTimeout, "request_timeout", "request was canceled or timed out")
	default:
		s.logger.ErrorContext(request.Context(), "API operation failed", "request_id", requestID(request), "error", err)
		s.writeAPIError(writer, request, http.StatusInternalServerError, "internal_error", "operation failed")
	}
}

func (s *Server) writeAPIError(writer http.ResponseWriter, request *http.Request, status int, code, message string) {
	writeJSON(writer, status, errorResponse{Error: apiError{
		Code:      code,
		Message:   message,
		RequestID: requestID(request),
	}})
}

func (s *Server) requestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestID := newRequestID()
		writer.Header().Set("X-Request-ID", requestID)
		ctx := context.WithValue(request.Context(), requestIDKey{}, requestID)
		startedAt := time.Now()
		next.ServeHTTP(writer, request.WithContext(ctx))
		s.logger.InfoContext(ctx, "HTTP request", "method", request.Method, "path", request.URL.Path, "duration_ms", time.Since(startedAt).Milliseconds(), "request_id", requestID)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(writer, request)
	})
}

func decodeJSONBody(writer http.ResponseWriter, request *http.Request, destination any) error {
	if request.Body == nil {
		return nil
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain exactly one JSON object")
	}
	return nil
}

func methodNotAllowed(writer http.ResponseWriter, request *http.Request, methods ...string) {
	writer.Header().Set("Allow", strings.Join(methods, ", "))
	writeJSON(writer, http.StatusMethodNotAllowed, errorResponse{Error: apiError{
		Code:      "method_not_allowed",
		Message:   "method not allowed",
		RequestID: requestID(request),
	}})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

type requestIDKey struct{}

func requestID(request *http.Request) string {
	value, _ := request.Context().Value(requestIDKey{}).(string)
	return value
}

func newRequestID() string {
	var data [12]byte
	if _, err := rand.Read(data[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data[:])
}
