package listener

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

var (
	ErrNotFound = errors.New("listener not found")
	ErrConflict = errors.New("listener conflict")
	ErrInvalid  = errors.New("invalid listener")
)

type Repository interface {
	CreateListener(context.Context, store.ListenerRecord) (store.ListenerRecord, error)
	GetListener(context.Context, string) (store.ListenerRecord, error)
	GetListenerByShareToken(context.Context, string) (store.ListenerRecord, error)
	ListListeners(context.Context) ([]store.ListenerRecord, error)
	UpdateListener(context.Context, store.ListenerRecord, int) (store.ListenerRecord, error)
	RotateListenerShareToken(context.Context, string, string) (store.ListenerRecord, error)
	DeleteListener(context.Context, string, int) error
	GetProxyGroup(context.Context, string) (store.ProxyGroupRecord, error)
}

type Cipher interface {
	Seal([]byte, []byte) ([]byte, error)
	Open([]byte, []byte) ([]byte, error)
}

type Reconciler interface {
	Apply(context.Context) error
}

type Auth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type Transport struct {
	Type   string `json:"type"`
	WSPath string `json:"ws_path,omitempty"`
}

// WebSocketPathPrefix reserves a dedicated namespace for public WebSocket
// proxy routes. Keeping the edge namespace separate from /api, /sub and the
// frontend prevents future control-plane routes from colliding with a proxy
// listener path.
const WebSocketPathPrefix = "/__hx-proxy__/"

type PublicEndpoint struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	TLS  bool   `json:"tls"`
}

type Listener struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	BindAddress    string         `json:"bind_address"`
	Port           int            `json:"port"`
	ProxyGroupID   string         `json:"proxy_group_id"`
	AuthConfigured bool           `json:"auth_configured"`
	Transport      Transport      `json:"transport"`
	PublicEndpoint PublicEndpoint `json:"public_endpoint"`
	SharePath      string         `json:"share_path,omitempty"`
	Enabled        bool           `json:"enabled"`
	Version        int            `json:"version"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type CreateRequest struct {
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	BindAddress    string         `json:"bind_address"`
	Port           int            `json:"port"`
	ProxyGroupID   string         `json:"proxy_group_id"`
	Auth           *Auth          `json:"auth,omitempty"`
	Transport      Transport      `json:"transport,omitempty"`
	PublicEndpoint PublicEndpoint `json:"public_endpoint,omitempty"`
	Enabled        *bool          `json:"enabled,omitempty"`
}

type UpdateRequest struct {
	Version        int            `json:"version"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	BindAddress    string         `json:"bind_address"`
	Port           int            `json:"port"`
	ProxyGroupID   string         `json:"proxy_group_id"`
	Auth           *Auth          `json:"auth,omitempty"`
	Transport      Transport      `json:"transport,omitempty"`
	PublicEndpoint PublicEndpoint `json:"public_endpoint,omitempty"`
	ClearAuth      bool           `json:"clear_auth,omitempty"`
	Enabled        bool           `json:"enabled"`
}

type Service struct {
	repository Repository
	cipher     Cipher
	reconciler Reconciler
	now        func() time.Time
}

func NewService(repository Repository, cipher Cipher, reconciler Reconciler) (*Service, error) {
	if repository == nil {
		return nil, errors.New("listener repository is required")
	}
	if cipher == nil {
		return nil, errors.New("listener cipher is required")
	}
	if reconciler == nil {
		return nil, errors.New("listener reconciler is required")
	}
	return &Service{repository: repository, cipher: cipher, reconciler: reconciler, now: time.Now}, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (Listener, error) {
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	id, err := newID()
	if err != nil {
		return Listener{}, err
	}
	normalized, err := s.normalize(ctx, id, request.Name, request.Kind, request.BindAddress, request.Port, request.ProxyGroupID, request.Auth, nil, request.Transport, request.PublicEndpoint, enabled)
	if err != nil {
		return Listener{}, err
	}
	shareToken, err := newShareToken()
	if err != nil {
		return Listener{}, err
	}
	now := s.now().UTC()
	record := store.ListenerRecord{
		ID:                  id,
		Name:                normalized.Name,
		Kind:                normalized.Kind,
		BindAddress:         normalized.BindAddress,
		Port:                normalized.Port,
		ProxyGroupID:        normalized.ProxyGroupID,
		AuthMode:            normalized.AuthMode,
		AuthConfigEncrypted: normalized.AuthConfigEncrypted,
		TransportJSON:       normalized.TransportJSON,
		PublicEndpointJSON:  normalized.PublicEndpointJSON,
		ShareToken:          shareToken,
		Enabled:             normalized.Enabled,
		Version:             1,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	created, err := s.repository.CreateListener(ctx, record)
	if err != nil {
		return Listener{}, mapStoreError(err)
	}
	if err := s.reconciler.Apply(ctx); err != nil {
		return fromRecord(created), fmt.Errorf("listener saved but dataplane apply failed: %w", err)
	}
	return fromRecord(created), nil
}

func (s *Service) Get(ctx context.Context, id string) (Listener, error) {
	record, err := s.repository.GetListener(ctx, id)
	if err != nil {
		return Listener{}, mapStoreError(err)
	}
	return fromRecord(record), nil
}

func (s *Service) List(ctx context.Context) ([]Listener, error) {
	records, err := s.repository.ListListeners(ctx)
	if err != nil {
		return nil, err
	}
	listeners := make([]Listener, 0, len(records))
	for _, record := range records {
		listeners = append(listeners, fromRecord(record))
	}
	return listeners, nil
}

func (s *Service) Update(ctx context.Context, id string, request UpdateRequest) (Listener, error) {
	if request.Version < 1 {
		return Listener{}, fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	existing, err := s.repository.GetListener(ctx, id)
	if err != nil {
		return Listener{}, mapStoreError(err)
	}
	currentAuth := existing.AuthConfigEncrypted
	if request.ClearAuth {
		currentAuth = nil
	}
	normalized, err := s.normalize(ctx, id, request.Name, request.Kind, request.BindAddress, request.Port, request.ProxyGroupID, request.Auth, currentAuth, request.Transport, request.PublicEndpoint, request.Enabled)
	if err != nil {
		return Listener{}, err
	}
	existing.Name = normalized.Name
	existing.Kind = normalized.Kind
	existing.BindAddress = normalized.BindAddress
	existing.Port = normalized.Port
	existing.ProxyGroupID = normalized.ProxyGroupID
	existing.AuthMode = normalized.AuthMode
	existing.AuthConfigEncrypted = normalized.AuthConfigEncrypted
	existing.TransportJSON = normalized.TransportJSON
	existing.PublicEndpointJSON = normalized.PublicEndpointJSON
	existing.Enabled = normalized.Enabled
	existing.UpdatedAt = s.now().UTC()
	updated, err := s.repository.UpdateListener(ctx, existing, request.Version)
	if err != nil {
		return Listener{}, mapStoreError(err)
	}
	if err := s.reconciler.Apply(ctx); err != nil {
		return fromRecord(updated), fmt.Errorf("listener saved but dataplane apply failed: %w", err)
	}
	return fromRecord(updated), nil
}

func (s *Service) Delete(ctx context.Context, id string, version int) error {
	if version < 1 {
		return fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	if err := s.repository.DeleteListener(ctx, id, version); err != nil {
		return mapStoreError(err)
	}
	if err := s.reconciler.Apply(ctx); err != nil {
		return fmt.Errorf("listener deleted but dataplane apply failed: %w", err)
	}
	return nil
}

type normalizedListener struct {
	Name                string
	Kind                string
	BindAddress         string
	Port                int
	ProxyGroupID        string
	AuthMode            string
	AuthConfigEncrypted []byte
	TransportJSON       string
	PublicEndpointJSON  string
	Enabled             bool
}

func (s *Service) normalize(
	ctx context.Context,
	id string,
	name string,
	kind string,
	bindAddress string,
	port int,
	groupID string,
	auth *Auth,
	existingAuth []byte,
	transport Transport,
	publicEndpoint PublicEndpoint,
	enabled bool,
) (normalizedListener, error) {
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 128 {
		return normalizedListener{}, fmt.Errorf("%w: name must contain 1 to 128 characters", ErrInvalid)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if !supportedKind(kind) {
		return normalizedListener{}, fmt.Errorf("%w: kind must be http, socks, mixed, vless, vmess, or trojan", ErrInvalid)
	}
	bindAddress = strings.TrimSpace(bindAddress)
	if bindAddress == "" {
		bindAddress = "127.0.0.1"
	}
	ip := net.ParseIP(bindAddress)
	if ip == nil {
		return normalizedListener{}, fmt.Errorf("%w: bind_address must be an explicit IP", ErrInvalid)
	}
	if port < 1 || port > 65535 {
		return normalizedListener{}, fmt.Errorf("%w: port must be between 1 and 65535", ErrInvalid)
	}
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return normalizedListener{}, fmt.Errorf("%w: proxy_group_id is required", ErrInvalid)
	}
	group, err := s.repository.GetProxyGroup(ctx, groupID)
	if errors.Is(err, store.ErrNotFound) {
		return normalizedListener{}, fmt.Errorf("%w: proxy group does not exist", ErrInvalid)
	}
	if err != nil {
		return normalizedListener{}, err
	}
	if !group.Enabled {
		return normalizedListener{}, fmt.Errorf("%w: proxy group is disabled", ErrInvalid)
	}

	authMode := "none"
	authEncrypted := append([]byte(nil), existingAuth...)
	if auth != nil {
		auth.Username = strings.TrimSpace(auth.Username)
		if auth.Username == "" || auth.Password == "" {
			return normalizedListener{}, fmt.Errorf("%w: username and password must both be set", ErrInvalid)
		}
		if len(auth.Username) > 128 || len(auth.Password) > 512 {
			return normalizedListener{}, fmt.Errorf("%w: listener credentials are too long", ErrInvalid)
		}
		if (kind == "vless" || kind == "vmess") && !validUUID(auth.Password) {
			return normalizedListener{}, fmt.Errorf("%w: %s credential must be a UUID", ErrInvalid, kind)
		}
		encoded, err := json.Marshal(auth)
		if err != nil {
			return normalizedListener{}, fmt.Errorf("encode listener auth: %w", err)
		}
		authEncrypted, err = s.cipher.Seal(encoded, associatedData(id))
		if err != nil {
			return normalizedListener{}, fmt.Errorf("encrypt listener auth: %w", err)
		}
	}
	if len(authEncrypted) > 0 {
		authMode = "userpass"
	}
	advanced := isAdvancedKind(kind)
	if advanced && !ip.IsLoopback() {
		return normalizedListener{}, fmt.Errorf("%w: WebSocket protocol listeners must bind to a loopback address behind a reverse proxy", ErrInvalid)
	}
	if advanced && authMode == "none" {
		return normalizedListener{}, fmt.Errorf("%w: %s listeners require credentials", ErrInvalid, kind)
	}
	if !ip.IsLoopback() && authMode == "none" {
		return normalizedListener{}, fmt.Errorf("%w: non-loopback listeners require username/password authentication", ErrInvalid)
	}
	transportJSON, publicEndpointJSON, err := normalizeEndpointConfig(advanced, transport, publicEndpoint, port)
	if err != nil {
		return normalizedListener{}, err
	}
	return normalizedListener{
		Name:                name,
		Kind:                kind,
		BindAddress:         ip.String(),
		Port:                port,
		ProxyGroupID:        groupID,
		AuthMode:            authMode,
		AuthConfigEncrypted: authEncrypted,
		TransportJSON:       transportJSON,
		PublicEndpointJSON:  publicEndpointJSON,
		Enabled:             enabled,
	}, nil
}

func fromRecord(record store.ListenerRecord) Listener {
	sharePath := ""
	if record.ShareToken != "" {
		sharePath = "/sub/" + record.ShareToken
	}
	var transport Transport
	var publicEndpoint PublicEndpoint
	_ = json.Unmarshal([]byte(record.TransportJSON), &transport)
	_ = json.Unmarshal([]byte(record.PublicEndpointJSON), &publicEndpoint)
	if isAdvancedKind(record.Kind) {
		if normalizedPath, err := NormalizeWebSocketPath(transport.WSPath); err == nil {
			transport.WSPath = normalizedPath
		}
	}
	return Listener{
		ID:             record.ID,
		Name:           record.Name,
		Kind:           record.Kind,
		BindAddress:    record.BindAddress,
		Port:           record.Port,
		ProxyGroupID:   record.ProxyGroupID,
		AuthConfigured: record.AuthMode != "none" && len(record.AuthConfigEncrypted) > 0,
		Transport:      transport,
		PublicEndpoint: publicEndpoint,
		SharePath:      sharePath,
		Enabled:        record.Enabled,
		Version:        record.Version,
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
	}
}

func supportedKind(kind string) bool {
	switch kind {
	case "http", "socks", "mixed", "vless", "vmess", "trojan":
		return true
	default:
		return false
	}
}

func isAdvancedKind(kind string) bool {
	return kind == "vless" || kind == "vmess" || kind == "trojan"
}

func normalizeEndpointConfig(advanced bool, transport Transport, endpoint PublicEndpoint, listenerPort int) (string, string, error) {
	if !advanced && strings.TrimSpace(endpoint.Host) == "" {
		return "{}", "{}", nil
	}
	if !advanced {
		endpoint.Host = strings.ToLower(strings.TrimSpace(endpoint.Host))
		if !validEndpointHost(endpoint.Host) {
			return "", "", fmt.Errorf("%w: public_endpoint.host must be a public IP address or domain name", ErrInvalid)
		}
		if endpoint.Port == 0 {
			endpoint.Port = listenerPort
		}
		if endpoint.Port < 1 || endpoint.Port > 65535 {
			return "", "", fmt.Errorf("%w: public_endpoint.port must be between 1 and 65535", ErrInvalid)
		}
		encoded, err := json.Marshal(endpoint)
		if err != nil {
			return "", "", fmt.Errorf("encode listener public endpoint: %w", err)
		}
		return "{}", string(encoded), nil
	}
	transport.Type = strings.ToLower(strings.TrimSpace(transport.Type))
	if transport.Type == "" {
		transport.Type = "ws"
	}
	if transport.Type != "ws" {
		return "", "", fmt.Errorf("%w: WebSocket transport only supports ws", ErrInvalid)
	}
	normalizedPath, err := NormalizeWebSocketPath(transport.WSPath)
	if err != nil {
		return "", "", err
	}
	transport.WSPath = normalizedPath
	endpoint.Host = strings.ToLower(strings.TrimSpace(endpoint.Host))
	if !validPublicHost(endpoint.Host) {
		return "", "", fmt.Errorf("%w: public_endpoint.host must be a valid domain name", ErrInvalid)
	}
	if endpoint.Port == 0 {
		endpoint.Port = 443
	}
	if endpoint.Port < 1 || endpoint.Port > 65535 || !endpoint.TLS {
		return "", "", fmt.Errorf("%w: WebSocket public endpoint must use TLS and a valid port", ErrInvalid)
	}
	transportEncoded, err := json.Marshal(transport)
	if err != nil {
		return "", "", fmt.Errorf("encode listener transport: %w", err)
	}
	endpointEncoded, err := json.Marshal(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("encode listener public endpoint: %w", err)
	}
	return string(transportEncoded), string(endpointEncoded), nil
}

// ValidatePublicEndpoint validates the endpoint metadata used by a standard
// HTTP, SOCKS5, or Mixed listener without changing any stored state.
func ValidatePublicEndpoint(endpoint PublicEndpoint, listenerPort int) error {
	_, _, err := normalizeEndpointConfig(false, Transport{}, endpoint, listenerPort)
	return err
}

func validEndpointHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsUnspecified()
	}
	return validPublicHost(host)
}

// NormalizeWebSocketPath maps user-facing paths into the reserved edge
// namespace. Existing paths from older releases are upgraded lazily, so a
// database upgrade does not invalidate a listener before its next edit.
func NormalizeWebSocketPath(value string) (string, error) {
	raw := strings.TrimSpace(value)
	if raw == "" || !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("%w: ws_path must begin with /", ErrInvalid)
	}
	if raw == strings.TrimSuffix(WebSocketPathPrefix, "/") || raw == WebSocketPathPrefix {
		return "", fmt.Errorf("%w: ws_path must contain a route after %s", ErrInvalid, WebSocketPathPrefix)
	}
	if strings.ContainsAny(raw, "?#\\") {
		return "", fmt.Errorf("%w: ws_path must be a path without query, fragment, or backslash", ErrInvalid)
	}
	normalized := raw
	if !strings.HasPrefix(normalized, WebSocketPathPrefix) {
		normalized = WebSocketPathPrefix + strings.TrimPrefix(normalized, "/")
	}
	if len(normalized) > 256 || !validNormalizedWebSocketPath(normalized) {
		return "", fmt.Errorf("%w: ws_path must use the %s prefix and contain only safe path segments", ErrInvalid, WebSocketPathPrefix)
	}
	return normalized, nil
}

func validNormalizedWebSocketPath(value string) bool {
	if !strings.HasPrefix(value, WebSocketPathPrefix) {
		return false
	}
	suffix := strings.TrimPrefix(value, WebSocketPathPrefix)
	if suffix == "" || strings.Contains(suffix, "//") || strings.HasSuffix(suffix, "/") {
		return false
	}
	for _, segment := range strings.Split(suffix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') &&
				character != '-' && character != '_' && character != '.' && character != '~' {
				return false
			}
		}
	}
	return true
}

func validPublicHost(host string) bool {
	if len(host) < 3 || len(host) > 253 || strings.ContainsAny(host, "/:@?# ") || !strings.Contains(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range strings.ToLower(value) {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func associatedData(id string) []byte {
	return []byte("listener:" + id)
}

func mapStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrConflict):
		return ErrConflict
	default:
		return err
	}
}

func newID() (string, error) {
	var buffer [12]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("generate listener id: %w", err)
	}
	return "listener-" + hex.EncodeToString(buffer[:]), nil
}
