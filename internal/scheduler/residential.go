package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/residential"
)

// ResidentialRefresher is the residential service surface the scheduler needs.
type ResidentialRefresher interface {
	ListChannels(context.Context) ([]residential.Channel, error)
	RefreshChannelPool(context.Context, string) error
}

// ResidentialConfig controls the session-pool maintenance pass.
type ResidentialConfig struct {
	// PollInterval is how often channels are inspected.
	PollInterval time.Duration
	// MaximumSessionAge forces a pool refresh once the sessions have been in use
	// for this long, so vendor sticky-session TTLs cannot silently expire under
	// an idle channel and leave it pinned to a dead exit.
	MaximumSessionAge time.Duration
	// MaximumRefreshesPerPass bounds the work of a single pass. Each refresh
	// republishes the channel's proxy group, which validates and applies a data
	// plane configuration, so a large deployment must not attempt them all at
	// once.
	MaximumRefreshesPerPass int
}

// ResidentialScheduler keeps residential session pools healthy.
//
// It only refreshes pools that are empty or stale; ordinary exit-IP rotation is
// consumer-driven and takes the cheap selector path instead. Refreshes are
// bounded per pass and run sequentially, so this never turns into an unbounded
// burst of data plane applies.
type ResidentialScheduler struct {
	service ResidentialRefresher
	logger  *slog.Logger
	config  ResidentialConfig
}

func NewResidentialScheduler(
	service ResidentialRefresher,
	logger *slog.Logger,
	config ResidentialConfig,
) (*ResidentialScheduler, error) {
	if service == nil {
		return nil, errors.New("residential service is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if config.PollInterval == 0 {
		config.PollInterval = 5 * time.Minute
	}
	if config.PollInterval < 30*time.Second {
		return nil, errors.New("residential poll interval must be at least 30 seconds")
	}
	if config.MaximumSessionAge == 0 {
		config.MaximumSessionAge = time.Hour
	}
	if config.MaximumSessionAge < time.Minute {
		return nil, errors.New("residential maximum session age must be at least one minute")
	}
	if config.MaximumRefreshesPerPass == 0 {
		config.MaximumRefreshesPerPass = 4
	}
	if config.MaximumRefreshesPerPass < 1 || config.MaximumRefreshesPerPass > 64 {
		return nil, errors.New("residential refreshes per pass must be between 1 and 64")
	}
	return &ResidentialScheduler{service: service, logger: logger, config: config}, nil
}

func (scheduler *ResidentialScheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(scheduler.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := scheduler.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				scheduler.logger.ErrorContext(ctx, "residential session pool pass failed", "error", err)
			}
		}
	}
}

func (scheduler *ResidentialScheduler) runOnce(ctx context.Context) error {
	channels, err := scheduler.service.ListChannels(ctx)
	if err != nil {
		return err
	}
	refreshed := 0
	skipped := 0
	for _, channel := range channels {
		if refreshed >= scheduler.config.MaximumRefreshesPerPass {
			skipped++
			continue
		}
		if !scheduler.needsRefresh(channel) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := scheduler.service.RefreshChannelPool(ctx, channel.ID); err != nil {
			// One unhealthy channel must not stop the others.
			scheduler.logger.ErrorContext(
				ctx,
				"residential session pool refresh failed",
				"channel_id", channel.ID,
				"error", err,
			)
			continue
		}
		refreshed++
		scheduler.logger.InfoContext(
			ctx,
			"residential session pool refreshed",
			"channel_id", channel.ID,
			"pool_size", channel.PoolSize,
		)
	}
	if skipped > 0 {
		// Report the deferred work explicitly rather than letting a bounded pass
		// look like full coverage.
		scheduler.logger.InfoContext(
			ctx,
			"residential session pool refreshes deferred to the next pass",
			"deferred", skipped,
			"limit", scheduler.config.MaximumRefreshesPerPass,
		)
	}
	return nil
}

// needsRefresh reports whether a channel's pool must be regenerated.
func (scheduler *ResidentialScheduler) needsRefresh(channel residential.Channel) bool {
	if !channel.Enabled {
		return false
	}
	if channel.PoolSize == 0 {
		return true
	}
	if channel.Mode != residential.ModeSticky {
		// Passthrough channels delegate rotation to the vendor, so their single
		// upstream never goes stale on our side.
		return false
	}
	// A never-rotated channel is left alone: its sessions were created with the
	// channel and rotating them would change a working exit IP unprompted.
	if channel.LastRotatedAt == nil {
		return false
	}
	return time.Since(*channel.LastRotatedAt) >= scheduler.config.MaximumSessionAge
}
