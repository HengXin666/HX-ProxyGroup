package residential

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

var (
	ErrNotFound       = errors.New("residential resource not found")
	ErrConflict       = errors.New("residential resource conflict")
	ErrInvalid        = errors.New("invalid residential configuration")
	ErrRateLimited    = errors.New("rotation rate limit exceeded")
	ErrSessionExpired = errors.New("residential client session expired")
)

// Provider is the administrator-facing view of a vendor account. Credentials are
// deliberately absent: the API never echoes them back.
type Provider struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Vendor               string `json:"vendor"`
	Protocol             string `json:"protocol"`
	GatewayHost          string `json:"gateway_host"`
	GatewayPort          int    `json:"gateway_port"`
	UpstreamProxyGroupID string `json:"upstream_proxy_group_id,omitempty"`
	// APIURL is the vendor extraction endpoint for api-list rotation. It is
	// write-only at the HTTP API boundary because BestProxy extraction URLs may
	// contain an app_key. The service still uses it internally.
	APIURL                string `json:"-"`
	APIURLConfigured      bool   `json:"api_url_configured"`
	APIProxyURL           string `json:"-"`
	APIProxyConfigured    bool   `json:"api_proxy_configured"`
	UsernameTemplate      string `json:"username_template"`
	RotationMode          string `json:"rotation_mode"`
	SessionTTLSeconds     int    `json:"session_ttl_seconds"`
	MaxConcurrentSessions int    `json:"max_concurrent_sessions"`
	SessionExpiryPolicy   string `json:"session_expiry_policy"`
	PoolSize              int    `json:"-"` // source compatibility for internal callers
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
	Name                  string       `json:"name"`
	Vendor                string       `json:"vendor"`
	Protocol              string       `json:"protocol"`
	GatewayHost           string       `json:"gateway_host"`
	GatewayPort           int          `json:"gateway_port"`
	UpstreamProxyGroupID  string       `json:"upstream_proxy_group_id,omitempty"`
	APIURL                string       `json:"api_url,omitempty"`
	APIProxyURL           string       `json:"api_proxy_url,omitempty"`
	Credentials           *Credentials `json:"credentials,omitempty"`
	UsernameTemplate      string       `json:"username_template"`
	RotationMode          string       `json:"rotation_mode"`
	SessionTTLSeconds     int          `json:"session_ttl_seconds,omitempty"`
	MaxConcurrentSessions int          `json:"max_concurrent_sessions,omitempty"`
	PoolSize              int          `json:"pool_size,omitempty"` // pre-v19 compatibility
	SessionExpiryPolicy   string       `json:"session_expiry_policy,omitempty"`
	DefaultRegion         string       `json:"default_region,omitempty"`
	Enabled               *bool        `json:"enabled,omitempty"`
}

type UpdateProviderRequest struct {
	Version               int          `json:"version"`
	Name                  string       `json:"name"`
	Vendor                string       `json:"vendor"`
	Protocol              string       `json:"protocol"`
	GatewayHost           string       `json:"gateway_host"`
	GatewayPort           int          `json:"gateway_port"`
	UpstreamProxyGroupID  string       `json:"upstream_proxy_group_id,omitempty"`
	APIURL                string       `json:"api_url,omitempty"`
	APIProxyURL           string       `json:"api_proxy_url,omitempty"`
	Credentials           *Credentials `json:"credentials,omitempty"`
	UsernameTemplate      string       `json:"username_template"`
	RotationMode          string       `json:"rotation_mode"`
	SessionTTLSeconds     int          `json:"session_ttl_seconds,omitempty"`
	MaxConcurrentSessions int          `json:"max_concurrent_sessions,omitempty"`
	PoolSize              int          `json:"pool_size,omitempty"` // pre-v19 compatibility
	SessionExpiryPolicy   string       `json:"session_expiry_policy,omitempty"`
	DefaultRegion         string       `json:"default_region,omitempty"`
	Enabled               bool         `json:"enabled"`
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
	id, err := newID("residential-provider")
	if err != nil {
		return Provider{}, err
	}
	normalized, err := s.normalizeProvider(id, providerInput{
		Name:                  request.Name,
		Vendor:                request.Vendor,
		Protocol:              request.Protocol,
		GatewayHost:           request.GatewayHost,
		GatewayPort:           request.GatewayPort,
		UpstreamProxyGroupID:  request.UpstreamProxyGroupID,
		APIProxyURL:           request.APIProxyURL,
		APIURL:                request.APIURL,
		Credentials:           request.Credentials,
		UsernameTemplate:      request.UsernameTemplate,
		RotationMode:          request.RotationMode,
		SessionTTLSeconds:     request.SessionTTLSeconds,
		MaxConcurrentSessions: request.MaxConcurrentSessions,
		PoolSize:              request.PoolSize,
		SessionExpiryPolicy:   request.SessionExpiryPolicy,
		DefaultRegion:         request.DefaultRegion,
		Enabled:               request.Enabled == nil || *request.Enabled,
	}, nil)
	if err != nil {
		return Provider{}, err
	}
	if err := s.validateUpstreamProxyGroup(ctx, normalized.UpstreamProxyGroupID); err != nil {
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
		UpstreamProxyGroupID: normalized.UpstreamProxyGroupID,
		CredentialsEncrypted: normalized.CredentialsEncrypted,
		UsernameTemplate:     normalized.UsernameTemplate,
		RotationMode:         normalized.RotationMode,
		SessionTTLSeconds:    normalized.SessionTTLSeconds,
		PoolSize:             normalized.MaxConcurrentSessions,
		SessionExpiryPolicy:  normalized.SessionExpiryPolicy,
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
		Name:                  request.Name,
		Vendor:                request.Vendor,
		Protocol:              request.Protocol,
		GatewayHost:           request.GatewayHost,
		GatewayPort:           request.GatewayPort,
		UpstreamProxyGroupID:  request.UpstreamProxyGroupID,
		APIProxyURL:           request.APIProxyURL,
		APIURL:                request.APIURL,
		Credentials:           request.Credentials,
		UsernameTemplate:      request.UsernameTemplate,
		RotationMode:          request.RotationMode,
		SessionTTLSeconds:     request.SessionTTLSeconds,
		MaxConcurrentSessions: request.MaxConcurrentSessions,
		PoolSize:              request.PoolSize,
		SessionExpiryPolicy:   request.SessionExpiryPolicy,
		DefaultRegion:         request.DefaultRegion,
		Enabled:               request.Enabled,
		ExistingAPIURL:        existing.APIURL,
	}, existing.CredentialsEncrypted)
	if err != nil {
		return Provider{}, err
	}
	if err := s.validateUpstreamProxyGroup(ctx, normalized.UpstreamProxyGroupID); err != nil {
		return Provider{}, err
	}
	existing.Name = normalized.Name
	existing.Vendor = normalized.Vendor
	existing.Protocol = normalized.Protocol
	existing.GatewayHost = normalized.GatewayHost
	existing.GatewayPort = normalized.GatewayPort
	existing.UpstreamProxyGroupID = normalized.UpstreamProxyGroupID
	// API extraction URLs are now held in the encrypted provider secret. This
	// also clears the v14 plaintext compatibility column after an edit.
	existing.APIURL = ""
	existing.CredentialsEncrypted = normalized.CredentialsEncrypted
	existing.UsernameTemplate = normalized.UsernameTemplate
	existing.RotationMode = normalized.RotationMode
	existing.SessionTTLSeconds = normalized.SessionTTLSeconds
	existing.PoolSize = normalized.MaxConcurrentSessions
	existing.SessionExpiryPolicy = normalized.SessionExpiryPolicy
	existing.DefaultRegion = normalized.DefaultRegion
	existing.Enabled = normalized.Enabled
	existing.UpdatedAt = s.now().UTC()
	updated, err := s.repository.UpdateResidentialProvider(ctx, existing, request.Version)
	if err != nil {
		return Provider{}, mapStoreError(err)
	}
	// Existing client sessions keep the credentials and endpoint with which
	// they were allocated. New settings take effect on the next allocation or
	// expiry-driven rotation instead of replacing live sessions in bulk.
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
	Name                  string
	Vendor                string
	Protocol              string
	GatewayHost           string
	GatewayPort           int
	UpstreamProxyGroupID  string
	APIURL                string
	APIProxyURL           string
	Credentials           *Credentials
	UsernameTemplate      string
	RotationMode          string
	SessionTTLSeconds     int
	MaxConcurrentSessions int
	PoolSize              int
	SessionExpiryPolicy   string
	DefaultRegion         string
	Enabled               bool
	ExistingAPIURL        string
}

type normalizedProvider struct {
	Name                  string
	Vendor                string
	Protocol              string
	GatewayHost           string
	GatewayPort           int
	UpstreamProxyGroupID  string
	CredentialsEncrypted  []byte
	UsernameTemplate      string
	RotationMode          string
	SessionTTLSeconds     int
	MaxConcurrentSessions int
	SessionExpiryPolicy   string
	DefaultRegion         string
	Enabled               bool
}

// validateUpstreamProxyGroup keeps the stable database id in the provider
// record while allowing operators to rename a group without breaking the
// residential chain. The compiler resolves the id to the current Mihomo name.
func (s *Service) validateUpstreamProxyGroup(ctx context.Context, groupID string) error {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" {
		return nil
	}
	group, err := s.repository.GetProxyGroup(ctx, groupID)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: upstream proxy group %q does not exist", ErrInvalid, groupID)
	}
	if err != nil {
		return fmt.Errorf("validate upstream proxy group: %w", err)
	}
	if !group.Enabled {
		return fmt.Errorf("%w: upstream proxy group %q is disabled", ErrInvalid, group.Name)
	}
	return nil
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
	template := strings.TrimSpace(input.UsernameTemplate)
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
	existingSecrets := providerSecrets{}
	if len(existingCredentials) > 0 {
		decoded, err := s.openProviderSecrets(id, existingCredentials)
		if err != nil {
			// A new credential pair or API URL replaces the old secret, so a
			// corrupt legacy value should not prevent recovery through edit.
			if input.Credentials == nil && strings.TrimSpace(input.APIURL) == "" && strings.TrimSpace(input.APIProxyURL) == "" {
				return normalizedProvider{}, err
			}
		} else {
			existingSecrets = decoded
		}
	}
	apiProxyURL := strings.TrimSpace(input.APIProxyURL)
	if apiProxyURL == "" {
		apiProxyURL = strings.TrimSpace(existingSecrets.APIProxyURL)
	}
	if apiProxyURL != "" {
		validatedProxyURL, err := validateAPIProxyURL(apiProxyURL)
		if err != nil {
			return normalizedProvider{}, err
		}
		apiProxyURL = validatedProxyURL
	}
	if rotationMode == RotationAPIList {
		// api-list providers get their endpoints from the extraction API, so
		// there is no gateway login and no username template to validate. The
		// gateway fields hold a placeholder that never reaches the data plane.
		host = apiListGatewayPlaceholder
		input.GatewayPort = 1
		template = ""
		apiURL := strings.TrimSpace(input.APIURL)
		if apiURL == "" {
			apiURL = strings.TrimSpace(existingSecrets.APIURL)
		}
		if apiURL == "" {
			// v14 stored the URL in a plaintext compatibility column. An edit
			// transparently upgrades it into the encrypted provider secret.
			apiURL = strings.TrimSpace(input.ExistingAPIURL)
		}
		apiURL, err := validateAPIURL(apiURL)
		if err != nil {
			return normalizedProvider{}, err
		}
		input.APIURL = apiURL
	} else {
		input.APIURL = ""
		if err := validateGatewayHost(host); err != nil {
			return normalizedProvider{}, err
		}
		if input.GatewayPort < 1 || input.GatewayPort > 65535 {
			return normalizedProvider{}, fmt.Errorf("%w: gateway_port must be between 1 and 65535", ErrInvalid)
		}
		if err := ValidateTemplate(template); err != nil {
			return normalizedProvider{}, fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if rotationMode == RotationSessionTemplate && !TemplateUsesSession(template) {
			return normalizedProvider{}, fmt.Errorf(
				"%w: rotation_mode %q requires the username template to reference {session}",
				ErrInvalid,
				RotationSessionTemplate,
			)
		}
	}
	ttl := input.SessionTTLSeconds
	if ttl == 0 {
		ttl = 600
		if vendor == "bestproxy" {
			// BestProxy's `life` parameter is measured in minutes, unlike the
			// generic provider field name, and its documented range is short.
			ttl = 60
		}
	}
	if ttl < 0 || ttl > 86400 {
		return normalizedProvider{}, fmt.Errorf("%w: session_ttl_seconds must be between 0 and 86400", ErrInvalid)
	}
	if vendor == "bestproxy" && strings.Contains(template, "_life-") && (ttl < 1 || ttl > 120) {
		return normalizedProvider{}, fmt.Errorf("%w: BestProxy life must be between 1 and 120 minutes", ErrInvalid)
	}
	maxSessions := input.MaxConcurrentSessions
	if maxSessions == 0 {
		maxSessions = input.PoolSize
	}
	if maxSessions == 0 {
		maxSessions = 64
	}
	if maxSessions < 1 || maxSessions > 1024 {
		return normalizedProvider{}, fmt.Errorf("%w: max_concurrent_sessions must be between 1 and 1024", ErrInvalid)
	}
	if rotationMode == RotationPerRequest {
		maxSessions = 1
	}
	expiryPolicy := strings.ToLower(strings.TrimSpace(input.SessionExpiryPolicy))
	if expiryPolicy == "" {
		expiryPolicy = "rotate"
	}
	if expiryPolicy != "expire" && expiryPolicy != "rotate" {
		return normalizedProvider{}, fmt.Errorf("%w: session_expiry_policy must be expire or rotate", ErrInvalid)
	}
	region := strings.TrimSpace(input.DefaultRegion)
	if err := validateRegion(region); err != nil {
		return normalizedProvider{}, err
	}
	if vendor == "bestproxy" && strings.Contains(template, "{region}") && region == "" {
		return normalizedProvider{}, fmt.Errorf("%w: BestProxy providers using {region} require a default_region", ErrInvalid)
	}

	secrets := existingSecrets
	if rotationMode == RotationAPIList {
		secrets = providerSecrets{APIURL: input.APIURL, APIProxyURL: apiProxyURL}
	} else if input.Credentials != nil {
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
		secrets = providerSecrets{Username: username, Password: password, APIProxyURL: apiProxyURL}
	} else if rotationMode != RotationAPIList {
		secrets.APIURL = ""
		secrets.APIProxyURL = apiProxyURL
	}
	if rotationMode != RotationAPIList && (secrets.Username == "" || secrets.Password == "") {
		return normalizedProvider{}, fmt.Errorf("%w: gateway credentials are required", ErrInvalid)
	}
	encoded, err := json.Marshal(secrets)
	if err != nil {
		return normalizedProvider{}, fmt.Errorf("encode residential provider secrets: %w", err)
	}
	sealed, err := s.cipher.Seal(encoded, providerAssociatedData(id))
	if err != nil {
		return normalizedProvider{}, fmt.Errorf("encrypt residential provider secrets: %w", err)
	}

	return normalizedProvider{
		Name:                  name,
		Vendor:                vendor,
		Protocol:              protocol,
		GatewayHost:           host,
		GatewayPort:           input.GatewayPort,
		UpstreamProxyGroupID:  strings.TrimSpace(input.UpstreamProxyGroupID),
		CredentialsEncrypted:  sealed,
		UsernameTemplate:      template,
		RotationMode:          rotationMode,
		SessionTTLSeconds:     ttl,
		MaxConcurrentSessions: maxSessions,
		SessionExpiryPolicy:   expiryPolicy,
		DefaultRegion:         region,
		Enabled:               input.Enabled,
	}, nil
}

// apiListGatewayPlaceholder fills the gateway columns of api-list providers.
// Their real endpoints come from the extraction API, so the placeholder never
// reaches the data plane; it only satisfies the schema's NOT NULL constraints.
const apiListGatewayPlaceholder = "api-list.invalid"

// validateAPIURL checks an api-list extraction endpoint before it is stored.
// The control plane fetches this URL, so it must be HTTPS and resolve to a
// public host; private and loopback targets would otherwise turn the provider
// save into an SSRF primitive.
func validateAPIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: api_url is required for api-list rotation", ErrInvalid)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: api_url must be an https URL", ErrInvalid)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%w: api_url must not embed credentials", ErrInvalid)
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && ip.IsPrivate() {
		return "", fmt.Errorf("%w: api_url must point at a public address", ErrInvalid)
	}
	if err := validateGatewayHost(parsed.Hostname()); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return raw, nil
}

// validateAPIProxyURL accepts the proxy protocols supported by the control
// plane. Unlike api_url, loopback/private hosts are allowed because a local
// Clash/Mihomo listener such as 127.0.0.1:7890 is a normal deployment.
func validateAPIProxyURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Fragment != "" || parsed.RawQuery != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("%w: api_proxy_url must be an HTTP, HTTPS, or SOCKS5 proxy URL", ErrInvalid)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5":
	default:
		return "", fmt.Errorf("%w: api_proxy_url must use http, https, or socks5", ErrInvalid)
	}
	if parsed.Hostname() == "" || parsed.Port() == "" {
		return "", fmt.Errorf("%w: api_proxy_url must include a host and port", ErrInvalid)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("%w: api_proxy_url port is invalid", ErrInvalid)
	}
	return raw, nil
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
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return fmt.Errorf("%w: region may only contain letters, digits, '-' and '_'", ErrInvalid)
		}
	}
	return nil
}

func (s *Service) providerFromRecord(record store.ResidentialProviderRecord) Provider {
	secrets, secretsErr := s.openProviderSecrets(record.ID, record.CredentialsEncrypted)
	apiURL := secrets.APIURL
	if apiURL == "" {
		// Read v14 records created before API URLs were moved into the encrypted
		// secret envelope. Saving the provider upgrades this compatibility value.
		apiURL = record.APIURL
	}
	provider := Provider{
		ID:                    record.ID,
		Name:                  record.Name,
		Vendor:                record.Vendor,
		Protocol:              record.Protocol,
		GatewayHost:           record.GatewayHost,
		GatewayPort:           record.GatewayPort,
		UpstreamProxyGroupID:  record.UpstreamProxyGroupID,
		APIURL:                apiURL,
		APIURLConfigured:      record.RotationMode == RotationAPIList && apiURL != "",
		APIProxyURL:           secrets.APIProxyURL,
		APIProxyConfigured:    secrets.APIProxyURL != "",
		UsernameTemplate:      record.UsernameTemplate,
		RotationMode:          record.RotationMode,
		SessionTTLSeconds:     record.SessionTTLSeconds,
		MaxConcurrentSessions: record.PoolSize,
		PoolSize:              record.PoolSize,
		SessionExpiryPolicy:   record.SessionExpiryPolicy,
		DefaultRegion:         record.DefaultRegion,
		CredentialsConfigured: record.RotationMode != RotationAPIList && secretsErr == nil &&
			secrets.Username != "" && secrets.Password != "",
		SupportsSticky: (record.RotationMode == RotationSessionTemplate && TemplateUsesSession(record.UsernameTemplate)) || record.RotationMode == RotationAPIList,
		Enabled:        record.Enabled,
		Version:        record.Version,
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
	}
	// The account login is safe to display; the password never leaves the box.
	if record.RotationMode != RotationAPIList && secretsErr == nil {
		provider.GatewayUsername = secrets.Username
	}
	return provider
}

type providerSecrets struct {
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	APIURL      string `json:"api_url,omitempty"`
	APIProxyURL string `json:"api_proxy_url,omitempty"`
}

func (s *Service) openProviderSecrets(id string, encrypted []byte) (providerSecrets, error) {
	if len(encrypted) == 0 {
		return providerSecrets{}, nil
	}
	plaintext, err := s.cipher.Open(encrypted, providerAssociatedData(id))
	if err != nil {
		return providerSecrets{}, fmt.Errorf("decrypt residential provider secrets: %w", err)
	}
	var secrets providerSecrets
	if err := json.Unmarshal(plaintext, &secrets); err != nil {
		return providerSecrets{}, fmt.Errorf("decode residential provider secrets: %w", err)
	}
	return secrets, nil
}

func (s *Service) openCredentials(record store.ResidentialProviderRecord) (Credentials, error) {
	secrets, err := s.openProviderSecrets(record.ID, record.CredentialsEncrypted)
	if err != nil {
		return Credentials{}, err
	}
	return Credentials{Username: secrets.Username, Password: secrets.Password}, nil
}

// providerCredentials returns the gateway credentials for a provider. api-list
// providers authenticate by extraction URL and carry no gateway login, so they
// always yield an empty credential pair.
func (s *Service) providerCredentials(record store.ResidentialProviderRecord) (Credentials, error) {
	if record.RotationMode == RotationAPIList {
		return Credentials{}, nil
	}
	return s.openCredentials(record)
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
