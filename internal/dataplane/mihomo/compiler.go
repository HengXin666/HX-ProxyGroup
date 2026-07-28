package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/listener"
	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
	"github.com/HengXin666/HX-ProxyGroup/internal/systemsettings"
	"gopkg.in/yaml.v3"
)

type Repository interface {
	ListProxyGroups(context.Context) ([]store.ProxyGroupRecord, error)
	ListListeners(context.Context) ([]store.ListenerRecord, error)
	ListNodeConfigs(context.Context, []string) ([]store.NodeConfigRecord, error)
	ListGroupNodeCandidates(context.Context) ([]store.GroupNodeCandidate, error)
	GetMetadata(context.Context, string) (string, error)
}

type Cipher interface {
	Open([]byte, []byte) ([]byte, error)
}

type Endpoint struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	BindAddress string `json:"bind_address"`
	Port        int    `json:"port"`
}

type Compiled struct {
	YAML             []byte
	Endpoints        []Endpoint
	ProxyCount       int
	ControllerSocket string
}

type Compiler struct {
	repository       Repository
	cipher           Cipher
	controllerSocket string
}

func NewCompiler(repository Repository, cipher Cipher) (*Compiler, error) {
	if repository == nil {
		return nil, errors.New("mihomo compiler repository is required")
	}
	if cipher == nil {
		return nil, errors.New("mihomo compiler cipher is required")
	}
	return &Compiler{repository: repository, cipher: cipher}, nil
}

func (c *Compiler) setControllerSocket(path string) {
	c.controllerSocket = strings.TrimSpace(path)
}

func (c *Compiler) Compile(ctx context.Context) (Compiled, error) {
	settings, err := systemsettings.Load(ctx, c.repository)
	if err != nil {
		return Compiled{}, fmt.Errorf("load global settings: %w", err)
	}
	groups, err := c.repository.ListProxyGroups(ctx)
	if err != nil {
		return Compiled{}, err
	}
	listeners, err := c.repository.ListListeners(ctx)
	if err != nil {
		return Compiled{}, err
	}
	enabledGroups := make(map[string]store.ProxyGroupRecord)
	groupNames := make(map[string]string)
	for _, group := range groups {
		if !group.Enabled {
			continue
		}
		if _, exists := groupNames[group.Name]; exists {
			return Compiled{}, fmt.Errorf("duplicate enabled proxy group name %q", group.Name)
		}
		enabledGroups[group.ID] = group
		groupNames[group.Name] = group.ID
	}
	nodeRecords, err := c.repository.ListNodeConfigs(ctx, nil)
	if err != nil {
		return Compiled{}, err
	}
	candidates, err := c.repository.ListGroupNodeCandidates(ctx)
	if err != nil {
		return Compiled{}, err
	}
	nodeByID := make(map[string]compiledNode, len(nodeRecords))
	proxies := make([]map[string]any, 0, len(nodeRecords))
	for _, record := range nodeRecords {
		proxyName := nodeProxyName(record.Fingerprint)
		config, err := c.decryptNode(record, proxyName)
		if err != nil {
			return Compiled{}, err
		}
		nodeByID[record.ID] = compiledNode{Name: proxyName, Config: config}
		proxies = append(proxies, config)
	}
	sort.Slice(proxies, func(left, right int) bool {
		return fmt.Sprint(proxies[left]["name"]) < fmt.Sprint(proxies[right]["name"])
	})

	groupNameByID := make(map[string]string, len(enabledGroups))
	for id, record := range enabledGroups {
		groupNameByID[id] = record.Name
	}
	proxyGroups := make([]map[string]any, 0, len(enabledGroups))
	for _, record := range sortGroupsByDependency(groups) {
		if !record.Enabled {
			continue
		}
		compiledGroup, err := compileGroup(record, nodeByID, candidates, groupNameByID)
		if err != nil {
			return Compiled{}, err
		}
		proxyGroups = append(proxyGroups, compiledGroup)
	}

	listenerConfigs := make([]map[string]any, 0, len(listeners))
	endpoints := make([]Endpoint, 0, len(listeners))
	usedEndpoints := make(map[string]string)
	for _, record := range listeners {
		if !record.Enabled {
			continue
		}
		group, exists := enabledGroups[record.ProxyGroupID]
		if !exists {
			return Compiled{}, fmt.Errorf("listener %q references a missing or disabled proxy group", record.Name)
		}
		endpointKey := fmt.Sprintf("%s:%d", record.BindAddress, record.Port)
		if other, exists := usedEndpoints[endpointKey]; exists {
			return Compiled{}, fmt.Errorf("listeners %q and %q use the same endpoint %s", other, record.Name, endpointKey)
		}
		usedEndpoints[endpointKey] = record.Name
		config, err := c.compileListener(record, group.Name)
		if err != nil {
			return Compiled{}, err
		}
		listenerConfigs = append(listenerConfigs, config)
		endpoints = append(endpoints, Endpoint{
			ID:          record.ID,
			Name:        record.Name,
			Kind:        record.Kind,
			BindAddress: record.BindAddress,
			Port:        record.Port,
		})
	}

	document := map[string]any{
		"mode":      "rule",
		"log-level": settings.Performance.LogLevel,
		"ipv6":      settings.DNS.IPv6,
		"allow-lan": false,
		"dns": map[string]any{
			"enable":             settings.DNS.Enabled,
			"ipv6":               settings.DNS.IPv6,
			"enhanced-mode":      settings.DNS.EnhancedMode,
			"default-nameserver": settings.DNS.DefaultNameserver,
			"nameserver":         settings.DNS.Nameserver,
			"fallback":           settings.DNS.Fallback,
		},
		"tcp-concurrent":      settings.Performance.TCPConcurrent,
		"unified-delay":       settings.Performance.UnifiedDelay,
		"keep-alive-idle":     settings.Performance.KeepAliveIdle,
		"keep-alive-interval": settings.Performance.KeepAliveInterval,
		"find-process-mode":   settings.Performance.FindProcessMode,
		"proxies":             proxies,
		"proxy-groups":        proxyGroups,
		"listeners":           listenerConfigs,
		"rules":               []string{"MATCH,DIRECT"},
	}
	if c.controllerSocket != "" {
		document["external-controller-unix"] = c.controllerSocket
	}
	encoded, err := yaml.Marshal(document)
	if err != nil {
		return Compiled{}, fmt.Errorf("encode mihomo configuration: %w", err)
	}
	return Compiled{
		YAML:             encoded,
		Endpoints:        endpoints,
		ProxyCount:       len(proxies),
		ControllerSocket: c.controllerSocket,
	}, nil
}

type compiledNode struct {
	Name   string
	Config map[string]any
}

func decodeSourceSpec(value string) (proxygroup.SourceSpec, error) {
	var spec proxygroup.SourceSpec
	if err := json.Unmarshal([]byte(value), &spec); err != nil {
		return proxygroup.SourceSpec{}, err
	}
	return spec, nil
}

// sortGroupsByDependency orders groups so every referenced group is emitted
// before the groups referencing it, keeping the compiled YAML stable and
// friendly to strict readers. Cycles cannot occur because the service rejects
// them at write time; unknown references keep their original position.
func sortGroupsByDependency(groups []store.ProxyGroupRecord) []store.ProxyGroupRecord {
	index := make(map[string]int, len(groups))
	for position, group := range groups {
		index[group.ID] = position
	}
	visited := make([]bool, len(groups))
	ordered := make([]store.ProxyGroupRecord, 0, len(groups))
	var visit func(position int)
	visit = func(position int) {
		if visited[position] {
			return
		}
		visited[position] = true
		spec, err := decodeSourceSpec(groups[position].SourceSpecJSON)
		if err == nil {
			for _, referenced := range spec.GroupIDs {
				if referencedPosition, exists := index[referenced]; exists {
					visit(referencedPosition)
				}
			}
		}
		ordered = append(ordered, groups[position])
	}
	for position := range groups {
		visit(position)
	}
	return ordered
}

func compileGroup(record store.ProxyGroupRecord, nodes map[string]compiledNode, candidates []store.GroupNodeCandidate, groupNameByID map[string]string) (map[string]any, error) {
	spec, err := decodeSourceSpec(record.SourceSpecJSON)
	if err != nil {
		return nil, fmt.Errorf("decode group %q source spec: %w", record.Name, err)
	}
	resolvedNodeIDs := proxygroup.ResolveNodeIDs(spec, candidates)
	members := make([]string, 0, len(spec.GroupIDs)+len(resolvedNodeIDs)+1)
	seen := make(map[string]struct{}, len(spec.GroupIDs)+len(resolvedNodeIDs)+1)
	for _, groupID := range spec.GroupIDs {
		name, exists := groupNameByID[groupID]
		if !exists {
			// The referenced group was disabled after this spec was saved;
			// drop the member instead of failing the whole compilation.
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		members = append(members, name)
	}
	for _, nodeID := range resolvedNodeIDs {
		node, exists := nodes[nodeID]
		if !exists {
			continue
		}
		if _, duplicate := seen[node.Name]; duplicate {
			continue
		}
		seen[node.Name] = struct{}{}
		members = append(members, node.Name)
	}
	if spec.IncludeDirect {
		members = append(members, "DIRECT")
	}
	if len(members) == 0 {
		if record.EmptyBehavior == "direct" {
			members = append(members, "DIRECT")
		} else {
			members = append(members, "REJECT")
		}
	}
	group := map[string]any{
		"name":    record.Name,
		"proxies": members,
	}
	switch record.Strategy {
	case "manual":
		group["type"] = "select"
	case "url-test", "fallback":
		group["type"] = record.Strategy
		group["url"] = spec.TestURL
		group["interval"] = spec.IntervalSeconds
		group["lazy"] = true
	case "round-robin", "consistent-hashing", "sticky-sessions":
		group["type"] = "load-balance"
		group["strategy"] = record.Strategy
		group["url"] = spec.TestURL
		group["interval"] = spec.IntervalSeconds
		group["lazy"] = true
	default:
		return nil, fmt.Errorf("proxy group %q uses unsupported strategy %q", record.Name, record.Strategy)
	}
	return group, nil
}

func (c *Compiler) compileListener(record store.ListenerRecord, groupName string) (map[string]any, error) {
	config := map[string]any{
		"name":   listenerConfigName(record.ID),
		"type":   record.Kind,
		"listen": record.BindAddress,
		"port":   record.Port,
		"udp":    true,
		"proxy":  groupName,
		"users":  []any{},
	}
	if record.AuthMode == "userpass" {
		plaintext, err := c.cipher.Open(record.AuthConfigEncrypted, []byte("listener:"+record.ID))
		if err != nil {
			return nil, fmt.Errorf("decrypt listener %q auth: %w", record.Name, err)
		}
		var auth listener.Auth
		if err := json.Unmarshal(plaintext, &auth); err != nil {
			return nil, fmt.Errorf("decode listener %q auth: %w", record.Name, err)
		}
		if auth.Username == "" || auth.Password == "" {
			return nil, fmt.Errorf("listener %q has incomplete authentication", record.Name)
		}
		config["users"] = []map[string]string{{
			"username": auth.Username,
			"password": auth.Password,
		}}
	}
	return config, nil
}

func nodeProxyName(fingerprint string) string {
	fingerprint = strings.TrimSpace(fingerprint)
	if len(fingerprint) > 16 {
		fingerprint = fingerprint[:16]
	}
	return "hx-node-" + fingerprint
}

func listenerConfigName(id string) string {
	id = strings.TrimPrefix(id, "listener-")
	if len(id) > 16 {
		id = id[:16]
	}
	return "hx-in-" + id
}
