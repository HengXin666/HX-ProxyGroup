package subscription

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/nodeparse"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

type SourceType string

const (
	SourceRemote SourceType = "remote"
	SourceInline SourceType = "inline"
	SourceFile   SourceType = "file"
)

const (
	defaultRefreshInterval = 3600
	minimumRefreshInterval = 60
	maximumInlineBytes     = 4 << 20
)

var (
	ErrInvalid  = errors.New("invalid subscription")
	ErrNotFound = errors.New("subscription not found")
	ErrConflict = errors.New("subscription version conflict")
)

type SourceConfig struct {
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	UserAgent      string            `json:"user_agent,omitempty"`
	Inline         string            `json:"inline,omitempty"`
	FilePath       string            `json:"file_path,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
	AllowPrivate   bool              `json:"allow_private,omitempty"`
}

type Subscription struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	SourceType             SourceType `json:"source_type"`
	SourceConfigured       bool       `json:"source_configured"`
	Enabled                bool       `json:"enabled"`
	RefreshIntervalSeconds int        `json:"refresh_interval_seconds"`
	LastSuccessSnapshotID  string     `json:"last_success_snapshot_id,omitempty"`
	ConsecutiveFailures    int        `json:"consecutive_failures"`
	LastRefreshAttemptAt   *time.Time `json:"last_refresh_attempt_at,omitempty"`
	NextRefreshAt          *time.Time `json:"next_refresh_at,omitempty"`
	Version                int        `json:"version"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
}

type CreateRequest struct {
	Name                   string       `json:"name"`
	SourceType             SourceType   `json:"source_type"`
	SourceConfig           SourceConfig `json:"source_config"`
	Enabled                *bool        `json:"enabled,omitempty"`
	RefreshIntervalSeconds int          `json:"refresh_interval_seconds,omitempty"`
}

type UpdateRequest struct {
	Version                int          `json:"version"`
	Name                   string       `json:"name"`
	SourceType             SourceType   `json:"source_type"`
	SourceConfig           SourceConfig `json:"source_config"`
	Enabled                bool         `json:"enabled"`
	RefreshIntervalSeconds int          `json:"refresh_interval_seconds"`
}

type Repository interface {
	CreateSubscription(context.Context, store.SubscriptionRecord) (store.SubscriptionRecord, error)
	GetSubscription(context.Context, string) (store.SubscriptionRecord, error)
	ListSubscriptions(context.Context, int, int) ([]store.SubscriptionRecord, error)
	UpdateSubscription(context.Context, store.SubscriptionRecord, int) (store.SubscriptionRecord, error)
	DeleteSubscription(context.Context, string, int) error
	GetSubscriptionSnapshot(context.Context, string) (store.SubscriptionSnapshotRecord, error)
	GetSubscriptionSnapshotByHash(context.Context, string, string) (store.SubscriptionSnapshotRecord, error)
	CommitSubscriptionSnapshot(context.Context, store.SubscriptionSnapshotRecord) error
	ActivateSubscriptionSnapshot(context.Context, string, string, time.Time, time.Time, string) error
	MarkSubscriptionRefreshFailed(context.Context, string, time.Time, time.Time) error
}

type ParsedNodeRepository interface {
	CommitParsedSubscriptionSnapshot(context.Context, store.SubscriptionSnapshotRecord, []store.NodeImportRecord) error
	ActivateParsedSubscriptionSnapshot(context.Context, store.SubscriptionSnapshotRecord, []store.NodeImportRecord, string) error
}

type Parser func([]byte) (nodeparse.Result, error)

type Reconciler interface {
	Apply(context.Context) error
}

type Cipher interface {
	Seal([]byte, []byte) ([]byte, error)
	Open([]byte, []byte) ([]byte, error)
}

type ServiceOption func(*Service) error

func WithRefresh(loader SourceLoader, snapshotDirectory string) ServiceOption {
	return func(service *Service) error {
		if loader == nil {
			return errors.New("subscription source loader is required")
		}
		if strings.TrimSpace(snapshotDirectory) == "" {
			return errors.New("subscription snapshot directory is required")
		}
		service.loader = loader
		service.snapshotDirectory = snapshotDirectory
		return nil
	}
}

func WithParser(parser Parser) ServiceOption {
	return func(service *Service) error {
		if parser == nil {
			return errors.New("subscription node parser is required")
		}
		if _, ok := service.repository.(ParsedNodeRepository); !ok {
			return errors.New("subscription repository does not support parsed nodes")
		}
		service.parser = parser
		return nil
	}
}

func WithReconciler(reconciler Reconciler) ServiceOption {
	return func(service *Service) error {
		if reconciler == nil {
			return errors.New("subscription reconciler is required")
		}
		service.reconciler = reconciler
		return nil
	}
}

type Service struct {
	repository        Repository
	cipher            Cipher
	loader            SourceLoader
	parser            Parser
	reconciler        Reconciler
	snapshotDirectory string
	now               func() time.Time
	refreshLocks      keyedLocker
}

func NewService(repository Repository, cipher Cipher, options ...ServiceOption) (*Service, error) {
	if repository == nil {
		return nil, errors.New("subscription repository is required")
	}
	if cipher == nil {
		return nil, errors.New("subscription cipher is required")
	}
	service := &Service{repository: repository, cipher: cipher, now: time.Now}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(service); err != nil {
			return nil, err
		}
	}
	return service, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (Subscription, error) {
	request = normalizeCreateRequest(request)
	if err := validateRequest(request.Name, request.SourceType, request.SourceConfig, request.RefreshIntervalSeconds); err != nil {
		return Subscription{}, err
	}
	id, err := newID()
	if err != nil {
		return Subscription{}, err
	}
	encrypted, err := s.encryptSourceConfig(id, request.SourceConfig)
	if err != nil {
		return Subscription{}, err
	}
	now := s.now().UTC()
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	record, err := s.repository.CreateSubscription(ctx, store.SubscriptionRecord{
		ID:                     id,
		Name:                   request.Name,
		SourceType:             string(request.SourceType),
		SourceConfigEncrypted:  encrypted,
		Enabled:                enabled,
		RefreshIntervalSeconds: request.RefreshIntervalSeconds,
		NextRefreshAt:          &now,
		Version:                1,
		CreatedAt:              now,
		UpdatedAt:              now,
	})
	if err != nil {
		return Subscription{}, mapRepositoryError(err)
	}
	return fromRecord(record), nil
}

func (s *Service) Get(ctx context.Context, id string) (Subscription, error) {
	record, err := s.repository.GetSubscription(ctx, id)
	if err != nil {
		return Subscription{}, mapRepositoryError(err)
	}
	return fromRecord(record), nil
}

func (s *Service) List(ctx context.Context, limit, offset int) ([]Subscription, error) {
	records, err := s.repository.ListSubscriptions(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	items := make([]Subscription, 0, len(records))
	for _, record := range records {
		items = append(items, fromRecord(record))
	}
	return items, nil
}

func (s *Service) Update(ctx context.Context, id string, request UpdateRequest) (Subscription, error) {
	request.Name = strings.TrimSpace(request.Name)
	if request.Version < 1 {
		return Subscription{}, fmt.Errorf("%w: version must be at least 1", ErrInvalid)
	}
	if request.RefreshIntervalSeconds == 0 {
		request.RefreshIntervalSeconds = defaultRefreshInterval
	}
	if err := validateRequest(request.Name, request.SourceType, request.SourceConfig, request.RefreshIntervalSeconds); err != nil {
		return Subscription{}, err
	}
	encrypted, err := s.encryptSourceConfig(id, request.SourceConfig)
	if err != nil {
		return Subscription{}, err
	}
	now := s.now().UTC()
	record, err := s.repository.UpdateSubscription(ctx, store.SubscriptionRecord{
		ID:                     id,
		Name:                   request.Name,
		SourceType:             string(request.SourceType),
		SourceConfigEncrypted:  encrypted,
		Enabled:                request.Enabled,
		RefreshIntervalSeconds: request.RefreshIntervalSeconds,
		NextRefreshAt:          &now,
		UpdatedAt:              now,
	}, request.Version)
	if err != nil {
		return Subscription{}, mapRepositoryError(err)
	}
	return fromRecord(record), nil
}

func (s *Service) Delete(ctx context.Context, id string, expectedVersion int) error {
	if expectedVersion < 1 {
		return fmt.Errorf("%w: version must be at least 1", ErrInvalid)
	}
	return mapRepositoryError(s.repository.DeleteSubscription(ctx, id, expectedVersion))
}

func (s *Service) SourceConfig(ctx context.Context, id string) (SourceConfig, error) {
	record, err := s.repository.GetSubscription(ctx, id)
	if err != nil {
		return SourceConfig{}, mapRepositoryError(err)
	}
	plaintext, err := s.cipher.Open(record.SourceConfigEncrypted, associatedData(id))
	if err != nil {
		return SourceConfig{}, fmt.Errorf("decrypt subscription source: %w", err)
	}
	var config SourceConfig
	if err := json.Unmarshal(plaintext, &config); err != nil {
		return SourceConfig{}, fmt.Errorf("decode subscription source: %w", err)
	}
	return config, nil
}

func (s *Service) encryptSourceConfig(id string, config SourceConfig) ([]byte, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("encode subscription source: %w", err)
	}
	encrypted, err := s.cipher.Seal(encoded, associatedData(id))
	if err != nil {
		return nil, fmt.Errorf("encrypt subscription source: %w", err)
	}
	return encrypted, nil
}

func normalizeCreateRequest(request CreateRequest) CreateRequest {
	request.Name = strings.TrimSpace(request.Name)
	if request.RefreshIntervalSeconds == 0 {
		request.RefreshIntervalSeconds = defaultRefreshInterval
	}
	return request
}

func validateRequest(name string, sourceType SourceType, config SourceConfig, refreshInterval int) error {
	if name == "" || len(name) > 128 {
		return fmt.Errorf("%w: name must contain 1 to 128 characters", ErrInvalid)
	}
	if refreshInterval < minimumRefreshInterval {
		return fmt.Errorf("%w: refresh interval must be at least %d seconds", ErrInvalid, minimumRefreshInterval)
	}
	if config.TimeoutSeconds < 0 || config.TimeoutSeconds > 300 {
		return fmt.Errorf("%w: timeout must be between 0 and 300 seconds", ErrInvalid)
	}
	if len(config.Headers) > 32 {
		return fmt.Errorf("%w: at most 32 request headers are allowed", ErrInvalid)
	}
	for key, value := range config.Headers {
		if textproto.CanonicalMIMEHeaderKey(key) == "" || len(key) > 128 || len(value) > 4096 || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%w: invalid request header", ErrInvalid)
		}
	}

	switch sourceType {
	case SourceRemote:
		parsed, err := url.Parse(config.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("%w: remote source requires an HTTP or HTTPS URL", ErrInvalid)
		}
		if config.Inline != "" || config.FilePath != "" {
			return fmt.Errorf("%w: remote source cannot include inline or file content", ErrInvalid)
		}
	case SourceInline:
		if config.Inline == "" || len(config.Inline) > maximumInlineBytes {
			return fmt.Errorf("%w: inline source must contain 1 byte to 4 MiB", ErrInvalid)
		}
		if config.URL != "" || config.FilePath != "" {
			return fmt.Errorf("%w: inline source cannot include URL or file path", ErrInvalid)
		}
	case SourceFile:
		if config.FilePath == "" || !filepath.IsAbs(config.FilePath) || strings.ContainsRune(config.FilePath, '\x00') {
			return fmt.Errorf("%w: file source requires an absolute path", ErrInvalid)
		}
		if config.URL != "" || config.Inline != "" {
			return fmt.Errorf("%w: file source cannot include URL or inline content", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unsupported source type %q", ErrInvalid, sourceType)
	}
	return nil
}

func fromRecord(record store.SubscriptionRecord) Subscription {
	return Subscription{
		ID:                     record.ID,
		Name:                   record.Name,
		SourceType:             SourceType(record.SourceType),
		SourceConfigured:       len(record.SourceConfigEncrypted) > 0,
		Enabled:                record.Enabled,
		RefreshIntervalSeconds: record.RefreshIntervalSeconds,
		LastSuccessSnapshotID:  record.LastSuccessSnapshotID,
		ConsecutiveFailures:    record.ConsecutiveFailures,
		LastRefreshAttemptAt:   record.LastRefreshAttemptAt,
		NextRefreshAt:          record.NextRefreshAt,
		Version:                record.Version,
		CreatedAt:              record.CreatedAt,
		UpdatedAt:              record.UpdatedAt,
	}
}

func mapRepositoryError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrConflict):
		return ErrConflict
	default:
		return err
	}
}

func associatedData(id string) []byte {
	return []byte("subscription:" + id)
}

func newID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate subscription id: %w", err)
	}
	return "sub-" + hex.EncodeToString(random[:]), nil
}
