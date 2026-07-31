package residential

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

// Channel modes.
const (
	// ModePassthrough authenticates the consumer at our listener and forwards to
	// the vendor gateway. Exit IP changes follow the vendor's own policy.
	ModePassthrough = "passthrough"
	// ModeSticky pins one pooled session per channel so the exit IP is stable,
	// and exposes an API to advance to the next residential IP.
	ModeSticky = "sticky"
)

// Channel is the administrator-facing view of one residential entry point.
type Channel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderID   string `json:"provider_id"`
	ProviderName string `json:"provider_name,omitempty"`
	Mode         string `json:"mode"`
	ProxyGroupID string `json:"proxy_group_id"`
	ListenerID   string `json:"listener_id"`
	Region       string `json:"region,omitempty"`
	// Endpoint describes where consumers connect.
	Endpoint ChannelEndpoint `json:"endpoint"`
	// PoolSize is how many sticky sessions are currently materialized.
	PoolSize           int        `json:"pool_size"`
	ActiveSessionIndex int        `json:"active_session_index"`
	RotateCount        int        `json:"rotate_count"`
	LastRotatedAt      *time.Time `json:"last_rotated_at,omitempty"`
	LastExitIP         string     `json:"last_exit_ip,omitempty"`
	// RotatePath is the public, token-addressed rotate endpoint. The token is
	// the credential, so it is only returned to an authenticated administrator.
	RotatePath string    `json:"rotate_path,omitempty"`
	CanRotate  bool      `json:"can_rotate"`
	Enabled    bool      `json:"enabled"`
	Version    int       `json:"version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type ChannelEndpoint struct {
	Kind        string `json:"kind"`
	BindAddress string `json:"bind_address"`
	Port        int    `json:"port"`
	AuthEnabled bool   `json:"auth_enabled"`
}

type CreateChannelRequest struct {
	Name       string                 `json:"name"`
	ProviderID string                 `json:"provider_id"`
	Mode       string                 `json:"mode"`
	Region     string                 `json:"region,omitempty"`
	PoolSize   int                    `json:"pool_size,omitempty"`
	Listener   ChannelListenerRequest `json:"listener"`
	Enabled    *bool                  `json:"enabled,omitempty"`
}

type ChannelListenerRequest struct {
	Kind        string         `json:"kind"`
	BindAddress string         `json:"bind_address"`
	Port        int            `json:"port"`
	Auth        *listener.Auth `json:"auth,omitempty"`
}

type UpdateChannelRequest struct {
	Version int    `json:"version"`
	Name    string `json:"name"`
	Region  string `json:"region,omitempty"`
	Enabled bool   `json:"enabled"`
}

func (s *Service) ListChannels(ctx context.Context) ([]Channel, error) {
	records, err := s.repository.ListResidentialChannels(ctx)
	if err != nil {
		return nil, err
	}
	providers, err := s.repository.ListResidentialProviders(ctx)
	if err != nil {
		return nil, err
	}
	providerNames := make(map[string]string, len(providers))
	for _, provider := range providers {
		providerNames[provider.ID] = provider.Name
	}
	channels := make([]Channel, 0, len(records))
	for _, record := range records {
		channel, err := s.channelFromRecord(ctx, record, providerNames[record.ProviderID])
		if err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	return channels, nil
}

func (s *Service) GetChannel(ctx context.Context, id string) (Channel, error) {
	record, err := s.repository.GetResidentialChannel(ctx, id)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	provider, err := s.repository.GetResidentialProvider(ctx, record.ProviderID)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	return s.channelFromRecord(ctx, record, provider.Name)
}

// CreateChannel provisions a residential entry point end to end: it materializes
// the session pool, creates the proxy group over those sessions, then creates the
// listener bound to that group. Every step is compensated on failure so a
// half-built channel is never left behind.
func (s *Service) CreateChannel(ctx context.Context, request CreateChannelRequest) (Channel, error) {
	providerRecord, err := s.repository.GetResidentialProvider(ctx, strings.TrimSpace(request.ProviderID))
	if errors.Is(err, store.ErrNotFound) {
		return Channel{}, fmt.Errorf("%w: provider does not exist", ErrInvalid)
	}
	if err != nil {
		return Channel{}, err
	}
	if !providerRecord.Enabled {
		return Channel{}, fmt.Errorf("%w: provider is disabled", ErrInvalid)
	}
	provider := s.providerFromRecord(providerRecord)

	name := strings.TrimSpace(request.Name)
	if len(name) < 1 || len(name) > 128 {
		return Channel{}, fmt.Errorf("%w: name must contain 1 to 128 characters", ErrInvalid)
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	if mode != ModePassthrough && mode != ModeSticky {
		return Channel{}, fmt.Errorf("%w: mode must be %q or %q", ErrInvalid, ModePassthrough, ModeSticky)
	}
	if mode == ModeSticky && !provider.SupportsSticky {
		return Channel{}, fmt.Errorf(
			"%w: provider rotation mode %q cannot pin sticky sessions; use %q mode",
			ErrInvalid,
			provider.RotationMode,
			ModePassthrough,
		)
	}
	region := strings.ToLower(strings.TrimSpace(request.Region))
	if region == "" {
		region = provider.DefaultRegion
	}
	if err := validateRegion(region); err != nil {
		return Channel{}, err
	}
	poolSize := request.PoolSize
	if poolSize == 0 {
		poolSize = provider.PoolSize
	}
	if mode == ModePassthrough {
		// Passthrough leaves rotation to the vendor, so one upstream is enough.
		poolSize = 1
	}
	if poolSize < 1 || poolSize > 64 {
		return Channel{}, fmt.Errorf("%w: pool_size must be between 1 and 64", ErrInvalid)
	}
	enabled := request.Enabled == nil || *request.Enabled

	channelID, err := newID("residential-channel")
	if err != nil {
		return Channel{}, err
	}
	rotateToken, err := newToken()
	if err != nil {
		return Channel{}, err
	}

	credentials, err := s.openCredentials(providerRecord)
	if err != nil {
		return Channel{}, err
	}
	nodeIDs, err := s.materializePool(ctx, channelID, name, provider, credentials, region, poolSize)
	if err != nil {
		return Channel{}, err
	}

	cleanupPool := func() {
		_ = s.repository.DeleteResidentialSessionPool(ctx, channelID)
	}

	group, err := s.groups.Create(ctx, proxygroup.CreateRequest{
		Name:     channelGroupName(name),
		Strategy: "manual",
		SourceSpec: proxygroup.SourceSpec{
			NodeIDs: nodeIDs,
		},
		Enabled:       &enabled,
		EmptyBehavior: "fail-closed",
	})
	if err != nil {
		if group.ID != "" {
			_ = s.groups.Delete(ctx, group.ID, group.Version)
		}
		cleanupPool()
		return Channel{}, fmt.Errorf("%w: create proxy group: %v", ErrInvalid, err)
	}

	createdListener, err := s.listeners.Create(ctx, listener.CreateRequest{
		Name:         channelListenerName(name),
		Kind:         request.Listener.Kind,
		BindAddress:  request.Listener.BindAddress,
		Port:         request.Listener.Port,
		ProxyGroupID: group.ID,
		Auth:         request.Listener.Auth,
		Enabled:      &enabled,
	})
	if err != nil {
		_ = s.groups.Delete(ctx, group.ID, group.Version)
		cleanupPool()
		return Channel{}, fmt.Errorf("%w: create listener: %v", ErrInvalid, err)
	}

	now := s.now().UTC()
	created, err := s.repository.CreateResidentialChannel(ctx, store.ResidentialChannelRecord{
		ID:           channelID,
		Name:         name,
		ProviderID:   provider.ID,
		Mode:         mode,
		ProxyGroupID: group.ID,
		ListenerID:   createdListener.ID,
		Region:       region,
		RotateToken:  rotateToken,
		Enabled:      enabled,
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		_ = s.listeners.Delete(ctx, createdListener.ID, createdListener.Version)
		_ = s.groups.Delete(ctx, group.ID, group.Version)
		cleanupPool()
		return Channel{}, mapStoreError(err)
	}
	// Point the selector at the first pooled session so a sticky channel has a
	// deterministic exit from the moment it is created.
	if mode == ModeSticky {
		_ = s.selectActiveSession(ctx, created, 0)
	}
	return s.channelFromRecord(ctx, created, provider.Name)
}

func (s *Service) UpdateChannel(ctx context.Context, id string, request UpdateChannelRequest) (Channel, error) {
	if request.Version < 1 {
		return Channel{}, fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	existing, err := s.repository.GetResidentialChannel(ctx, id)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	name := strings.TrimSpace(request.Name)
	if len(name) < 1 || len(name) > 128 {
		return Channel{}, fmt.Errorf("%w: name must contain 1 to 128 characters", ErrInvalid)
	}
	region := strings.ToLower(strings.TrimSpace(request.Region))
	if err := validateRegion(region); err != nil {
		return Channel{}, err
	}
	regionChanged := region != existing.Region

	existing.Name = name
	existing.Region = region
	existing.Enabled = request.Enabled
	existing.UpdatedAt = s.now().UTC()
	updated, err := s.repository.UpdateResidentialChannel(ctx, existing, request.Version)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	if regionChanged {
		// The region is encoded in the gateway username, so the pool must be
		// re-rendered for the new region.
		if err := s.RefreshChannelPool(ctx, updated.ID); err != nil {
			return Channel{}, err
		}
		refreshed, err := s.repository.GetResidentialChannel(ctx, updated.ID)
		if err != nil {
			return Channel{}, mapStoreError(err)
		}
		updated = refreshed
	}
	provider, err := s.repository.GetResidentialProvider(ctx, updated.ProviderID)
	if err != nil {
		return Channel{}, mapStoreError(err)
	}
	return s.channelFromRecord(ctx, updated, provider.Name)
}

// DeleteChannel removes the channel and everything it provisioned, in reverse
// dependency order: listener, then group, then pooled session nodes.
func (s *Service) DeleteChannel(ctx context.Context, id string, version int) error {
	if version < 1 {
		return fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	record, err := s.repository.GetResidentialChannel(ctx, id)
	if err != nil {
		return mapStoreError(err)
	}
	if err := s.repository.DeleteResidentialChannel(ctx, id, version); err != nil {
		return mapStoreError(err)
	}
	listenerRecord, err := s.repository.GetListener(ctx, record.ListenerID)
	if err == nil {
		if err := s.listeners.Delete(ctx, listenerRecord.ID, listenerRecord.Version); err != nil {
			return fmt.Errorf("channel deleted but listener cleanup failed: %w", err)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	groupRecord, err := s.repository.GetProxyGroup(ctx, record.ProxyGroupID)
	if err == nil {
		if err := s.groups.Delete(ctx, groupRecord.ID, groupRecord.Version); err != nil {
			return fmt.Errorf("channel deleted but proxy group cleanup failed: %w", err)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err := s.repository.DeleteResidentialSessionPool(ctx, record.ID); err != nil {
		return fmt.Errorf("channel deleted but session pool cleanup failed: %w", err)
	}
	return nil
}

func (s *Service) channelFromRecord(
	ctx context.Context,
	record store.ResidentialChannelRecord,
	providerName string,
) (Channel, error) {
	pool, err := s.repository.ListResidentialSessionNodes(ctx, record.ID)
	if err != nil {
		return Channel{}, err
	}
	channel := Channel{
		ID:                 record.ID,
		Name:               record.Name,
		ProviderID:         record.ProviderID,
		ProviderName:       providerName,
		Mode:               record.Mode,
		ProxyGroupID:       record.ProxyGroupID,
		ListenerID:         record.ListenerID,
		Region:             record.Region,
		PoolSize:           len(pool),
		ActiveSessionIndex: record.ActiveSessionIndex,
		RotateCount:        record.RotateCount,
		LastRotatedAt:      record.LastRotatedAt,
		LastExitIP:         record.LastExitIP,
		CanRotate:          record.Mode == ModeSticky && len(pool) > 1,
		Enabled:            record.Enabled,
		Version:            record.Version,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
	}
	if record.Mode == ModeSticky && record.RotateToken != "" {
		channel.RotatePath = "/rot/" + record.RotateToken
	}
	listenerRecord, err := s.repository.GetListener(ctx, record.ListenerID)
	if err == nil {
		channel.Endpoint = ChannelEndpoint{
			Kind:        listenerRecord.Kind,
			BindAddress: listenerRecord.BindAddress,
			Port:        listenerRecord.Port,
			AuthEnabled: listenerRecord.AuthMode != "none" && len(listenerRecord.AuthConfigEncrypted) > 0,
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return Channel{}, err
	}
	return channel, nil
}

func channelGroupName(channelName string) string {
	return "residential-" + channelName
}

func channelListenerName(channelName string) string {
	return "residential-" + channelName + "-entry"
}
