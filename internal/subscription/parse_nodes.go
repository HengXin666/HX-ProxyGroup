package subscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/HengXin666/HX-ProxyGroup/internal/nodeparse"
	"github.com/HengXin666/HX-ProxyGroup/internal/store"
)

func (s *Service) parseNodeImports(content []byte, contentType string) (ParseSummary, []store.NodeImportRecord, error) {
	if s.parser == nil {
		summary := inspectSource(content, contentType)
		return summary, nil, nil
	}
	parsed, err := s.parser(content)
	if err != nil {
		return ParseSummary{}, nil, fmt.Errorf("parse subscription nodes: %w", err)
	}
	unique := make(map[string]nodeparse.Node, len(parsed.Nodes))
	for _, node := range parsed.Nodes {
		if _, exists := unique[node.Fingerprint]; !exists {
			unique[node.Fingerprint] = node
		}
	}
	if len(unique) == 0 {
		return ParseSummary{}, nil, errors.New("subscription contains no valid nodes")
	}
	imports := make([]store.NodeImportRecord, 0, len(unique))
	for _, node := range unique {
		encoded, err := json.Marshal(node.Canonical)
		if err != nil {
			return ParseSummary{}, nil, fmt.Errorf("encode canonical node: %w", err)
		}
		encrypted, err := s.cipher.Seal(encoded, nodeAssociatedData(node.Fingerprint))
		if err != nil {
			return ParseSummary{}, nil, fmt.Errorf("encrypt canonical node: %w", err)
		}
		nodeID, err := newNodeID()
		if err != nil {
			return ParseSummary{}, nil, err
		}
		imports = append(imports, store.NodeImportRecord{
			ID:                       nodeID,
			Fingerprint:              node.Fingerprint,
			DisplayName:              node.DisplayName,
			Protocol:                 node.Protocol,
			CanonicalConfigEncrypted: encrypted,
			SourceName:               node.DisplayName,
		})
	}
	return ParseSummary{
		DetectedFormat: parsed.DetectedFormat,
		EstimatedNodes: len(imports),
		ParsedNodes:    len(imports),
		FailedNodes:    len(parsed.Failures),
		Failures:       parsed.Failures,
	}, imports, nil
}

func newNodeID() (string, error) {
	id, err := newID()
	if err != nil {
		return "", err
	}
	return "node-" + strings.TrimPrefix(id, "sub-"), nil
}

func nodeAssociatedData(fingerprint string) []byte {
	return []byte("node:" + fingerprint)
}
