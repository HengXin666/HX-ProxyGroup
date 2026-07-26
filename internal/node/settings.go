package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

// ErrInvalidSettings marks quality-settings validation failures so the API
// layer can map them onto a 422 response.
var ErrInvalidSettings = errors.New("invalid node quality settings")

const qualitySettingsKey = "node_quality_settings"

// QualitySettings controls the periodic node quality checks. All values are
// administrator-editable at runtime; zero values fall back to the defaults
// below when loading.
type QualitySettings struct {
	// CheckIntervalSeconds is the minimum age of a node's last check before
	// the scheduler re-tests it.
	CheckIntervalSeconds int `json:"check_interval_seconds"`
	// TimeoutSeconds bounds a single delay test through Mihomo.
	TimeoutSeconds int `json:"timeout_seconds"`
	// BatchSize caps how many due nodes one scheduler pass may test.
	BatchSize int `json:"batch_size"`
	// TestURL is the HTTP(S) endpoint used for delay tests.
	TestURL string `json:"test_url"`
}

func DefaultQualitySettings() QualitySettings {
	return QualitySettings{
		CheckIntervalSeconds: 600,
		TimeoutSeconds:       8,
		BatchSize:            20,
		TestURL:              defaultTestURL,
	}
}

func (settings QualitySettings) validate() error {
	if settings.CheckIntervalSeconds < 60 || settings.CheckIntervalSeconds > 86400 {
		return fmt.Errorf("%w: check_interval_seconds must be between 60 and 86400", ErrInvalidSettings)
	}
	if settings.TimeoutSeconds < 1 || settings.TimeoutSeconds > 30 {
		return fmt.Errorf("%w: timeout_seconds must be between 1 and 30", ErrInvalidSettings)
	}
	if settings.BatchSize < 1 || settings.BatchSize > 200 {
		return fmt.Errorf("%w: batch_size must be between 1 and 200", ErrInvalidSettings)
	}
	url := strings.TrimSpace(settings.TestURL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("%w: test_url must use HTTP or HTTPS", ErrInvalidSettings)
	}
	if len(url) > 512 {
		return fmt.Errorf("%w: test_url must not exceed 512 characters", ErrInvalidSettings)
	}
	return nil
}

// QualitySettings returns the stored settings, falling back to defaults when
// nothing has been saved yet or the stored payload cannot be decoded.
func (s *Service) QualitySettings(ctx context.Context) (QualitySettings, error) {
	value, err := s.repository.GetMetadata(ctx, qualitySettingsKey)
	if errors.Is(err, store.ErrNotFound) {
		return DefaultQualitySettings(), nil
	}
	if err != nil {
		return QualitySettings{}, err
	}
	settings := DefaultQualitySettings()
	if err := json.Unmarshal([]byte(value), &settings); err != nil {
		return DefaultQualitySettings(), nil
	}
	if err := settings.validate(); err != nil {
		return DefaultQualitySettings(), nil
	}
	return settings, nil
}

func (s *Service) UpdateQualitySettings(ctx context.Context, settings QualitySettings) (QualitySettings, error) {
	settings.TestURL = strings.TrimSpace(settings.TestURL)
	if err := settings.validate(); err != nil {
		return QualitySettings{}, err
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return QualitySettings{}, fmt.Errorf("encode node quality settings: %w", err)
	}
	if err := s.repository.SetMetadata(ctx, qualitySettingsKey, string(encoded)); err != nil {
		return QualitySettings{}, err
	}
	return settings, nil
}
