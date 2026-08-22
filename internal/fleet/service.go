// Package fleet maintains a dynamic fleet of Cloudflare Worker proxy endpoints
// inside HX-ProxyGroup (20260823 user decision): give it CF accounts (API
// tokens) and it deploys fleet_size workers per account, probes them on an
// interval, recreates workers after max_consecutive_failures failed probes
// (treated as a ban), and switches accounts when an account's API stops
// accepting calls (banned/revoked).
package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/cfworker"
	"github.com/HengXin666/HX-ProxyGroup/internal/residential"
	"github.com/HengXin666/HX-ProxyGroup/internal/secret"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
	"github.com/HengXin666/HX-ProxyGroup/internal/systemsettings"
)

// SettingsReader supplies the current fleet configuration.
type SettingsReader func(context.Context) (systemsettings.Settings, error)

// Service is the fleet maintainer.
type Service struct {
	store        *store.Store
	residential  *residential.Service
	secretBox    *secret.Box
	logger       *slog.Logger
	dataDir      string
	settingsRead SettingsReader
	sweepMu      sync.Mutex
}

// NewService wires the fleet maintainer.
func NewService(
	database *store.Store,
	residentialService *residential.Service,
	box *secret.Box,
	logger *slog.Logger,
	dataDir string,
	settingsRead SettingsReader,
) (*Service, error) {
	if database == nil {
		return nil, errors.New("fleet: store is required")
	}
	if residentialService == nil {
		return nil, errors.New("fleet: residential service is required")
	}
	if box == nil {
		return nil, errors.New("fleet: secret box is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store: database, residential: residentialService, secretBox: box,
		logger: logger, dataDir: dataDir, settingsRead: settingsRead,
	}, nil
}

// AccountSummary describes one account's fleet state for a sweep pass.
type AccountSummary struct {
	Email           string   `json:"account"`
	Status          string   `json:"status"`
	Workers         []string `json:"workers"`
	Healthy         int      `json:"healthy"`
	BannedThisRound []string `json:"banned_this_round"`
	Error           string   `json:"error,omitempty"`
}

// Summary is the aggregate sweep result.
type Summary struct {
	At           string           `json:"at"`
	Accounts     []AccountSummary `json:"accounts"`
	TotalWorkers int              `json:"total_workers"`
}

// Sweep runs one maintenance pass over all accounts. Concurrent sweeps are
// serialized so the fill loop cannot over-deploy under the periodic loop.
func (s *Service) Sweep(ctx context.Context) (Summary, error) {
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	summary := Summary{At: time.Now().UTC().Format(time.RFC3339Nano)}
	accounts, err := s.store.ListCFAccounts(ctx)
	if err != nil {
		return summary, err
	}
	for _, account := range accounts {
		if account.Status == "banned" {
			summary.Accounts = append(summary.Accounts, AccountSummary{
				Email: account.Email, Status: "banned",
			})
			continue
		}
		result, err := s.maintainAccount(ctx, account)
		if err != nil {
			result = AccountSummary{
				Email: account.Email, Status: "error", Error: truncateStr(err.Error(), 200),
			}
			s.logger.ErrorContext(ctx, "fleet account pass failed", "account", account.Email, "error", err)
		}
		summary.Accounts = append(summary.Accounts, result)
	}
	for _, account := range summary.Accounts {
		if account.Status == "active" {
			summary.TotalWorkers += len(account.Workers)
		}
	}
	s.SetLastSummary(summary)
	return summary, nil
}

func (s *Service) maintainAccount(ctx context.Context, account store.CFAccountRecord) (AccountSummary, error) {
	settings, err := s.settingsRead(ctx)
	if err != nil {
		return AccountSummary{}, err
	}
	client := cfworker.NewClient(
		decryptToken(account.APITokenEncrypted, s.secretBox, account.ID),
		settings.Fleet.Proxy, 30*time.Second,
	)
	if ok, detail := client.AccountOK(ctx, account.CFAccountID); !ok {
		if err := s.store.SetCFAccountStatus(ctx, account.ID, "banned"); err == nil {
			s.logger.WarnContext(ctx, "fleet account banned", "account", account.Email, "detail", detail)
		}
		_ = s.disableAccountProviders(ctx, account.ID)
		return AccountSummary{Email: account.Email, Status: "banned"}, nil
	}

	workers, err := s.store.ListFleetWorkers(ctx, account.ID)
	if err != nil {
		return AccountSummary{}, err
	}
	byName := make(map[string]*store.FleetWorkerRecord, len(workers))
	for i := range workers {
		byName[workers[i].WorkerName] = &workers[i]
	}
	healthy := 0
	banned := make([]string, 0)
	for i := range workers {
		record := byName[workers[i].WorkerName]
		ok, detail := cfworker.ProbeSubscription(ctx, record.CanonicalURL, 15*time.Second)
		record.LastCheck = time.Now().UTC().Format(time.RFC3339Nano)
		record.LastDetail = detail
		if ok {
			record.Failures = 0
			healthy++
		} else {
			record.Failures++
		}
		if record.Failures >= settings.Fleet.MaxConsecutiveFailures {
			// treat as banned: delete worker + disable provider + drop record
			if err := client.DeleteWorker(ctx, account.CFAccountID, record.WorkerName); err != nil {
				s.logger.WarnContext(ctx, "fleet delete worker failed", "worker", record.WorkerName, "error", err)
			}
			if record.ProviderID != "" {
				if err := s.disableProvider(ctx, record.ProviderID); err != nil {
					s.logger.WarnContext(ctx, "fleet disable provider failed", "provider", record.ProviderID, "error", err)
				}
			}
			_ = s.store.DeleteFleetWorker(ctx, account.ID, record.WorkerName)
			delete(byName, record.WorkerName)
			banned = append(banned, record.WorkerName)
			s.logger.WarnContext(ctx, "fleet worker banned (recreating)",
				"worker", record.WorkerName, "failures", record.Failures)
			continue
		}
		_ = s.store.UpsertFleetWorker(ctx, *record)
	}

	// fill up to fleet size
	for len(byName) < settings.Fleet.FleetSize {
		deployed, err := cfworker.Deploy(ctx, client, cfworker.AccountCredentials{
			Email: account.Email, AccountID: account.CFAccountID,
			APIToken: decryptToken(account.APITokenEncrypted, s.secretBox, account.ID),
		}, s.dataDir)
		if err != nil {
			s.logger.ErrorContext(ctx, "fleet deploy failed", "error", err)
			break
		}
		enabled := true
		provider, err := s.residential.CreateProvider(ctx, residential.CreateProviderRequest{
			Name:         "CF-" + deployed.Name,
			Vendor:       "bpb",
			Protocol:     "vless",
			WorkerURL:    deployed.CanonicalURL,
			RotationMode: "cf-worker",
			Enabled:      &enabled,
		})
		if err != nil {
			s.logger.ErrorContext(ctx, "fleet provider create failed", "worker", deployed.Name, "error", err)
			break
		}
		record := store.FleetWorkerRecord{
			AccountID:    account.ID,
			WorkerName:   deployed.Name,
			CanonicalURL: deployed.CanonicalURL,
			ProviderID:   provider.ID,
			Failures:     0,
			LastCheck:    time.Now().UTC().Format(time.RFC3339Nano),
			LastDetail:   "deployed",
		}
		if err := s.store.UpsertFleetWorker(ctx, record); err != nil {
			s.logger.ErrorContext(ctx, "fleet worker upsert failed", "worker", deployed.Name, "error", err)
			break
		}
		byName[deployed.Name] = &record
		s.logger.InfoContext(ctx, "fleet worker deployed",
			"account", account.Email, "worker", deployed.Name,
			"count", len(byName), "target", settings.Fleet.FleetSize)
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sortStrings(names)
	return AccountSummary{
		Email: account.Email, Status: "active",
		Workers: names, Healthy: healthy, BannedThisRound: banned,
	}, nil
}

// disableProvider disables a cf-worker provider (keeps it off the provision list).
func (s *Service) disableProvider(ctx context.Context, providerID string) error {
	provider, err := s.residential.GetProvider(ctx, providerID)
	if err != nil {
		return err
	}
	_, err = s.residential.UpdateProvider(ctx, providerID, residential.UpdateProviderRequest{
		Version:               provider.Version,
		Name:                  provider.Name,
		Vendor:                provider.Vendor,
		Protocol:              provider.Protocol,
		GatewayHost:           provider.GatewayHost,
		GatewayPort:           provider.GatewayPort,
		UsernameTemplate:      provider.UsernameTemplate,
		RotationMode:          provider.RotationMode,
		SessionTTLSeconds:     provider.SessionTTLSeconds,
		MaxConcurrentSessions: provider.MaxConcurrentSessions,
		PoolSize:              provider.PoolSize,
		SessionExpiryPolicy:   provider.SessionExpiryPolicy,
		DefaultRegion:         provider.DefaultRegion,
		DefaultRegionMode:     provider.DefaultRegionMode,
		DefaultRandomRegions:  provider.DefaultRandomRegions,
		Enabled:               false,
	})
	return err
}

func (s *Service) disableAccountProviders(ctx context.Context, accountID string) error {
	workers, err := s.store.ListFleetWorkers(ctx, accountID)
	if err != nil {
		return err
	}
	for _, record := range workers {
		if record.ProviderID != "" {
			_ = s.disableProvider(ctx, record.ProviderID)
		}
	}
	return nil
}

// Run loops the maintenance pass on the configured probe interval.
func (s *Service) Run(ctx context.Context) error {
	for {
		settings, err := s.settingsRead(ctx)
		if err != nil {
			s.logger.ErrorContext(ctx, "fleet settings read failed", "error", err)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(time.Minute):
			}
			continue
		}
		if !settings.Fleet.Enabled {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(30 * time.Second):
			}
			continue
		}
		summary, err := s.Sweep(ctx)
		if err != nil {
			s.logger.ErrorContext(ctx, "fleet sweep failed", "error", err)
		} else {
			s.logger.InfoContext(ctx, "fleet sweep done", "total_workers", summary.TotalWorkers)
		}
		interval := time.Duration(settings.Fleet.ProbeIntervalSeconds) * time.Second
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func decryptToken(encrypted string, box *secret.Box, id string) string {
	if encrypted == "" {
		return ""
	}
	plain, err := box.Open([]byte(encrypted), []byte("cf_account:"+id))
	if err != nil {
		return ""
	}
	return string(plain)
}

func truncateStr(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return text[:max] + "..."
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

var _ = fmt.Sprintf

// AddAccountRequest carries a new Cloudflare account to join the fleet.
type AddAccountRequest struct {
	Email     string `json:"email"`
	AccountID string `json:"cf_account_id"`
	APIToken  string `json:"api_token"`
}

// AddAccount encrypts the token and stores the account.
func (s *Service) AddAccount(ctx context.Context, request AddAccountRequest) (store.CFAccountRecord, error) {
	if request.Email == "" || request.AccountID == "" || request.APIToken == "" {
		return store.CFAccountRecord{}, errors.New("email/cf_account_id/api_token 必填")
	}
	id, err := newAccountID()
	if err != nil {
		return store.CFAccountRecord{}, err
	}
	sealed, err := s.secretBox.Seal([]byte(request.APIToken), []byte("cf_account:"+id))
	if err != nil {
		return store.CFAccountRecord{}, fmt.Errorf("加密 token 失败: %w", err)
	}
	record := store.CFAccountRecord{
		ID:                id,
		Email:             request.Email,
		CFAccountID:       request.AccountID,
		APITokenEncrypted: string(sealed),
		Status:            "active",
	}
	return s.store.CreateCFAccount(ctx, record)
}

// ListAccounts returns all fleet accounts (token not exposed).
func (s *Service) ListAccounts(ctx context.Context) ([]store.CFAccountRecord, error) {
	return s.store.ListCFAccounts(ctx)
}

// RemoveAccount deletes an account and its fleet workers, disabling providers.
func (s *Service) RemoveAccount(ctx context.Context, id string) error {
	account, err := s.store.GetCFAccount(ctx, id)
	if err != nil {
		return err
	}
	_ = s.disableAccountProviders(ctx, id)
	_ = account
	return s.store.DeleteCFAccount(ctx, id)
}

func newAccountID() (string, error) {
	raw := make([]byte, 9)
	if _, err := randRead(raw); err != nil {
		return "", err
	}
	return "cf-account-" + hexEncode(raw), nil
}

// ListAccountWorkers returns health state for one account's fleet workers.
func (s *Service) ListAccountWorkers(ctx context.Context, accountID string) ([]store.FleetWorkerRecord, error) {
	return s.store.ListFleetWorkers(ctx, accountID)
}
