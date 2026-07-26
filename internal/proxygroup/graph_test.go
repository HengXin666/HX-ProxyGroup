package proxygroup

import (
	"strings"
	"testing"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func TestFindCycleDetectsLoops(t *testing.T) {
	edges := map[string][]string{
		"a": {"b"},
		"b": {"c"},
		"c": {"a"},
	}
	cycle := findCycle(edges, "a")
	if cycle == nil {
		t.Fatal("expected a cycle")
	}
	if cycle[0] != cycle[len(cycle)-1] {
		t.Fatalf("cycle should start and end at the same member: %v", cycle)
	}
}

func TestFindCycleAcceptsDiamonds(t *testing.T) {
	// a references b and c, both reference d: a valid DAG, not a cycle.
	edges := map[string][]string{
		"a": {"b", "c"},
		"b": {"d"},
		"c": {"d"},
		"d": nil,
	}
	if cycle := findCycle(edges, "a"); cycle != nil {
		t.Fatalf("diamond incorrectly reported as cycle: %v", cycle)
	}
}

func TestFindCycleDetectsSelfLoop(t *testing.T) {
	if cycle := findCycle(map[string][]string{"a": {"a"}}, "a"); cycle == nil {
		t.Fatal("expected self loop to be detected")
	}
}

func TestGroupEdgesSubstitutesCandidate(t *testing.T) {
	records := []store.ProxyGroupRecord{
		{ID: "a", SourceSpecJSON: `{"node_ids":[],"group_ids":["b"]}`},
		{ID: "b", SourceSpecJSON: `{"node_ids":[]}`},
	}
	edges := groupEdges(records, "b", SourceSpec{GroupIDs: []string{"a"}})
	cycle := findCycle(edges, "b")
	if cycle == nil {
		t.Fatal("expected cycle after substituting the candidate spec")
	}
	joined := strings.Join(cycle, "->")
	if !strings.Contains(joined, "a") || !strings.Contains(joined, "b") {
		t.Fatalf("unexpected cycle members: %v", cycle)
	}
}

func TestReferencedBy(t *testing.T) {
	records := []store.ProxyGroupRecord{
		{ID: "a", SourceSpecJSON: `{"node_ids":[],"group_ids":["c"]}`},
		{ID: "b", SourceSpecJSON: `{"node_ids":[],"group_ids":["c","a"]}`},
		{ID: "c", SourceSpecJSON: `{"node_ids":[]}`},
	}
	owners := referencedBy(records, "c")
	if len(owners) != 2 {
		t.Fatalf("owners = %v, want a and b", owners)
	}
	if owners := referencedBy(records, "b"); len(owners) != 0 {
		t.Fatalf("owners of b = %v, want none", owners)
	}
}
