package mihomo

import (
	"fmt"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/proxygroup"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

// validateResidentialDialerChains prevents a residential node from selecting a
// group that eventually contains the node's own channel group. Mihomo would
// otherwise recurse while trying to resolve dialer-proxy at runtime.
func validateResidentialDialerChains(
	groups []store.ProxyGroupRecord,
	nodeRecords []store.NodeConfigRecord,
	nodes map[string]compiledNode,
	candidates []store.GroupNodeCandidate,
	groupNameByID map[string]string,
) error {
	groupIDByName := make(map[string]string, len(groupNameByID))
	edges := make(map[string][]string, len(groupNameByID))
	members := make(map[string]map[string]struct{}, len(groupNameByID))
	for id, name := range groupNameByID {
		groupIDByName[name] = id
	}
	for _, record := range groups {
		if _, enabled := groupNameByID[record.ID]; !enabled {
			continue
		}
		spec, err := decodeSourceSpec(record.SourceSpecJSON)
		if err != nil {
			return fmt.Errorf("decode proxy group %q source spec: %w", record.Name, err)
		}
		edges[record.ID] = append([]string(nil), spec.GroupIDs...)
		set := make(map[string]struct{})
		for _, nodeID := range proxygroup.ResolveNodeIDs(spec, candidates) {
			set[nodeID] = struct{}{}
		}
		members[record.ID] = set
	}
	owners := make(map[string][]string)
	for groupID, nodeIDs := range members {
		for nodeID := range nodeIDs {
			owners[nodeID] = append(owners[nodeID], groupID)
		}
	}
	for _, record := range nodeRecords {
		compiled, exists := nodes[record.ID]
		if !exists {
			continue
		}
		targetName := strings.TrimSpace(stringValue(compiled.Config["dialer-proxy"]))
		if targetName == "" {
			continue
		}
		targetID, exists := groupIDByName[targetName]
		if !exists {
			return fmt.Errorf("residential node %q references missing dialer proxy group %q", record.DisplayName, targetName)
		}
		if groupContainsNode(targetID, record.ID, members, edges, make(map[string]struct{})) {
			return fmt.Errorf("residential node %q has a cyclic dialer-proxy chain through group %q", record.DisplayName, targetName)
		}
		for _, ownerID := range owners[record.ID] {
			if ownerID == targetID || groupReaches(edges, targetID, ownerID) {
				return fmt.Errorf("residential node %q has a cyclic dialer-proxy chain through group %q", record.DisplayName, targetName)
			}
		}
	}
	return nil
}

func groupReaches(edges map[string][]string, start, target string) bool {
	visited := make(map[string]struct{})
	var walk func(string) bool
	walk = func(current string) bool {
		if current == target {
			return true
		}
		if _, seen := visited[current]; seen {
			return false
		}
		visited[current] = struct{}{}
		for _, next := range edges[current] {
			if walk(next) {
				return true
			}
		}
		return false
	}
	return walk(start)
}

func groupContainsNode(
	groupID, nodeID string,
	members map[string]map[string]struct{},
	edges map[string][]string,
	visited map[string]struct{},
) bool {
	if _, seen := visited[groupID]; seen {
		return false
	}
	visited[groupID] = struct{}{}
	if _, exists := members[groupID][nodeID]; exists {
		return true
	}
	for _, referenced := range edges[groupID] {
		if groupContainsNode(referenced, nodeID, members, edges, visited) {
			return true
		}
	}
	return false
}

// compileResidentialClientRules runs before administrator routing rules so one
// authenticated logical session always reaches its assigned residential slot
// or DIRECT, independent of destination. Existing connections are explicitly
// drained by the service when this action changes.
func compileResidentialClientRules(
	routes []store.ResidentialClientRouteRecord,
	nodes map[string]compiledNode,
) ([]string, error) {
	nodeNames := make(map[string]string, len(nodes))
	for _, node := range nodes {
		nodeNames[strings.TrimPrefix(node.Name, "hx-node-")] = node.Name
	}
	compiled := make([]string, 0, len(routes))
	for _, route := range routes {
		if !route.ChannelEnabled {
			continue
		}
		action := "DIRECT"
		switch route.RouteMode {
		case "residential":
			fingerprint := strings.TrimSpace(route.NodeFingerprint)
			key := fingerprint
			if len(key) > 16 {
				key = key[:16]
			}
			var exists bool
			action, exists = nodeNames[key]
			if !exists || fingerprint == "" {
				return nil, fmt.Errorf(
					"residential client session %q references a missing pool slot",
					route.SessionID,
				)
			}
		case "upstream":
			action = strings.TrimSpace(route.UpstreamGroup)
			if action == "" {
				return nil, fmt.Errorf("residential client session %q has no available upstream group", route.SessionID)
			}
		case "direct":
		default:
			return nil, fmt.Errorf("residential client session %q has invalid route mode", route.SessionID)
		}
		compiled = append(compiled,
			"AND,((IN-NAME,"+listenerConfigName(route.ListenerID)+"),(IN-USER,"+route.AuthUsername+")),"+action,
		)
	}
	return compiled, nil
}
