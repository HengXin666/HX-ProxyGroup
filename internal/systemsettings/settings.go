package systemsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

const (
	MetadataKey      = "global_settings"
	legacyQualityKey = "node_quality_settings"
)

var ErrInvalid = errors.New("invalid global settings")

type Reader interface {
	GetMetadata(context.Context, string) (string, error)
}

type Repository interface {
	Reader
	SetMetadata(context.Context, string, string) error
}

type Applier interface {
	Apply(context.Context) error
}

type HealthTarget struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
}

type QualitySettings struct {
	CheckIntervalSeconds int            `json:"check_interval_seconds"`
	TimeoutSeconds       int            `json:"timeout_seconds"`
	BatchSize            int            `json:"batch_size"`
	ProbeConcurrency     int            `json:"probe_concurrency"`
	TestURL              string         `json:"test_url"`
	HealthTargets        []HealthTarget `json:"health_targets"`
}

type DNSSettings struct {
	Enabled           bool     `json:"enabled"`
	IPv6              bool     `json:"ipv6"`
	EnhancedMode      string   `json:"enhanced_mode"`
	DefaultNameserver []string `json:"default_nameserver"`
	Nameserver        []string `json:"nameserver"`
	Fallback          []string `json:"fallback"`
}

type PerformanceSettings struct {
	TCPConcurrent     bool   `json:"tcp_concurrent"`
	UnifiedDelay      bool   `json:"unified_delay"`
	KeepAliveIdle     int    `json:"keep_alive_idle_seconds"`
	KeepAliveInterval int    `json:"keep_alive_interval_seconds"`
	FindProcessMode   string `json:"find_process_mode"`
	LogLevel          string `json:"log_level"`
}

type Settings struct {
	Quality     QualitySettings     `json:"quality"`
	DNS         DNSSettings         `json:"dns"`
	Performance PerformanceSettings `json:"performance"`
}

func Default() Settings {
	return Settings{
		Quality: QualitySettings{
			CheckIntervalSeconds: 600,
			TimeoutSeconds:       8,
			BatchSize:            20,
			ProbeConcurrency:     4,
			TestURL:              "https://www.gstatic.com/generate_204",
			HealthTargets: []HealthTarget{
				{ID: "chatgpt", Name: "ChatGPT", URL: "https://chatgpt.com/", Enabled: false},
				{ID: "claude", Name: "Claude", URL: "https://claude.ai/", Enabled: false},
				{ID: "github", Name: "GitHub", URL: "https://github.com/", Enabled: false},
				{ID: "google", Name: "Google", URL: "https://www.google.com/generate_204", Enabled: false},
				{ID: "telegram", Name: "Telegram", URL: "https://telegram.org/", Enabled: false},
			},
		},
		DNS: DNSSettings{
			Enabled:           true,
			IPv6:              false,
			EnhancedMode:      "normal",
			DefaultNameserver: []string{"223.5.5.5", "119.29.29.29"},
			Nameserver:        []string{"https://223.5.5.5/dns-query", "https://120.53.53.53/dns-query"},
			Fallback:          []string{},
		},
		Performance: PerformanceSettings{
			TCPConcurrent:     true,
			UnifiedDelay:      true,
			KeepAliveIdle:     15,
			KeepAliveInterval: 15,
			FindProcessMode:   "off",
			LogLevel:          "warning",
		},
	}
}

func Load(ctx context.Context, repository Reader) (Settings, error) {
	value, err := repository.GetMetadata(ctx, MetadataKey)
	if errors.Is(err, store.ErrNotFound) {
		settings := Default()
		legacy, legacyErr := repository.GetMetadata(ctx, legacyQualityKey)
		if errors.Is(legacyErr, store.ErrNotFound) {
			return settings, nil
		}
		if legacyErr != nil {
			return Settings{}, legacyErr
		}
		if err := json.Unmarshal([]byte(legacy), &settings.Quality); err != nil {
			return Settings{}, fmt.Errorf("decode legacy quality settings: %w", err)
		}
		if settings.Quality.ProbeConcurrency == 0 {
			settings.Quality.ProbeConcurrency = Default().Quality.ProbeConcurrency
		}
		if settings.Quality.HealthTargets == nil {
			settings.Quality.HealthTargets = Default().Quality.HealthTargets
		}
		if err := Validate(settings); err != nil {
			return Settings{}, err
		}
		return settings, nil
	}
	if err != nil {
		return Settings{}, err
	}
	settings := Default()
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return Settings{}, fmt.Errorf("decode global settings: %w", err)
	}
	if err := Validate(settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func Save(ctx context.Context, repository Repository, settings Settings) error {
	normalize(&settings)
	if err := Validate(settings); err != nil {
		return err
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("encode global settings: %w", err)
	}
	return repository.SetMetadata(ctx, MetadataKey, string(encoded))
}

type Service struct {
	repository Repository
	applier    Applier
}

func NewService(repository Repository, applier Applier) (*Service, error) {
	if repository == nil {
		return nil, errors.New("settings repository is required")
	}
	return &Service{repository: repository, applier: applier}, nil
}

func (s *Service) Get(ctx context.Context) (Settings, error) {
	return Load(ctx, s.repository)
}

func (s *Service) Update(ctx context.Context, settings Settings) (Settings, error) {
	normalize(&settings)
	if err := Validate(settings); err != nil {
		return Settings{}, err
	}
	previous, err := Load(ctx, s.repository)
	if err != nil {
		return Settings{}, err
	}
	if err := Save(ctx, s.repository, settings); err != nil {
		return Settings{}, err
	}
	if s.applier == nil {
		return settings, nil
	}
	if err := s.applier.Apply(ctx); err != nil {
		rollbackErr := Save(ctx, s.repository, previous)
		if rollbackErr == nil {
			rollbackErr = s.applier.Apply(ctx)
		}
		return Settings{}, fmt.Errorf("apply global settings: %w; rollback: %v", err, rollbackErr)
	}
	return settings, nil
}

func Validate(settings Settings) error {
	quality := settings.Quality
	if quality.CheckIntervalSeconds < 60 || quality.CheckIntervalSeconds > 86400 {
		return fmt.Errorf("%w: quality.check_interval_seconds must be between 60 and 86400", ErrInvalid)
	}
	if quality.TimeoutSeconds < 1 || quality.TimeoutSeconds > 30 {
		return fmt.Errorf("%w: quality.timeout_seconds must be between 1 and 30", ErrInvalid)
	}
	if quality.BatchSize < 1 || quality.BatchSize > 200 {
		return fmt.Errorf("%w: quality.batch_size must be between 1 and 200", ErrInvalid)
	}
	if quality.ProbeConcurrency < 1 || quality.ProbeConcurrency > 16 {
		return fmt.Errorf("%w: quality.probe_concurrency must be between 1 and 16", ErrInvalid)
	}
	if err := validatePublicHTTPURL(quality.TestURL); err != nil {
		return fmt.Errorf("%w: quality.test_url: %v", ErrInvalid, err)
	}
	if len(quality.HealthTargets) > 12 {
		return fmt.Errorf("%w: at most 12 health targets are allowed", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(quality.HealthTargets))
	for _, target := range quality.HealthTargets {
		if !validID(target.ID) || len(target.Name) < 1 || len(target.Name) > 40 {
			return fmt.Errorf("%w: health target id or name is invalid", ErrInvalid)
		}
		if _, duplicate := seen[target.ID]; duplicate {
			return fmt.Errorf("%w: duplicate health target id %q", ErrInvalid, target.ID)
		}
		seen[target.ID] = struct{}{}
		if err := validatePublicHTTPURL(target.URL); err != nil {
			return fmt.Errorf("%w: health target %q: %v", ErrInvalid, target.ID, err)
		}
	}
	if settings.DNS.EnhancedMode != "normal" && settings.DNS.EnhancedMode != "fake-ip" && settings.DNS.EnhancedMode != "redir-host" {
		return fmt.Errorf("%w: dns.enhanced_mode is invalid", ErrInvalid)
	}
	if len(settings.DNS.DefaultNameserver) < 1 || len(settings.DNS.DefaultNameserver) > 8 || len(settings.DNS.Nameserver) < 1 || len(settings.DNS.Nameserver) > 8 || len(settings.DNS.Fallback) > 8 {
		return fmt.Errorf("%w: DNS server lists must contain 1-8 primary entries and at most 8 fallback entries", ErrInvalid)
	}
	for _, server := range append(append(append([]string{}, settings.DNS.DefaultNameserver...), settings.DNS.Nameserver...), settings.DNS.Fallback...) {
		if len(server) < 1 || len(server) > 256 || strings.ContainsAny(server, "\r\n") {
			return fmt.Errorf("%w: invalid DNS server entry", ErrInvalid)
		}
	}
	performance := settings.Performance
	if performance.KeepAliveIdle < 0 || performance.KeepAliveIdle > 600 || performance.KeepAliveInterval < 0 || performance.KeepAliveInterval > 600 {
		return fmt.Errorf("%w: keep-alive values must be between 0 and 600 seconds", ErrInvalid)
	}
	if performance.FindProcessMode != "off" && performance.FindProcessMode != "strict" && performance.FindProcessMode != "always" {
		return fmt.Errorf("%w: performance.find_process_mode is invalid", ErrInvalid)
	}
	if performance.LogLevel != "silent" && performance.LogLevel != "error" && performance.LogLevel != "warning" && performance.LogLevel != "info" && performance.LogLevel != "debug" {
		return fmt.Errorf("%w: performance.log_level is invalid", ErrInvalid)
	}
	return nil
}

func normalize(settings *Settings) {
	settings.Quality.TestURL = strings.TrimSpace(settings.Quality.TestURL)
	for index := range settings.Quality.HealthTargets {
		settings.Quality.HealthTargets[index].ID = strings.ToLower(strings.TrimSpace(settings.Quality.HealthTargets[index].ID))
		settings.Quality.HealthTargets[index].Name = strings.TrimSpace(settings.Quality.HealthTargets[index].Name)
		settings.Quality.HealthTargets[index].URL = strings.TrimSpace(settings.Quality.HealthTargets[index].URL)
	}
	trimList(settings.DNS.DefaultNameserver)
	trimList(settings.DNS.Nameserver)
	trimList(settings.DNS.Fallback)
}

func trimList(values []string) {
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func validatePublicHTTPURL(value string) error {
	if len(value) > 512 {
		return errors.New("URL must not exceed 512 characters")
	}
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("URL must be an absolute HTTP(S) URL without credentials")
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return errors.New("URL must not target a private or special-use IP")
	}
	return nil
}
