package residential

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
	ErrNotFound    = errors.New("residential resource not found")
	ErrConflict    = errors.New("residential resource conflict")
	ErrInvalid     = errors.New("invalid residential configuration")
	ErrRateLimited = errors.New("rotation rate limit exceeded")
)

// Provider is the administrator-facing view of a vendor account. Credentials are
// deliberately absent: the API never echoes them back.
type Provider struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Vendor                string `json:"vendor"`
	Protocol              string `json:"protocol"`
	GatewayHost           string `json:"gateway_host"`
	GatewayPort           int    `json:"gateway_port"`
	UsernameTemplate      string `json:"username_template"`
	RotationMode          string `json:"rotation_mode"`
	SessionTTLSeconds     int    `json:"session_ttl_seconds"`
	PoolSize              int    `json:"pool_size"`
	DefaultRegion         string `json:"default_region,omitempty"`
	CredentialsConfigured bool   `json:"credentials_configured"`
	// GatewayUsername is the account login without any session parameters. It
	// is shown so an operator can confirm which account is in use; the password
	// is never returned.
	GatewayUsername string    `json:"gateway_username,omitempty"`
	SupportsSticky  bool      `json:"supports_sticky"`
	Enabled         bool      `json:"enabled"`
	Version         int       `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type CreateProviderRequest struct {
	Name              string       `json:"name"`
	Vendor            string       `json:"vendor"`
	Protocol          string       `json:"protocol"`
	GatewayHost       string       `json:"gateway_host"`
	GatewayPort       int          `json:"gateway_port"`
	Credentials       *Credentials `json:"credentials,omitempty"`
	UsernameTemplate  string       `json:"username_template"`
	RotationMode      string       `json:"rotation_mode"`
	SessionTTLSeconds int          `json:"session_ttl_seconds,omitempty"`
	PoolSize          int          `json:"pool_size,omitempty"`
	DefaultRegion     string       `json:"default_region,omitempty"`
	Enabled           *bool        `json:"enabled,omitempty"`
}

type UpdateProviderRequest struct {
	Version           int          `json:"version"`
	Name              string       `json:"name"`
	Vendor            string       `json:"vendor"`
	Protocol          string       `json:"protocol"`
	GatewayHost       string       `json:"gateway_host"`
	GatewayPort       int          `json:"gateway_port"`
	Credentials       *Credentials `json:"credentials,omitempty"`
	UsernameTemplate  string       `json:"username_template"`
	RotationMode      string       `json:"rotation_mode"`
	SessionTTLSeconds int          `json:"session_ttl_seconds,omitempty"`
	PoolSize          int          `json:"pool_size,omitempty"`
	DefaultRegion     string       `json:"default_region,omitempty"`
	Enabled           bool         `json:"enabled"`
}

func (s *Service) ListProviders(ctx context.Context) ([]Provider, error) {
	records, err := s.repository.ListResidentialProviders(ctx)
	if err != nil {
		return nil, err
	}
	providers := make([]Provider, 0, len(records))
	for _, record := range records {
		providers = append(providers, s.providerFromRecord(record))
	}
	return providers, nil
}

func (s *Service) GetProvider(ctx context.Context, id string) (Provider, error) {
	record, err := s.repository.GetResidentialProvider(ctx, id)
	if err != nil {
		return Provider{}, mapStoreError(err)
	}
	return s.providerFromRecord(record), nil
}

func (s *Service) CreateProvider(ctx context.Context, request CreateProviderRequest) (Provider, error) {
	if request.Credentials == nil {
		return Provider{}, fmt.Errorf("%w: gateway credentials are required", ErrInvalid)
	}
	id, err := newID("residential-provider")
	if err != nil {
		return Provider{}, err
	}
	normalized, err := s.normalizeProvider(id, providerInput{
		Name:              request.Name,
		Vendor:            request.Vendor,
		Protocol:          request.Protocol,
		GatewayHost:       request.GatewayHost,
		GatewayPort:       request.GatewayPort,
		Credentials:       request.Credentials,
		UsernameTemplate:  request.UsernameTemplate,
		RotationMode:      request.RotationMode,
		SessionTTLSeconds: request.SessionTTLSeconds,
		PoolSize:          request.PoolSize,
		DefaultRegion:     request.DefaultRegion,
		Enabled:           request.Enabled == nil || *request.Enabled,
	}, nil)
	if err != nil {
		return Provider{}, err
	}
	now := s.now().UTC()
	created, err := s.repository.CreateResidentialProvider(ctx, store.ResidentialProviderRecord{
		ID:                   id,
		Name:                 normalized.Name,
		Vendor:               normalized.Vendor,
		Protocol:             normalized.Protocol,
		GatewayHost:          normalized.GatewayHost,
		GatewayPort:          normalized.GatewayPort,
		CredentialsEncrypted: normalized.CredentialsEncrypted,
		UsernameTemplate:     normalized.UsernameTemplate,
		RotationMode:         normalized.RotationMode,
		SessionTTLSeconds:    normalized.SessionTTLSeconds,
		PoolSize:             normalized.PoolSize,
		DefaultRegion:        normalized.DefaultRegion,
		Enabled:              normalized.Enabled,
		Version:              1,
		CreatedAt:            now,
		UpdatedAt:            now,
	})
	if err != nil {
		return Provider{}, mapStoreError(err)
	}
	return s.providerFromRecord(created), nil
}

func (s *Service) UpdateProvider(ctx context.Context, id string, request UpdateProviderRequest) (Provider, error) {
	if request.Version < 1 {
		return Provider{}, fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	existing, err := s.repository.GetResidentialProvider(ctx, id)
	if err != nil {
		return Provider{}, mapStoreError(err)
	}
	normalized, err := s.normalizeProvider(id, providerInput{
		Name:              request.Name,
		Vendor:            request.Vendor,
		Protocol:          request.Protocol,
		GatewayHost:       request.GatewayHost,
		GatewayPort:       request.GatewayPort,
		Credentials:       request.Credentials,
		UsernameTemplate:  request.UsernameTemplate,
		RotationMode:      request.RotationMode,
		SessionTTLSeconds: request.SessionTTLSeconds,
		PoolSize:          request.PoolSize,
		DefaultRegion:     request.DefaultRegion,
		Enabled:           request.Enabled,
	}, existing.CredentialsEncrypted)
	if err != nil {
		return Provider{}, err
	}
	existing.Name = normalized.Name
	existing.Vendor = normalized.Vendor
	existing.Protocol = normalized.Protocol
	existing.GatewayHost = normalized.GatewayHost
	existing.GatewayPort = normalized.GatewayPort
	existing.CredentialsEncrypted = normalized.CredentialsEncrypted
	existing.UsernameTemplate = normalized.UsernameTemplate
	existing.RotationMode = normalized.RotationMode
	existing.SessionTTLSeconds = normalized.SessionTTLSeconds
	existing.PoolSize = normalized.PoolSize
	existing.DefaultRegion = normalized.DefaultRegion
	existing.Enabled = normalized.Enabled
	existing.UpdatedAt = s.now().UTC()
	updated, err := s.repository.UpdateResidentialProvider(ctx, existing, request.Version)
	if err != nil {
		return Provider{}, mapStoreError(err)
	}
	// Gateway details changed, so every channel's pooled sessions must be
	// re-rendered against the new account before they are usable again.
	if err := s.refreshChannelsOfProvider(ctx, id); err != nil {
		return s.providerFromRecord(updated), err
	}
	return s.providerFromRecord(updated), nil
}

func (s *Service) DeleteProvider(ctx context.Context, id string, version int) error {
	if version < 1 {
		return fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	if err := s.repository.DeleteResidentialProvider(ctx, id, version); err != nil {
		return mapStoreError(err)
	}
	return nil
}

type providerInput struct {
	Name              string
	Vendor            string
	Protocol          string
	GatewayHost       string
	GatewayPort       int
	Credentials       *Credentials
	UsernameTemplate  string
	RotationMode      string
	SessionTTLSeconds int
	PoolSize          int
	DefaultRegion     string
	Enabled           bool
}

type normalizedProvider struct {
	Name                 string
	Vendor               string
	Protocol             string
	GatewayHost          string
	GatewayPort          int
	CredentialsEncrypted []byte
	UsernameTemplate     string
	RotationMode         string
	SessionTTLSeconds    int
	PoolSize             int
	DefaultRegion        string
	Enabled              bool
}

func (s *Service) normalizeProvider(
	id string,
	input providerInput,
	existingCredentials []byte,
) (normalizedProvider, error) {
	name := strings.TrimSpace(input.Name)
	if len(name) < 1 || len(name) > 128 {
		return normalizedProvider{}, fmt.Errorf("%w: name must contain 1 to 128 characters", ErrInvalid)
	}
	vendor := strings.ToLower(strings.TrimSpace(input.Vendor))
	if vendor == "" {
		vendor = "custom"
	}
	if len(vendor) > 64 {
		return normalizedProvider{}, fmt.Errorf("%w: vendor must contain at most 64 characters", ErrInvalid)
	}
	protocol := strings.ToLower(strings.TrimSpace(input.Protocol))
	if !containsString(SupportedProtocols(), protocol) {
		return normalizedProvider{}, fmt.Errorf(
			"%w: protocol must be one of %s",
			ErrInvalid,
			strings.Join(SupportedProtocols(), ", "),
		)
	}
	host := strings.ToLower(strings.TrimSpace(input.GatewayHost))
	if err := validateGatewayHost(host); err != nil {
		return normalizedProvider{}, err
	}
	if input.GatewayPort < 1 || input.GatewayPort > 65535 {
		return normalizedProvider{}, fmt.Errorf("%w: gateway_port must be between 1 and 65535", ErrInvalid)
	}
	template := strings.TrimSpace(input.UsernameTemplate)
	if err := ValidateTemplate(template); err != nil {
		return normalizedProvider{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	rotationMode := strings.ToLower(strings.TrimSpace(input.RotationMode))
	if rotationMode == "" {
		rotationMode = RotationSessionTemplate
	}
	if !containsString(SupportedRotationModes(), rotationMode) {
		return normalizedProvider{}, fmt.Errorf(
			"%w: rotation_mode must be one of %s",
			ErrInvalid,
			strings.Join(SupportedRotationModes(), ", "),
		)
	}
	if rotationMode == RotationSessionTemplate && !TemplateUsesSession(template) {
		return normalizedProvider{}, fmt.Errorf(
			"%w: rotation_mode %q requires the username template to reference {session}",
			ErrInvalid,
			RotationSessionTemplate,
		)
	}
	if rotationMode == RotationAPIList {
		return normalizedProvider{}, fmt.Errorf(
			"%w: rotation_mode %q is not implemented yet; use %q or %q",
			ErrInvalid,
			RotationAPIList,
			RotationSessionTemplate,
			RotationPerRequest,
		)
	}
	ttl := input.SessionTTLSeconds
	if ttl == 0 {
		ttl = 600
	}
	if ttl < 0 || ttl > 86400 {
		return normalizedProvider{}, fmt.Errorf("%w: session_ttl_seconds must be between 0 and 86400", ErrInvalid)
	}
	poolSize := input.PoolSize
	if poolSize == 0 {
		poolSize = 8
	}
	if poolSize < 1 || poolSize > 64 {
		return normalizedProvider{}, fmt.Errorf("%w: pool_size must be between 1 and 64", ErrInvalid)
	}
	if rotationMode == RotationPerRequest {
		// A gateway that rotates on its own has nothing to pin, so a pool would
		// only multiply identical upstreams.
		poolSize = 1
	}
	region := strings.ToLower(strings.TrimSpace(input.DefaultRegion))
	if err := validateRegion(region); err != nil {
		return normalizedProvider{}, err
	}

	credentials := append([]byte(nil), existingCredentials...)
	if input.Credentials != nil {
		username := strings.TrimSpace(input.Credentials.Username)
		password := input.Credentials.Password
		if username == "" || password == "" {
			return normalizedProvider{}, fmt.Errorf("%w: gateway username and password must both be set", ErrInvalid)
		}
		if len(username) > 128 || len(password) > 512 {
			return normalizedProvider{}, fmt.Errorf("%w: gateway credentials are too long", ErrInvalid)
		}
		// Reject credentials that cannot survive the username framing before
		// they are encrypted, so the failure surfaces at save time.
		if _, err := Render(template, Variables{
			User:    username,
			Session: "0123456789abcdef",
			Region:  region,
			Country: region,
			TTL:     "600",
		}); err != nil {
			return normalizedProvider{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		encoded, err := json.Marshal(Credentials{Username: username, Password: password})
		if err != nil {
			return normalizedProvider{}, fmt.Errorf("encode residential credentials: %w", err)
		}
		credentials, err = s.cipher.Seal(encoded, providerAssociatedData(id))
		if err != nil {
			return normalizedProvider{}, fmt.Errorf("encrypt residential credentials: %w", err)
		}
	}
	if len(credentials) == 0 {
		return normalizedProvider{}, fmt.Errorf("%w: gateway credentials are required", ErrInvalid)
	}

	return normalizedProvider{
		Name:                 name,
		Vendor:               vendor,
		Protocol:             protocol,
		GatewayHost:          host,
		GatewayPort:          input.GatewayPort,
		CredentialsEncrypted: credentials,
		UsernameTemplate:     template,
		RotationMode:         rotationMode,
		SessionTTLSeconds:    ttl,
		PoolSize:             poolSize,
		DefaultRegion:        region,
		Enabled:              input.Enabled,
	}, nil
}

// validateGatewayHost rejects hosts that would point the data plane at the local
// machine. A residential gateway is by definition remote, and allowing loopback
// or link-local targets here would turn provider configuration into an SSRF and
// listener-loop primitive.
func validateGatewayHost(host string) error {
	if host == "" {
		return fmt.Errorf("%w: gateway_host is required", ErrInvalid)
	}
	if len(host) > 253 || strings.ContainsAny(host, "/:@?# \t\r\n") {
		return fmt.Errorf("%w: gateway_host is not a valid hostname", ErrInvalid)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
			return fmt.Errorf("%w: gateway_host must be a public address", ErrInvalid)
		}
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("%w: gateway_host must be a public address", ErrInvalid)
	}
	if !strings.Contains(host, ".") {
		return fmt.Errorf("%w: gateway_host must be a fully qualified domain or IP", ErrInvalid)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("%w: gateway_host is not a valid hostname", ErrInvalid)
		}
		for _, character := range label {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
				return fmt.Errorf("%w: gateway_host is not a valid hostname", ErrInvalid)
			}
		}
	}
	return nil
}

func validateRegion(region string) error {
	if region == "" {
		return nil
	}
	if len(region) > 64 {
		return fmt.Errorf("%w: region must contain at most 64 characters", ErrInvalid)
	}
	for _, character := range region {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return fmt.Errorf("%w: region may only contain letters, digits, '-' and '_'", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) providerFromRecord(record store.ResidentialProviderRecord) Provider {
	provider := Provider{
		ID:                    record.ID,
		Name:                  record.Name,
		Vendor:                record.Vendor,
		Protocol:              record.Protocol,
		GatewayHost:           record.GatewayHost,
		GatewayPort:           record.GatewayPort,
		UsernameTemplate:      record.UsernameTemplate,
		RotationMode:          record.RotationMode,
		SessionTTLSeconds:     record.SessionTTLSeconds,
		PoolSize:              record.PoolSize,
		DefaultRegion:         record.DefaultRegion,
		CredentialsConfigured: len(record.CredentialsEncrypted) > 0,
		SupportsSticky:        record.RotationMode == RotationSessionTemplate && TemplateUsesSession(record.UsernameTemplate),
		Enabled:               record.Enabled,
		Version:               record.Version,
		CreatedAt:             record.CreatedAt,
		UpdatedAt:             record.UpdatedAt,
	}
	// The account login is safe to display; the password never leaves the box.
	if credentials, err := s.openCredentials(record); err == nil {
		provider.GatewayUsername = credentials.Username
	}
	return provider
}

func (s *Service) openCredentials(record store.ResidentialProviderRecord) (Credentials, error) {
	plaintext, err := s.cipher.Open(record.CredentialsEncrypted, providerAssociatedData(record.ID))
	if err != nil {
		return Credentials{}, fmt.Errorf("decrypt residential credentials: %w", err)
	}
	var credentials Credentials
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return Credentials{}, fmt.Errorf("decode residential credentials: %w", err)
	}
	return credentials, nil
}

func providerAssociatedData(id string) []byte {
	return []byte("residential-provider:" + id)
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func newID(prefix string) (string, error) {
	var buffer [12]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(buffer[:]), nil
}

func newToken() (string, error) {
	var buffer [16]byte
	if _, err := rand.Read(buffer[:]); err != nil {
		return "", fmt.Errorf("generate rotate token: %w", err)
	}
	return hex.EncodeToString(buffer[:]), nil
}
