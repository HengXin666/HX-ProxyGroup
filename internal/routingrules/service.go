package routingrules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

const MetadataKey = "routing_rule_sets"

var ErrInvalid = errors.New("invalid routing rules")

type Repository interface {
	GetMetadata(context.Context, string) (string, error)
	SetMetadata(context.Context, string, string) error
	ListProxyGroups(context.Context) ([]store.ProxyGroupRecord, error)
}

type Reader interface {
	GetMetadata(context.Context, string) (string, error)
}

type Applier interface {
	Apply(context.Context) error
}

type Rule struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type Action struct {
	Type         string `json:"type"`
	ProxyGroupID string `json:"proxy_group_id,omitempty"`
}

type RuleSet struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Enabled         bool     `json:"enabled"`
	Priority        int      `json:"priority"`
	AppliedGroupIDs []string `json:"applied_group_ids"`
	Action          Action   `json:"action"`
	Rules           []Rule   `json:"rules"`
}

type Config struct {
	RuleSets []RuleSet `json:"rule_sets"`
}

func Default() Config {
	return Config{RuleSets: []RuleSet{}}
}

func Load(ctx context.Context, reader Reader) (Config, error) {
	value, err := reader.GetMetadata(ctx, MetadataKey)
	if errors.Is(err, store.ErrNotFound) {
		return Default(), nil
	}
	if err != nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal([]byte(value), &config); err != nil {
		return Config{}, fmt.Errorf("decode routing rules: %w", err)
	}
	if config.RuleSets == nil {
		config.RuleSets = []RuleSet{}
	}
	return config, nil
}

type Service struct {
	repository Repository
	applier    Applier
}

func NewService(repository Repository, applier Applier) (*Service, error) {
	if repository == nil {
		return nil, errors.New("routing rules repository is required")
	}
	return &Service{repository: repository, applier: applier}, nil
}

func (s *Service) Get(ctx context.Context) (Config, error) {
	return Load(ctx, s.repository)
}

func (s *Service) Update(ctx context.Context, config Config) (Config, error) {
	normalize(&config)
	groups, err := s.repository.ListProxyGroups(ctx)
	if err != nil {
		return Config{}, err
	}
	if err := Validate(config, groups); err != nil {
		return Config{}, err
	}
	previous, err := Load(ctx, s.repository)
	if err != nil {
		return Config{}, err
	}
	if err := save(ctx, s.repository, config); err != nil {
		return Config{}, err
	}
	if s.applier == nil {
		return config, nil
	}
	if err := s.applier.Apply(ctx); err != nil {
		rollbackErr := save(ctx, s.repository, previous)
		if rollbackErr == nil {
			rollbackErr = s.applier.Apply(ctx)
		}
		return Config{}, fmt.Errorf("apply routing rules: %w; rollback: %v", err, rollbackErr)
	}
	return config, nil
}

func save(ctx context.Context, repository Repository, config Config) error {
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode routing rules: %w", err)
	}
	return repository.SetMetadata(ctx, MetadataKey, string(encoded))
}

func Validate(config Config, groups []store.ProxyGroupRecord) error {
	if len(config.RuleSets) > 100 {
		return fmt.Errorf("%w: at most 100 rule sets are allowed", ErrInvalid)
	}
	groupIDs := make(map[string]struct{}, len(groups))
	for _, group := range groups {
		groupIDs[group.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(config.RuleSets))
	for _, set := range config.RuleSets {
		if !validID(set.ID) || len(set.Name) < 1 || len(set.Name) > 60 || set.Priority < 0 || set.Priority > 10000 {
			return fmt.Errorf("%w: rule set id, name, or priority is invalid", ErrInvalid)
		}
		if _, duplicate := seen[set.ID]; duplicate {
			return fmt.Errorf("%w: duplicate rule set id %q", ErrInvalid, set.ID)
		}
		seen[set.ID] = struct{}{}
		if len(set.Rules) < 1 || len(set.Rules) > 1000 {
			return fmt.Errorf("%w: rule set %q must contain 1-1000 rules", ErrInvalid, set.ID)
		}
		for _, groupID := range set.AppliedGroupIDs {
			if _, exists := groupIDs[groupID]; !exists {
				return fmt.Errorf("%w: rule set %q references missing applied group", ErrInvalid, set.ID)
			}
		}
		if set.Action.Type != "reject" && set.Action.Type != "direct" && set.Action.Type != "proxy_group" {
			return fmt.Errorf("%w: rule set %q action is invalid", ErrInvalid, set.ID)
		}
		if set.Action.Type == "proxy_group" {
			if _, exists := groupIDs[set.Action.ProxyGroupID]; !exists {
				return fmt.Errorf("%w: rule set %q target group is missing", ErrInvalid, set.ID)
			}
		}
		for _, rule := range set.Rules {
			if err := validateRule(rule); err != nil {
				return fmt.Errorf("%w: rule set %q: %v", ErrInvalid, set.ID, err)
			}
		}
	}
	return nil
}

func Compile(config Config, groups []store.ProxyGroupRecord, listeners []store.ListenerRecord, listenerName func(string) string) []string {
	groupNames := make(map[string]string, len(groups))
	for _, group := range groups {
		if group.Enabled {
			groupNames[group.ID] = group.Name
		}
	}
	sort.Slice(config.RuleSets, func(left, right int) bool {
		if config.RuleSets[left].Priority == config.RuleSets[right].Priority {
			return config.RuleSets[left].ID < config.RuleSets[right].ID
		}
		return config.RuleSets[left].Priority < config.RuleSets[right].Priority
	})
	compiled := make([]string, 0)
	for _, set := range config.RuleSets {
		if !set.Enabled {
			continue
		}
		action := actionName(set.Action, groupNames)
		inbounds := inboundNames(set.AppliedGroupIDs, listeners, listenerName)
		for _, rule := range set.Rules {
			matcher := matcherText(rule)
			if len(set.AppliedGroupIDs) == 0 {
				compiled = append(compiled, matcher+","+action)
				continue
			}
			for _, inbound := range inbounds {
				compiled = append(compiled, "AND,((IN-NAME,"+inbound+"),("+matcher+")),"+action)
			}
		}
	}
	return compiled
}

func actionName(action Action, groups map[string]string) string {
	switch action.Type {
	case "direct":
		return "DIRECT"
	case "proxy_group":
		if name := groups[action.ProxyGroupID]; name != "" {
			return name
		}
	}
	return "REJECT"
}

func inboundNames(groupIDs []string, listeners []store.ListenerRecord, name func(string) string) []string {
	selected := make(map[string]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		selected[id] = struct{}{}
	}
	result := make([]string, 0)
	for _, listener := range listeners {
		if listener.Enabled {
			if _, exists := selected[listener.ProxyGroupID]; exists {
				result = append(result, name(listener.ID))
			}
		}
	}
	sort.Strings(result)
	return result
}

func matcherText(rule Rule) string {
	return strings.ToUpper(strings.ReplaceAll(rule.Type, "_", "-")) + "," + rule.Value
}

func validateRule(rule Rule) error {
	value := strings.TrimSpace(rule.Value)
	if len(value) < 1 || len(value) > 253 || strings.ContainsAny(value, ",\r\n") {
		return errors.New("rule value is empty, too long, or contains a separator")
	}
	switch rule.Type {
	case "domain", "domain_suffix", "domain_keyword", "geosite", "geoip", "process_name":
		return nil
	case "ip_cidr":
		if _, _, err := net.ParseCIDR(value); err != nil {
			return errors.New("IP CIDR rule is invalid")
		}
		return nil
	case "network":
		if value != "tcp" && value != "udp" {
			return errors.New("network rule must be tcp or udp")
		}
		return nil
	case "dst_port":
		port, err := strconv.Atoi(value)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("destination port is invalid")
		}
		return nil
	default:
		return errors.New("rule type is unsupported")
	}
}

func normalize(config *Config) {
	for setIndex := range config.RuleSets {
		set := &config.RuleSets[setIndex]
		set.ID = strings.ToLower(strings.TrimSpace(set.ID))
		set.Name = strings.TrimSpace(set.Name)
		set.Action.Type = strings.ToLower(strings.TrimSpace(set.Action.Type))
		set.Action.ProxyGroupID = strings.TrimSpace(set.Action.ProxyGroupID)
		set.AppliedGroupIDs = uniqueSorted(set.AppliedGroupIDs)
		for ruleIndex := range set.Rules {
			set.Rules[ruleIndex].Type = strings.ToLower(strings.TrimSpace(set.Rules[ruleIndex].Type))
			set.Rules[ruleIndex].Value = strings.TrimSpace(set.Rules[ruleIndex].Value)
		}
	}
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validID(value string) bool {
	if len(value) < 1 || len(value) > 40 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}
