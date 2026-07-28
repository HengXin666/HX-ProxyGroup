package proxygroup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

var (
	ErrNotFound = errors.New("proxy group not found")
	ErrConflict = errors.New("proxy group conflict")
	ErrInvalid  = errors.New("invalid proxy group")
)

const defaultTestURL = "http://cp.cloudflare.com/generate_204"

type Repository interface {
	CreateProxyGroup(context.Context, store.ProxyGroupRecord) (store.ProxyGroupRecord, error)
	GetProxyGroup(context.Context, string) (store.ProxyGroupRecord, error)
	ListProxyGroups(context.Context) ([]store.ProxyGroupRecord, error)
	UpdateProxyGroup(context.Context, store.ProxyGroupRecord, int) (store.ProxyGroupRecord, error)
	DeleteProxyGroup(context.Context, string, int) error
	ListNodeConfigs(context.Context, []string) ([]store.NodeConfigRecord, error)
	ListGroupNodeCandidates(context.Context) ([]store.GroupNodeCandidate, error)
	GetSubscription(context.Context, string) (store.SubscriptionRecord, error)
}

type Reconciler interface {
	Apply(context.Context) error
}

type SourceSpec struct {
	NodeIDs []string `json:"node_ids"`
	// GroupIDs reference other proxy groups as ordered members, enabling
	// serial (fallback chain) and parallel (url-test / load-balance)
	// composition. References must stay acyclic.
	GroupIDs        []string `json:"group_ids,omitempty"`
	SubscriptionIDs []string `json:"subscription_ids,omitempty"`
	NameKeywords    []string `json:"name_keywords,omitempty"`
	Regions         []string `json:"regions,omitempty"`
	Protocols       []string `json:"protocols,omitempty"`
	States          []string `json:"states,omitempty"`
	MaxLatencyMS    int      `json:"max_latency_ms,omitempty"`
	SortBy          string   `json:"sort_by,omitempty"`
	Limit           int      `json:"limit,omitempty"`
	IncludeDirect   bool     `json:"include_direct"`
	TestURL         string   `json:"test_url,omitempty"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
}

type Group struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Strategy         string     `json:"strategy"`
	SourceSpec       SourceSpec `json:"source_spec"`
	Enabled          bool       `json:"enabled"`
	EmptyBehavior    string     `json:"empty_behavior"`
	FallbackTargetID string     `json:"fallback_target_id,omitempty"`
	Version          int        `json:"version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

type CreateRequest struct {
	Name           string     `json:"name"`
	Strategy       string     `json:"strategy"`
	SourceSpec     SourceSpec `json:"source_spec"`
	Enabled        *bool      `json:"enabled,omitempty"`
	EmptyBehavior  string     `json:"empty_behavior,omitempty"`
	FallbackTarget string     `json:"fallback_target_id,omitempty"`
}

type UpdateRequest struct {
	Version        int        `json:"version"`
	Name           string     `json:"name"`
	Strategy       string     `json:"strategy"`
	SourceSpec     SourceSpec `json:"source_spec"`
	Enabled        bool       `json:"enabled"`
	EmptyBehavior  string     `json:"empty_behavior"`
	FallbackTarget string     `json:"fallback_target_id,omitempty"`
}

type Service struct {
	repository Repository
	reconciler Reconciler
	now        func() time.Time
}

func NewService(repository Repository, reconciler Reconciler) (*Service, error) {
	if repository == nil {
		return nil, errors.New("proxy group repository is required")
	}
	if reconciler == nil {
		return nil, errors.New("proxy group reconciler is required")
	}
	return &Service{repository: repository, reconciler: reconciler, now: time.Now}, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (Group, error) {
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	id, err := newID("group")
	if err != nil {
		return Group{}, err
	}
	normalized, err := s.normalize(ctx, id, request.Name, request.Strategy, request.SourceSpec, enabled, request.EmptyBehavior, request.FallbackTarget)
	if err != nil {
		return Group{}, err
	}
	now := s.now().UTC()
	record := store.ProxyGroupRecord{
		ID:               id,
		Name:             normalized.Name,
		Strategy:         normalized.Strategy,
		SourceSpecJSON:   normalized.SourceSpecJSON,
		RulePipelineJSON: "{}",
		Enabled:          normalized.Enabled,
		EmptyBehavior:    normalized.EmptyBehavior,
		FallbackTargetID: normalized.FallbackTargetID,
		Version:          1,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	created, err := s.repository.CreateProxyGroup(ctx, record)
	if err != nil {
		return Group{}, mapStoreError(err)
	}
	if err := s.reconciler.Apply(ctx); err != nil {
		return fromRecord(created), fmt.Errorf("proxy group saved but dataplane apply failed: %w", err)
	}
	return fromRecord(created), nil
}

func (s *Service) Get(ctx context.Context, id string) (Group, error) {
	record, err := s.repository.GetProxyGroup(ctx, id)
	if err != nil {
		return Group{}, mapStoreError(err)
	}
	return fromRecord(record), nil
}

func (s *Service) List(ctx context.Context) ([]Group, error) {
	records, err := s.repository.ListProxyGroups(ctx)
	if err != nil {
		return nil, err
	}
	groups := make([]Group, 0, len(records))
	for _, record := range records {
		groups = append(groups, fromRecord(record))
	}
	return groups, nil
}

func (s *Service) Update(ctx context.Context, id string, request UpdateRequest) (Group, error) {
	if request.Version < 1 {
		return Group{}, fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	normalized, err := s.normalize(ctx, id, request.Name, request.Strategy, request.SourceSpec, request.Enabled, request.EmptyBehavior, request.FallbackTarget)
	if err != nil {
		return Group{}, err
	}
	existing, err := s.repository.GetProxyGroup(ctx, id)
	if err != nil {
		return Group{}, mapStoreError(err)
	}
	existing.Name = normalized.Name
	existing.Strategy = normalized.Strategy
	existing.SourceSpecJSON = normalized.SourceSpecJSON
	existing.Enabled = normalized.Enabled
	existing.EmptyBehavior = normalized.EmptyBehavior
	existing.FallbackTargetID = normalized.FallbackTargetID
	existing.UpdatedAt = s.now().UTC()
	updated, err := s.repository.UpdateProxyGroup(ctx, existing, request.Version)
	if err != nil {
		return Group{}, mapStoreError(err)
	}
	if err := s.reconciler.Apply(ctx); err != nil {
		return fromRecord(updated), fmt.Errorf("proxy group saved but dataplane apply failed: %w", err)
	}
	return fromRecord(updated), nil
}

func (s *Service) Delete(ctx context.Context, id string, version int) error {
	if version < 1 {
		return fmt.Errorf("%w: version must be positive", ErrInvalid)
	}
	records, err := s.repository.ListProxyGroups(ctx)
	if err != nil {
		return err
	}
	if owners := referencedBy(records, id); len(owners) > 0 {
		names := make([]string, 0, len(owners))
		for _, record := range records {
			for _, owner := range owners {
				if record.ID == owner {
					names = append(names, record.Name)
				}
			}
		}
		return fmt.Errorf("%w: group is referenced by %s", ErrConflict, strings.Join(names, ", "))
	}
	if err := s.repository.DeleteProxyGroup(ctx, id, version); err != nil {
		return mapStoreError(err)
	}
	if err := s.reconciler.Apply(ctx); err != nil {
		return fmt.Errorf("proxy group deleted but dataplane apply failed: %w", err)
	}
	return nil
}

type normalizedGroup struct {
	Name             string
	Strategy         string
	SourceSpecJSON   string
	Enabled          bool
	EmptyBehavior    string
	FallbackTargetID string
}

func (s *Service) normalize(
	ctx context.Context,
	selfID string,
	name string,
	strategy string,
	spec SourceSpec,
	enabled bool,
	emptyBehavior string,
	fallbackTarget string,
) (normalizedGroup, error) {
	name = strings.TrimSpace(name)
	if len(name) < 1 || len(name) > 128 {
		return normalizedGroup{}, fmt.Errorf("%w: name must contain 1 to 128 characters", ErrInvalid)
	}
	for _, reserved := range []string{"DIRECT", "REJECT", "GLOBAL", "COMPATIBLE"} {
		if strings.EqualFold(name, reserved) {
			return normalizedGroup{}, fmt.Errorf("%w: name %q is reserved", ErrInvalid, name)
		}
	}
	strategy = strings.ToLower(strings.TrimSpace(strategy))
	if !validStrategy(strategy) {
		return normalizedGroup{}, fmt.Errorf("%w: unsupported strategy %q", ErrInvalid, strategy)
	}
	spec.NodeIDs = deduplicateIDs(spec.NodeIDs)
	spec.GroupIDs = deduplicateIDs(spec.GroupIDs)
	spec.SubscriptionIDs = deduplicateIDs(spec.SubscriptionIDs)
	if len(spec.NodeIDs) == 0 && len(spec.GroupIDs) == 0 && len(spec.SubscriptionIDs) == 0 && !spec.IncludeDirect {
		return normalizedGroup{}, fmt.Errorf("%w: at least one node, group, subscription, or DIRECT is required", ErrInvalid)
	}
	if len(spec.GroupIDs) > 0 {
		records, err := s.repository.ListProxyGroups(ctx)
		if err != nil {
			return normalizedGroup{}, err
		}
		known := make(map[string]store.ProxyGroupRecord, len(records))
		for _, record := range records {
			known[record.ID] = record
		}
		for _, groupID := range spec.GroupIDs {
			if groupID == selfID {
				return normalizedGroup{}, fmt.Errorf("%w: a group cannot reference itself", ErrInvalid)
			}
			referenced, exists := known[groupID]
			if !exists {
				return normalizedGroup{}, fmt.Errorf("%w: referenced group %q does not exist", ErrInvalid, groupID)
			}
			if !referenced.Enabled {
				return normalizedGroup{}, fmt.Errorf("%w: referenced group %q is disabled", ErrInvalid, referenced.Name)
			}
		}
		if cycle := findCycle(groupEdges(records, selfID, spec), selfID); cycle != nil {
			names := make([]string, 0, len(cycle))
			for _, member := range cycle {
				if record, exists := known[member]; exists {
					names = append(names, record.Name)
				} else if member == selfID {
					names = append(names, name)
				} else {
					names = append(names, member)
				}
			}
			return normalizedGroup{}, fmt.Errorf("%w: group references form a cycle: %s", ErrInvalid, strings.Join(names, " -> "))
		}
	}
	if len(spec.NodeIDs) > 0 {
		nodes, err := s.repository.ListNodeConfigs(ctx, spec.NodeIDs)
		if err != nil {
			return normalizedGroup{}, err
		}
		if len(nodes) != len(spec.NodeIDs) {
			return normalizedGroup{}, fmt.Errorf("%w: one or more nodes do not exist or are inactive", ErrInvalid)
		}
	}
	for _, subscriptionID := range spec.SubscriptionIDs {
		record, err := s.repository.GetSubscription(ctx, subscriptionID)
		if errors.Is(err, store.ErrNotFound) {
			return normalizedGroup{}, fmt.Errorf("%w: subscription %q does not exist", ErrInvalid, subscriptionID)
		}
		if err != nil {
			return normalizedGroup{}, err
		}
		if !record.Enabled {
			return normalizedGroup{}, fmt.Errorf("%w: subscription %q is disabled", ErrInvalid, subscriptionID)
		}
	}
	spec.NameKeywords = normalizeValues(spec.NameKeywords, false)
	spec.Regions = normalizeValues(spec.Regions, true)
	spec.Protocols = normalizeValues(spec.Protocols, true)
	spec.States = normalizeValues(spec.States, true)
	for _, state := range spec.States {
		if !validNodeState(state) {
			return normalizedGroup{}, fmt.Errorf("%w: unsupported node state %q", ErrInvalid, state)
		}
	}
	if spec.MaxLatencyMS < 0 || spec.MaxLatencyMS > 60000 {
		return normalizedGroup{}, fmt.Errorf("%w: max_latency_ms must be between 0 and 60000", ErrInvalid)
	}
	spec.SortBy = strings.ToLower(strings.TrimSpace(spec.SortBy))
	if spec.SortBy == "" && len(spec.SubscriptionIDs) > 0 {
		spec.SortBy = "latency"
	}
	if spec.SortBy != "" && spec.SortBy != "latency" && spec.SortBy != "name" {
		return normalizedGroup{}, fmt.Errorf("%w: sort_by must be latency or name", ErrInvalid)
	}
	if spec.Limit < 0 || spec.Limit > 500 {
		return normalizedGroup{}, fmt.Errorf("%w: limit must be between 0 and 500", ErrInvalid)
	}
	if strategy != "manual" {
		spec.TestURL = strings.TrimSpace(spec.TestURL)
		if spec.TestURL == "" {
			spec.TestURL = defaultTestURL
		}
		if !strings.HasPrefix(spec.TestURL, "http://") && !strings.HasPrefix(spec.TestURL, "https://") {
			return normalizedGroup{}, fmt.Errorf("%w: test_url must use HTTP or HTTPS", ErrInvalid)
		}
		if spec.IntervalSeconds == 0 {
			spec.IntervalSeconds = 300
		}
		if spec.IntervalSeconds < 30 || spec.IntervalSeconds > 86400 {
			return normalizedGroup{}, fmt.Errorf("%w: interval_seconds must be between 30 and 86400", ErrInvalid)
		}
	} else {
		spec.TestURL = ""
		spec.IntervalSeconds = 0
	}
	emptyBehavior = strings.ToLower(strings.TrimSpace(emptyBehavior))
	if emptyBehavior == "" {
		emptyBehavior = "fail-closed"
	}
	if emptyBehavior != "fail-closed" && emptyBehavior != "direct" {
		return normalizedGroup{}, fmt.Errorf("%w: unsupported empty_behavior %q", ErrInvalid, emptyBehavior)
	}
	fallbackTarget = strings.TrimSpace(fallbackTarget)
	encoded, err := json.Marshal(spec)
	if err != nil {
		return normalizedGroup{}, fmt.Errorf("encode source spec: %w", err)
	}
	return normalizedGroup{
		Name:             name,
		Strategy:         strategy,
		SourceSpecJSON:   string(encoded),
		Enabled:          enabled,
		EmptyBehavior:    emptyBehavior,
		FallbackTargetID: fallbackTarget,
	}, nil
}

func validStrategy(value string) bool {
	switch value {
	case "manual", "url-test", "fallback", "round-robin", "consistent-hashing", "sticky-sessions":
		return true
	default:
		return false
	}
}

func deduplicateIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeValues(values []string, lowercase bool) []string {
	values = deduplicateIDs(values)
	for index := range values {
		if lowercase {
			values[index] = strings.ToLower(values[index])
		}
	}
	return values
}

func validNodeState(value string) bool {
	switch value {
	case "candidate", "healthy", "degraded", "quarantined":
		return true
	default:
		return false
	}
}

func fromRecord(record store.ProxyGroupRecord) Group {
	var spec SourceSpec
	_ = json.Unmarshal([]byte(record.SourceSpecJSON), &spec)
	return Group{
		ID:               record.ID,
		Name:             record.Name,
		Strategy:         record.Strategy,
		SourceSpec:       spec,
		Enabled:          record.Enabled,
		EmptyBehavior:    record.EmptyBehavior,
		FallbackTargetID: record.FallbackTargetID,
		Version:          record.Version,
		CreatedAt:        record.CreatedAt,
		UpdatedAt:        record.UpdatedAt,
	}
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
