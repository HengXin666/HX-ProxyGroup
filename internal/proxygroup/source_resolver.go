package proxygroup

import (
	"sort"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func ResolveNodeIDs(spec SourceSpec, candidates []store.GroupNodeCandidate) []string {
	resolved := deduplicateIDs(spec.NodeIDs)
	seen := make(map[string]struct{}, len(resolved))
	for _, id := range resolved {
		seen[id] = struct{}{}
	}
	if len(spec.SubscriptionIDs) == 0 {
		return resolved
	}
	selected := make([]store.GroupNodeCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if intersects(candidate.SubscriptionIDs, spec.SubscriptionIDs) && matchesCandidate(candidate, spec) {
			selected = append(selected, candidate)
		}
	}
	sortCandidates(selected, spec.SortBy)
	if spec.Limit > 0 && len(selected) > spec.Limit {
		selected = selected[:spec.Limit]
	}
	for _, candidate := range selected {
		if _, exists := seen[candidate.ID]; exists {
			continue
		}
		seen[candidate.ID] = struct{}{}
		resolved = append(resolved, candidate.ID)
	}
	return resolved
}

func matchesCandidate(candidate store.GroupNodeCandidate, spec SourceSpec) bool {
	if len(spec.Protocols) > 0 && !containsFold(spec.Protocols, candidate.Protocol) {
		return false
	}
	if len(spec.States) > 0 && !containsFold(spec.States, candidate.LifecycleState) {
		return false
	}
	if spec.MaxLatencyMS > 0 && (candidate.LastLatencyMS == nil || *candidate.LastLatencyMS > spec.MaxLatencyMS) {
		return false
	}
	name := strings.ToLower(candidate.DisplayName)
	if len(spec.NameKeywords) > 0 && !containsAny(name, spec.NameKeywords) {
		return false
	}
	if len(spec.Regions) == 0 {
		return true
	}
	for _, region := range spec.Regions {
		if containsAny(name, regionAliases(region)) {
			return true
		}
	}
	return false
}

func intersects(left, right []string) bool {
	for _, candidate := range left {
		if containsFold(right, candidate) {
			return true
		}
	}
	return false
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func containsAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}

func regionAliases(region string) []string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "jp", "japan", "日本":
		return []string{"jp", "japan", "日本", "东京", "大阪", "tokyo", "osaka"}
	case "hk", "hong kong", "香港":
		return []string{"hk", "hong kong", "香港"}
	case "tw", "taiwan", "台湾", "台灣":
		return []string{"tw", "taiwan", "台湾", "台灣", "台北", "taipei"}
	case "sg", "singapore", "新加坡":
		return []string{"sg", "singapore", "新加坡", "狮城"}
	case "us", "usa", "united states", "美国", "美國":
		return []string{"us", "usa", "united states", "美国", "美國", "洛杉矶", "los angeles", "san jose"}
	case "kr", "korea", "韩国", "韓國":
		return []string{"kr", "korea", "韩国", "韓國", "首尔", "seoul"}
	default:
		return []string{region}
	}
}

func sortCandidates(candidates []store.GroupNodeCandidate, sortBy string) {
	sort.SliceStable(candidates, func(left, right int) bool {
		if sortBy == "name" {
			return strings.ToLower(candidates[left].DisplayName) < strings.ToLower(candidates[right].DisplayName)
		}
		leftLatency := candidates[left].LastLatencyMS
		rightLatency := candidates[right].LastLatencyMS
		if leftLatency == nil {
			return false
		}
		if rightLatency == nil {
			return true
		}
		if *leftLatency != *rightLatency {
			return *leftLatency < *rightLatency
		}
		return candidates[left].ID < candidates[right].ID
	})
}
