package listener

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func parseIPAddress(value string) net.IP { return net.ParseIP(strings.TrimSpace(value)) }

// Shared-inbound ownership markers.
//
// A shared inbound is a *membership* concept, not a separate record: every
// proxy service keeps its own listener row (its own name, credential, protocol
// and proxy group), but rows marked here reuse one bind address and port
// instead of each reserving a dedicated one. Mihomo then separates the members
// by username through IN-USER rules, so a single client process can carry
// every protocol and every group through one URL.
const (
	// SharedInboundStandardOwner marks HTTP / SOCKS5 / Mixed members that share
	// the aggregate Mixed port.
	SharedInboundStandardOwner = "standard"
	// SharedInboundWebSocketOwner marks VLESS / VMess / Trojan members that
	// share one aggregate WebSocket port per protocol.
	SharedInboundWebSocketOwner = "websocket"

	// sharedInboundRouteName is the single reserved edge route shared by every
	// WebSocket service carried by an aggregate listener.
	sharedInboundRouteName = "shared"
	// sharedAggregateNamePrefix identifies the single aggregate row of a family.
	sharedAggregateNamePrefix = "hx-shared-inbound-"
)

// SharedInboundOwnerOf reports the aggregate family a record belongs to, or ""
// for a dedicated per-service listener.
func SharedInboundOwnerOf(record store.ListenerRecord) string {
	return strings.TrimSpace(record.SharedInbound)
}

// IsSharedInboundOwner reports whether an owner marker is one of the two
// aggregate families.
func IsSharedInboundOwner(owner string) bool {
	switch strings.TrimSpace(owner) {
	case SharedInboundStandardOwner, SharedInboundWebSocketOwner:
		return true
	default:
		return false
	}
}

// SharedInboundRoutePath is the single reserved edge route shared by every
// WebSocket service carried by an aggregate listener, so one URL works for
// every protocol.
func SharedInboundRoutePath() string {
	return WebSocketPathPrefix + sharedInboundRouteName
}

// SharedInboundSpec is the resolved shared-inbound configuration handed to the
// listener service. It mirrors systemsettings.SharedInboundSettings without
// creating an import cycle; the application layer maps one onto the other.
type SharedInboundSpec struct {
	// Enabled publishes standard services through the aggregate Mixed listener.
	Enabled bool
	// IncludeWebSocket publishes WebSocket services through the aggregate
	// VLESS / VMess / Trojan listeners.
	IncludeWebSocket bool
	MixedBindAddress string
	MixedPort        int
	WSPort           int
	// Client-facing endpoints. The shared WebSocket port is loopback-only, so
	// a public host (normally the reverse proxy) is required for a usable
	// subscription.
	MixedPublicHost string
	MixedPublicPort int
	MixedPublicTLS  bool
	WSPublicHost    string
	WSPublicPort    int
}

// SharedEndpointProvider resolves the current shared-inbound configuration at
// export time. The listener service keeps it optional so packages that only
// need per-service listeners (tests) can skip it.
type SharedEndpointProvider func() (SharedInboundSpec, bool)

// ResolveSharedEndpoint maps a member listener onto the client-facing endpoint
// of its aggregate family. It returns ok=false when the family is enabled but
// has no reachable endpoint, which callers surface as a disabled share link so
// an internal Mihomo port never leaks into a subscription.
func ResolveSharedEndpoint(spec SharedInboundSpec, record store.ListenerRecord, requestHost string) (PublicEndpoint, string, int, bool) {
	owner := SharedInboundOwnerOf(record)
	switch owner {
	case SharedInboundStandardOwner:
		host := strings.TrimSpace(spec.MixedPublicHost)
		port := spec.MixedPublicPort
		endpoint := PublicEndpoint{Host: host, Port: port, TLS: spec.MixedPublicTLS}
		if host == "" {
			// Reuse the per-service rule: only an explicitly public bind
			// address may be advertised, otherwise fall back to the request
			// host (the admin reaches the control plane by name).
			if ip := parseIPAddress(spec.MixedBindAddress); ip != nil && !ip.IsLoopback() && !ip.IsUnspecified() {
				return PublicEndpoint{Port: spec.MixedPort}, ip.String(), spec.MixedPort, true
			}
			if requestHost == "" {
				return PublicEndpoint{}, "", 0, false
			}
			fallback := exportHost(spec.MixedBindAddress, requestHost)
			if fallback == "" {
				return PublicEndpoint{}, "", 0, false
			}
			return PublicEndpoint{Port: spec.MixedPort}, fallback, spec.MixedPort, true
		}
		if port == 0 {
			port = 443
		}
		return endpoint, host, port, true
	case SharedInboundWebSocketOwner:
		host := strings.TrimSpace(spec.WSPublicHost)
		if host == "" {
			return PublicEndpoint{}, "", 0, false
		}
		port := spec.WSPublicPort
		if port == 0 {
			port = 443
		}
		return PublicEndpoint{Host: host, Port: port, TLS: true}, host, port, true
	default:
		return PublicEndpoint{}, "", 0, false
	}
}

// SharedInboundMember identifies one aggregate family and where it binds. The
// per-family membership itself is read from the persisted listener rows.
type SharedInboundMember struct {
	Owner       string
	Kind        string
	BindAddress string
	Port        int
}

// EnsureSharedInbounds converges the aggregate listener rows onto the current
// persisted membership and returns every aggregate the control plane manages.
//
// The persisted rows are the desired state: every listener marked with a
// shared-inbound owner and a non-aggregate name is a member, and every family
// with at least one member needs exactly one aggregate row. The call is
// therefore idempotent and safe after any mutation — creating a service adds a
// family, deleting the last member removes it, and changing the configured port
// repairs the existing aggregate in place.
func (s *Service) EnsureSharedInbounds(
	ctx context.Context,
	spec SharedInboundSpec,
	_ []store.ProxyGroupRecord,
	_ []SharedInboundMember,
) ([]Listener, error) {
	records, err := s.repository.ListListeners(ctx)
	if err != nil {
		return nil, err
	}
	aggregates := make(map[string]store.ListenerRecord, 4)
	families := make(map[string]SharedInboundMember, 4)
	familyOrder := make([]string, 0, 4)
	for _, record := range records {
		owner := SharedInboundOwnerOf(record)
		if owner == "" {
			continue
		}
		key := aggregateKey(owner, record.Kind)
		if isAggregateRecord(record) {
			aggregates[key] = record
			continue
		}
		if _, duplicate := families[key]; duplicate {
			continue
		}
		bindAddress, port := aggregateEndpoint(spec, owner, record.Kind)
		member := SharedInboundMember{Owner: owner, Kind: record.Kind, BindAddress: bindAddress, Port: port}
		if err := validateSharedMember(member, spec); err != nil {
			return nil, err
		}
		families[key] = member
		familyOrder = append(familyOrder, key)
	}
	sort.Strings(familyOrder)

	anchor, err := s.sharedInboundAnchorGroup(ctx)
	if err != nil {
		return nil, err
	}

	changed := false
	// An aggregate without members would keep the port bound forever, so it is
	// removed as soon as the last member leaves the family.
	for key, record := range aggregates {
		if _, wanted := families[key]; wanted {
			continue
		}
		if err := s.repository.DeleteListener(ctx, record.ID, record.Version); err != nil && err != store.ErrNotFound {
			return nil, mapStoreError(err)
		}
		delete(aggregates, key)
		changed = true
	}

	for _, key := range familyOrder {
		member := families[key]
		record, exists := aggregates[key]
		if !exists {
			if _, err := s.createSharedAggregate(ctx, member, anchor); err != nil {
				return nil, err
			}
			changed = true
			continue
		}
		if record.BindAddress == member.BindAddress && record.Port == member.Port && record.Enabled && record.ProxyGroupID == anchor {
			continue
		}
		record.BindAddress = member.BindAddress
		record.Port = member.Port
		record.ProxyGroupID = anchor
		record.Enabled = true
		record.UpdatedAt = s.now().UTC()
		if _, err := s.repository.UpdateListener(ctx, record, record.Version); err != nil {
			return nil, mapStoreError(err)
		}
		changed = true
	}
	// Publish the aggregate rows in the same call: a caller that just created
	// or removed the last member of a family must not have to apply twice, and
	// a settings change that only moves a port must not leave the data plane
	// binding the old one. The apply is content-hash deduplicated upstream, so
	// a no-op convergence is cheap.
	if changed && s.reconciler != nil {
		if err := s.reconciler.Apply(ctx); err != nil {
			return nil, fmt.Errorf("%w: publish shared inbound: %v", ErrApplyFailed, err)
		}
	}
	return s.aggregateListeners(ctx)
}

// SharedInboundAggregates returns the aggregate listeners currently published.
func (s *Service) SharedInboundAggregates(ctx context.Context) ([]Listener, error) {
	return s.aggregateListeners(ctx)
}

func (s *Service) aggregateListeners(ctx context.Context) ([]Listener, error) {
	records, err := s.repository.ListListeners(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]Listener, 0, 4)
	for _, record := range records {
		if isAggregateRecord(record) {
			result = append(result, fromRecord(record))
		}
	}
	return result, nil
}

// IsSharedInboundAggregate distinguishes the single aggregate row of a family
// from its members. Aggregate rows carry the deterministic managed name and
// never hold a credential of their own, so the compiler must not treat them as
// members when it collects usernames.
func IsSharedInboundAggregate(record store.ListenerRecord) bool {
	return SharedInboundOwnerOf(record) != "" && strings.HasPrefix(record.Name, sharedAggregateNamePrefix)
}

func isAggregateRecord(record store.ListenerRecord) bool {
	return IsSharedInboundAggregate(record)
}

func aggregateEndpoint(spec SharedInboundSpec, owner, kind string) (string, int) {
	if owner == SharedInboundStandardOwner && !isAdvancedKind(kind) {
		return spec.MixedBindAddress, spec.MixedPort
	}
	return "127.0.0.1", spec.WSPort
}

func (s *Service) createSharedAggregate(ctx context.Context, member SharedInboundMember, anchorGroupID string) (Listener, error) {
	id, err := newID()
	if err != nil {
		return Listener{}, err
	}
	transportJSON, endpointJSON, err := sharedAggregateEndpointConfig(member.Kind)
	if err != nil {
		return Listener{}, err
	}
	shareToken, err := newShareToken()
	if err != nil {
		return Listener{}, err
	}
	now := s.now().UTC()
	record := store.ListenerRecord{
		ID:                 id,
		Name:               sharedAggregateName(member.Owner, member.Kind),
		Kind:               member.Kind,
		BindAddress:        member.BindAddress,
		Port:               member.Port,
		ProxyGroupID:       anchorGroupID,
		AuthMode:           "none",
		TransportJSON:      transportJSON,
		PublicEndpointJSON: endpointJSON,
		ShareToken:         shareToken,
		SharedInbound:      member.Owner,
		Enabled:            true,
		Version:            1,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	created, err := s.repository.CreateListener(ctx, record)
	if err != nil {
		return Listener{}, mapStoreError(err)
	}
	return fromRecord(created), nil
}

// sharedInboundAnchorGroup resolves the proxy group referenced by an aggregate
// listener row. Mihomo's listener schema requires a valid proxy group name and
// the compiler refuses the whole configuration when one is missing, but every
// member is routed by an IN-USER rule that runs before the fallback, so the
// anchor itself never carries traffic. The first enabled group (by id, for a
// stable choice) is therefore safe.
func (s *Service) sharedInboundAnchorGroup(ctx context.Context) (string, error) {
	groups, err := s.repository.ListProxyGroups(ctx)
	if err != nil {
		return "", err
	}
	anchor := ""
	for _, group := range groups {
		if !group.Enabled {
			continue
		}
		if anchor == "" || group.ID < anchor {
			anchor = group.ID
		}
	}
	if anchor == "" {
		return "", fmt.Errorf("%w: create and enable at least one proxy group before using the shared inbound", ErrInvalid)
	}
	return anchor, nil
}

func sharedAggregateName(owner, kind string) string {
	return sharedAggregateNamePrefix + strings.TrimSpace(owner) + "-" + strings.ToLower(strings.TrimSpace(kind))
}

func sharedAggregateEndpointConfig(kind string) (string, string, error) {
	if !isAdvancedKind(kind) {
		return "{}", "{}", nil
	}
	path, err := NormalizeWebSocketPath(SharedInboundRoutePath())
	if err != nil {
		return "", "", err
	}
	encoded, err := json.Marshal(Transport{Type: "ws", WSPath: path})
	if err != nil {
		return "", "", fmt.Errorf("encode shared inbound transport: %w", err)
	}
	return string(encoded), "{}", nil
}

func aggregateKey(owner, kind string) string {
	return strings.ToLower(strings.TrimSpace(owner)) + "|" + strings.ToLower(strings.TrimSpace(kind))
}

func validateSharedMember(member SharedInboundMember, spec SharedInboundSpec) error {
	switch member.Owner {
	case SharedInboundStandardOwner:
		if isAdvancedKind(member.Kind) || member.Kind != "mixed" {
			return fmt.Errorf("%w: a standard shared-inbound member must be a mixed listener", ErrInvalid)
		}
		if !spec.Enabled {
			return fmt.Errorf("%w: the shared inbound is disabled", ErrInvalid)
		}
	case SharedInboundWebSocketOwner:
		if !isAdvancedKind(member.Kind) {
			return fmt.Errorf("%w: a WebSocket shared-inbound member must be vless, vmess, or trojan", ErrInvalid)
		}
		if !spec.IncludeWebSocket {
			return fmt.Errorf("%w: the shared WebSocket inbound is disabled", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown shared inbound owner %q", ErrInvalid, member.Owner)
	}
	return nil
}
