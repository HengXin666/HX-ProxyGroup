package proxygroup

import (
	"encoding/json"

	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

// groupEdges builds the group-reference adjacency list (group id -> referenced
// group ids) from stored records, substituting the candidate's pending spec so
// validation sees the state after the write would land.
func groupEdges(records []store.ProxyGroupRecord, candidateID string, candidate SourceSpec) map[string][]string {
	edges := make(map[string][]string, len(records)+1)
	for _, record := range records {
		if record.ID == candidateID {
			continue
		}
		var spec SourceSpec
		if err := json.Unmarshal([]byte(record.SourceSpecJSON), &spec); err != nil {
			continue
		}
		edges[record.ID] = spec.GroupIDs
	}
	edges[candidateID] = candidate.GroupIDs
	return edges
}

// findCycle returns a reference path that loops back onto itself, starting the
// search from the given group, or nil when the graph below it is acyclic.
func findCycle(edges map[string][]string, start string) []string {
	const (
		visiting = 1
		done     = 2
	)
	states := make(map[string]int, len(edges))
	var path []string
	var walk func(id string) []string
	walk = func(id string) []string {
		states[id] = visiting
		path = append(path, id)
		for _, next := range edges[id] {
			switch states[next] {
			case visiting:
				// Trim the path down to where the loop begins.
				for index, member := range path {
					if member == next {
						return append(append([]string(nil), path[index:]...), next)
					}
				}
				return append(append([]string(nil), path...), next)
			case done:
				continue
			default:
				if cycle := walk(next); cycle != nil {
					return cycle
				}
			}
		}
		states[id] = done
		path = path[:len(path)-1]
		return nil
	}
	return walk(start)
}

// referencedBy lists the ids of groups whose spec references the target group.
func referencedBy(records []store.ProxyGroupRecord, targetID string) []string {
	var owners []string
	for _, record := range records {
		if record.ID == targetID {
			continue
		}
		var spec SourceSpec
		if err := json.Unmarshal([]byte(record.SourceSpecJSON), &spec); err != nil {
			continue
		}
		for _, id := range spec.GroupIDs {
			if id == targetID {
				owners = append(owners, record.ID)
				break
			}
		}
	}
	return owners
}
