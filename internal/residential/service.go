package residential

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

// Repository is the persistence surface the residential domain needs.
type Repository interface {
	CreateResidentialProvider(context.Context, store.ResidentialProviderRecord) (store.ResidentialProviderRecord, error)
	GetResidentialProvider(context.Context, string) (store.ResidentialProviderRecord, error)
	ListResidentialProviders(context.Context) ([]store.ResidentialProviderRecord, error)
	UpdateResidentialProvider(context.Context, store.ResidentialProviderRecord, int) (store.ResidentialProviderRecord, error)
	DeleteResidentialProvider(context.Context, string, int) error

	CreateResidentialChannel(context.Context, store.ResidentialChannelRecord) (store.ResidentialChannelRecord, error)
	GetResidentialChannel(context.Context, string) (store.ResidentialChannelRecord, error)
	GetResidentialChannelByRotateToken(context.Context, string) (store.ResidentialChannelRecord, error)
	ListResidentialChannels(context.Context) ([]store.ResidentialChannelRecord, error)
	UpdateResidentialChannel(context.Context, store.ResidentialChannelRecord, int) (store.ResidentialChannelRecord, error)
	SetResidentialChannelRotation(context.Context, string, int, string, time.Time) (store.ResidentialChannelRecord, error)
	RotateResidentialChannelToken(context.Context, string, string) (store.ResidentialChannelRecord, error)
	DeleteResidentialChannel(context.Context, string, int) error

	ReplaceResidentialSessionPool(context.Context, string, []store.ResidentialSessionNode, time.Time) ([]string, error)
	UpsertResidentialSessionNode(context.Context, string, store.ResidentialSessionNode, time.Time) (string, error)
	DeleteResidentialSessionNode(context.Context, string, string) error
	ListResidentialSessionNodes(context.Context, string) ([]store.NodeConfigRecord, error)
	SetResidentialChannelPoolCreatedAt(context.Context, string, time.Time) error
	DeleteResidentialSessionPool(context.Context, string) error
	CreateResidentialClientSession(context.Context, store.ResidentialClientSessionRecord) (store.ResidentialClientSessionRecord, error)
	GetResidentialClientSession(context.Context, string, string) (store.ResidentialClientSessionRecord, error)
	ListResidentialClientSessions(context.Context, string) ([]store.ResidentialClientSessionRecord, error)
	UpdateResidentialClientSessionRoute(context.Context, string, string, string, int, *time.Time) (store.ResidentialClientSessionRecord, error)
	UpdateResidentialClientSessionAllocation(context.Context, string, string, string, time.Time, *time.Time, bool) (store.ResidentialClientSessionRecord, error)
	RestoreResidentialClientSessionState(context.Context, store.ResidentialClientSessionRecord) error
	DeleteResidentialClientSession(context.Context, string, string) error

	GetProxyGroup(context.Context, string) (store.ProxyGroupRecord, error)
	GetListener(context.Context, string) (store.ListenerRecord, error)
}

type Cipher interface {
	Seal([]byte, []byte) ([]byte, error)
	Open([]byte, []byte) ([]byte, error)
}

// GroupService and ListenerService mirror the interfaces used by
// internal/proxyservice so a channel reuses the exact same provisioning path as
// a hand-built proxy service.
type GroupService interface {
	Create(context.Context, proxygroup.CreateRequest) (proxygroup.Group, error)
	Update(context.Context, string, proxygroup.UpdateRequest) (proxygroup.Group, error)
	Delete(context.Context, string, int) error
}

type ListenerService interface {
	Create(context.Context, listener.CreateRequest) (listener.Listener, error)
	Get(context.Context, string) (listener.Listener, error)
	Update(context.Context, string, listener.UpdateRequest) (listener.Listener, error)
	Delete(context.Context, string, int) error
}

// Selector switches which pooled session a channel's group currently uses. It is
// satisfied by the Mihomo manager and is the reason rotation does not need a
// configuration recompile.
type Selector interface {
	SelectProxy(ctx context.Context, groupName, proxyName string) error
}

// ReachabilityChecker confirms that a pooled session can still egress. The data
// plane reports latency only, so a successful check proves the newly selected
// session works without disclosing its exit address.
type ReachabilityChecker interface {
	CheckProxyReachable(ctx context.Context, proxyName string) (int, error)
}

// SessionRouter republishes IN-USER routes and terminates only the connections
// owned by the logical client session being switched.
type SessionRouter interface {
	Apply(context.Context) error
	CloseConnectionsByInboundUser(context.Context, string, string) error
}

// Service is the residential proxy application service.
type Service struct {
	repository          Repository
	cipher              Cipher
	groups              GroupService
	listeners           ListenerService
	selector            Selector
	checker             ReachabilityChecker
	sessionRouter       SessionRouter
	fetchNodes          NodeFetcher
	fetchNodesWithProxy func(context.Context, string, string) ([]FetchedNode, error)
	now                 func() time.Time

	// rotateLimiter bounds how often each channel may rotate. Rotation is
	// consumer-triggered over a public route, so it needs its own limit
	// independent of admin authentication.
	rotateLimiter *rotateLimiter

	// refreshMutex serializes pool refreshes so two concurrent top-ups cannot
	// interleave their node replacements for the same channel.
	refreshMutex sync.Mutex
	// clientSessionMutex serializes slot allocation and route changes. The
	// critical section is bounded by one database update and one data-plane
	// apply; no goroutine or queue is created per client session.
	clientSessionMutex sync.Mutex
}

type Option func(*Service)

// WithSelector enables data-plane session switching.
func WithSelector(selector Selector) Option {
	return func(service *Service) {
		if selector != nil {
			service.selector = selector
		}
	}
}

// WithReachabilityChecker enables post-rotation verification that the newly
// selected session can actually egress.
func WithReachabilityChecker(checker ReachabilityChecker) Option {
	return func(service *Service) {
		if checker != nil {
			service.checker = checker
		}
	}
}

// WithSessionRouter enables one-listener, multi-session residential routing.
func WithSessionRouter(router SessionRouter) Option {
	return func(service *Service) {
		if router != nil {
			service.sessionRouter = router
		}
	}
}

// WithClock overrides the clock, for tests.
func WithClock(now func() time.Time) Option {
	return func(service *Service) {
		if now != nil {
			service.now = now
		}
	}
}

// WithRotateInterval sets the minimum interval between two rotations of one
// channel.
func WithRotateInterval(interval time.Duration) Option {
	return func(service *Service) {
		if interval > 0 {
			service.rotateLimiter.minimumInterval = interval
		}
	}
}

// WithNodeFetcher overrides how api-list providers fetch their endpoint lists.
// The default implementation performs one bounded HTTPS GET per pool render.
func WithNodeFetcher(fetcher NodeFetcher) Option {
	return func(service *Service) {
		if fetcher != nil {
			service.fetchNodes = fetcher
			service.fetchNodesWithProxy = nil
		}
	}
}

func NewService(
	repository Repository,
	cipher Cipher,
	groups GroupService,
	listeners ListenerService,
	options ...Option,
) (*Service, error) {
	if repository == nil {
		return nil, errors.New("residential repository is required")
	}
	if cipher == nil {
		return nil, errors.New("residential cipher is required")
	}
	if groups == nil || listeners == nil {
		return nil, errors.New("residential proxy group and listener services are required")
	}
	service := &Service{
		repository:          repository,
		cipher:              cipher,
		groups:              groups,
		listeners:           listeners,
		fetchNodes:          fetchNodesFromAPI,
		fetchNodesWithProxy: fetchNodesFromAPIWithProxy,
		now:                 time.Now,
		rotateLimiter:       newRotateLimiter(defaultRotateInterval),
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		option(service)
	}
	service.rotateLimiter.now = func() time.Time { return service.now() }
	return service, nil
}

// materializePool renders the session pool for one channel and persists it as
// node rows. It returns the node ids in pool order.
//
// providerSessions returns the pool sessions for one channel. api-list
// providers fetch their endpoints from the extraction API; every other mode
// renders gateway-login sessions from the username template.
func (s *Service) providerSessions(
	ctx context.Context,
	provider Provider,
	credentials Credentials,
	regionSelection RegionSelection,
	size int,
) ([]Session, error) {
	region, err := chooseRegion(regionSelection)
	if err != nil {
		return nil, err
	}
	if provider.RotationMode == RotationAPIList {
		if strings.TrimSpace(provider.APIURL) == "" {
			return nil, fmt.Errorf("%w: api-list provider has no api_url", ErrInvalid)
		}
		apiURL := provider.APIURL
		if region != "" && (regionSelection.Mode == RegionModeApplicationRandom || apiURLHasRegionParameter(apiURL)) {
			apiURL, err = apiURLWithRegion(apiURL, region)
			if err != nil {
				return nil, fmt.Errorf("apply residential region to api_url: %w", err)
			}
		}
		var nodes []FetchedNode
		if s.fetchNodesWithProxy != nil {
			nodes, err = s.fetchNodesWithProxy(ctx, apiURL, provider.APIProxyURL)
		} else {
			nodes, err = s.fetchNodes(ctx, apiURL)
		}
		if err != nil {
			return nil, err
		}
		sessions := sessionsFromNodes(nodes, size)
		if len(sessions) == 0 {
			return nil, fmt.Errorf("%w: api-list endpoint returned no nodes", ErrInvalid)
		}
		return sessions, nil
	}
	return buildSessions(provider, credentials, region, size)
}

func (s *Service) materializePool(
	ctx context.Context,
	channelID string,
	channelName string,
	provider Provider,
	credentials Credentials,
	regionSelection RegionSelection,
	size int,
) ([]string, error) {
	region, err := chooseRegion(regionSelection)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	resolvedRegion := RegionSelection{Mode: RegionModeFixed, Region: region}
	sessions, err := s.providerSessions(ctx, provider, credentials, resolvedRegion, size)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	records := make([]store.ResidentialSessionNode, 0, len(sessions))
	for _, session := range sessions {
		fingerprint, err := sessionFingerprint(channelID, provider, session)
		if err != nil {
			return nil, err
		}
		displayName := sessionDisplayName(channelName, region, session)
		canonical := canonicalNodeConfig(provider, session, credentials.Password, displayName)
		encrypted, err := s.sealNodeConfig(canonical, fingerprint)
		if err != nil {
			return nil, err
		}
		nodeID, err := newID("node-residential")
		if err != nil {
			return nil, err
		}
		protocol := provider.Protocol
		if protocol == "https" {
			protocol = "http"
		}
		records = append(records, store.ResidentialSessionNode{
			ID:                       nodeID,
			Fingerprint:              fingerprint,
			DisplayName:              displayName,
			Protocol:                 protocol,
			CanonicalConfigEncrypted: encrypted,
		})
	}
	return s.repository.ReplaceResidentialSessionPool(ctx, channelID, records, s.now().UTC())
}

// sealNodeConfig encrypts a canonical node config with the same associated data
// the Mihomo compiler expects when decrypting node rows.
func (s *Service) sealNodeConfig(canonical map[string]any, fingerprint string) ([]byte, error) {
	encoded, err := marshalCanonical(canonical)
	if err != nil {
		return nil, err
	}
	sealed, err := s.cipher.Seal(encoded, []byte("node:"+fingerprint))
	if err != nil {
		return nil, fmt.Errorf("encrypt residential session node: %w", err)
	}
	return sealed, nil
}

// RefreshChannelPool re-renders one channel's session pool with fresh session
// ids and republishes the group membership. This is the heavyweight path used
// when the pool is exhausted or the provider configuration changed; ordinary
// rotation only moves the selector.
func (s *Service) RefreshChannelPool(ctx context.Context, channelID string) error {
	s.refreshMutex.Lock()
	defer s.refreshMutex.Unlock()

	record, err := s.repository.GetResidentialChannel(ctx, channelID)
	if err != nil {
		return mapStoreError(err)
	}
	if record.Mode == ModeSticky {
		return fmt.Errorf("%w: sticky channels allocate IPs per client session; rotate that session instead", ErrInvalid)
	}
	providerRecord, err := s.repository.GetResidentialProvider(ctx, record.ProviderID)
	if err != nil {
		return mapStoreError(err)
	}
	provider := s.providerFromRecord(providerRecord)
	credentials, err := s.providerCredentials(providerRecord)
	if err != nil {
		return err
	}
	groupRecord, err := s.repository.GetProxyGroup(ctx, record.ProxyGroupID)
	if err != nil {
		return mapStoreError(err)
	}
	existingPool, err := s.repository.ListResidentialSessionNodes(ctx, record.ID)
	if err != nil {
		return err
	}
	// The channel's current pool size wins over the provider default: an
	// operator may have sized this channel deliberately, and a refresh must not
	// silently resize it. Only an empty pool falls back to the provider value.
	size := len(existingPool)
	if size == 0 {
		size = provider.PoolSize
	}
	if record.Mode == ModePassthrough {
		size = 1
	}
	poolCreatedAt := s.now().UTC()
	regionSelection, err := normalizeRegionSelection(record.RegionMode, record.Region, parseRegionList(record.RandomRegions))
	if err != nil {
		return err
	}
	nodeIDs, err := s.materializePool(ctx, record.ID, record.Name, provider, credentials, regionSelection, size)
	if err != nil {
		return err
	}
	// Republishing the group is what actually pushes the new sessions into the
	// data plane; it validates and applies the candidate configuration.
	if _, err := s.groups.Update(ctx, groupRecord.ID, proxygroup.UpdateRequest{
		Version:       groupRecord.Version,
		Name:          groupRecord.Name,
		Strategy:      groupRecord.Strategy,
		SourceSpec:    proxygroup.SourceSpec{NodeIDs: nodeIDs, AllowEmpty: record.Mode == ModeSticky},
		Enabled:       groupRecord.Enabled,
		EmptyBehavior: groupRecord.EmptyBehavior,
	}); err != nil {
		restoreErr := s.restoreResidentialPoolAndGroup(ctx, record, existingPool)
		if restoreErr != nil {
			return fmt.Errorf("republish residential proxy group: %w; restore previous pool: %v", err, restoreErr)
		}
		return fmt.Errorf("republish residential proxy group: %w", err)
	}
	if record.Mode == ModeSticky {
		if err := s.selectActiveSession(ctx, record, 0); err != nil {
			restoreErr := s.restoreResidentialPoolAndGroup(ctx, record, existingPool)
			if restoreErr != nil {
				return fmt.Errorf("select refreshed residential session: %w; restore previous pool: %v", err, restoreErr)
			}
			return err
		}
	}
	if err := s.repository.SetResidentialChannelPoolCreatedAt(ctx, record.ID, poolCreatedAt); err != nil {
		return err
	}
	return nil
}

// restoreResidentialPoolAndGroup puts the previous node rows back after a
// group publish failure. Pool replacement is transactional by itself, but the
// data-plane publish is a separate operation and can fail after replacement.
func (s *Service) restoreResidentialPoolAndGroup(
	ctx context.Context,
	record store.ResidentialChannelRecord,
	previousPool []store.NodeConfigRecord,
) error {
	if len(previousPool) == 0 {
		return nil
	}
	sessions := make([]store.ResidentialSessionNode, 0, len(previousPool))
	for _, node := range previousPool {
		sessions = append(sessions, store.ResidentialSessionNode{
			ID:                       node.ID,
			Fingerprint:              node.Fingerprint,
			DisplayName:              node.DisplayName,
			Protocol:                 node.Protocol,
			CanonicalConfigEncrypted: node.CanonicalConfigEncrypted,
		})
	}
	nodeIDs, err := s.repository.ReplaceResidentialSessionPool(ctx, record.ID, sessions, s.now().UTC())
	if err != nil {
		return err
	}
	groupRecord, err := s.repository.GetProxyGroup(ctx, record.ProxyGroupID)
	if err != nil {
		return mapStoreError(err)
	}
	if _, err := s.groups.Update(ctx, groupRecord.ID, proxygroup.UpdateRequest{
		Version:       groupRecord.Version,
		Name:          groupRecord.Name,
		Strategy:      groupRecord.Strategy,
		SourceSpec:    proxygroup.SourceSpec{NodeIDs: nodeIDs, AllowEmpty: record.Mode == ModeSticky},
		Enabled:       groupRecord.Enabled,
		EmptyBehavior: groupRecord.EmptyBehavior,
	}); err != nil {
		return err
	}
	if record.Mode == ModeSticky {
		return s.selectActiveSession(ctx, record, 0)
	}
	return nil
}

// refreshChannelsOfProvider re-renders every channel belonging to one provider.
// It is used after the provider's gateway or credentials change.
func (s *Service) refreshChannelsOfProvider(ctx context.Context, providerID string) error {
	records, err := s.repository.ListResidentialChannels(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.ProviderID != providerID {
			continue
		}
		if err := s.RefreshChannelPool(ctx, record.ID); err != nil {
			return fmt.Errorf("refresh channel %q after provider change: %w", record.Name, err)
		}
	}
	return nil
}
