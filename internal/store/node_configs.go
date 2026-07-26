package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type NodeConfigRecord struct {
	ID                       string
	Fingerprint              string
	DisplayName              string
	Protocol                 string
	LifecycleState           string
	CanonicalConfigEncrypted []byte
}

type GroupNodeCandidate struct {
	NodeConfigRecord
	LastLatencyMS   *int
	SubscriptionIDs []string
}

func (s *Store) ListNodeConfigs(ctx context.Context, ids []string) ([]NodeConfigRecord, error) {
	conditions := []string{"lifecycle_state NOT IN ('disabled', 'retired')"}
	arguments := make([]any, 0, len(ids))
	if len(ids) > 0 {
		placeholders := make([]string, len(ids))
		for index, id := range ids {
			placeholders[index] = "?"
			arguments = append(arguments, id)
		}
		conditions = append(conditions, "id IN ("+strings.Join(placeholders, ",")+")")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, fingerprint, display_name, protocol, lifecycle_state, canonical_config_encrypted
FROM nodes
WHERE `+strings.Join(conditions, " AND ")+`
ORDER BY first_seen_at ASC, id ASC
`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list node configs: %w", err)
	}
	defer rows.Close()
	records := make([]NodeConfigRecord, 0)
	for rows.Next() {
		var record NodeConfigRecord
		if err := rows.Scan(
			&record.ID,
			&record.Fingerprint,
			&record.DisplayName,
			&record.Protocol,
			&record.LifecycleState,
			&record.CanonicalConfigEncrypted,
		); err != nil {
			return nil, fmt.Errorf("scan node config: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate node configs: %w", err)
	}
	return records, nil
}

func (s *Store) ListGroupNodeCandidates(ctx context.Context) ([]GroupNodeCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT
    n.id, n.fingerprint, n.display_name, n.protocol, n.lifecycle_state,
    n.canonical_config_encrypted, n.last_latency_ms, sn.subscription_id
FROM nodes n
JOIN subscription_nodes sn ON sn.node_id = n.id
JOIN subscription_snapshots ss ON ss.id = sn.snapshot_id AND ss.status = 'active'
WHERE n.lifecycle_state NOT IN ('disabled', 'retired')
ORDER BY n.id ASC, sn.subscription_id ASC
`)
	if err != nil {
		return nil, fmt.Errorf("list proxy group node candidates: %w", err)
	}
	defer rows.Close()

	items := make([]GroupNodeCandidate, 0)
	byID := make(map[string]int)
	for rows.Next() {
		var record GroupNodeCandidate
		var latency sql.NullInt64
		var subscriptionID string
		if err := rows.Scan(
			&record.ID,
			&record.Fingerprint,
			&record.DisplayName,
			&record.Protocol,
			&record.LifecycleState,
			&record.CanonicalConfigEncrypted,
			&latency,
			&subscriptionID,
		); err != nil {
			return nil, fmt.Errorf("scan proxy group node candidate: %w", err)
		}
		if index, exists := byID[record.ID]; exists {
			items[index].SubscriptionIDs = append(items[index].SubscriptionIDs, subscriptionID)
			continue
		}
		if latency.Valid {
			value := int(latency.Int64)
			record.LastLatencyMS = &value
		}
		record.SubscriptionIDs = []string{subscriptionID}
		byID[record.ID] = len(items)
		items = append(items, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy group node candidates: %w", err)
	}
	return items, nil
}
