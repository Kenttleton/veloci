package store

import (
	"context"
	"encoding/json"
)

// EnrichConditions walks a conditions tree and replaces machine IDs with human-readable
// names so the frontend can display and edit them without knowing UUIDs.
//
//   canonical_merchant: canonical_merchant_id → merchant + aliases
//   label:              label_id              → label (name string)
//
// Nodes that cannot be resolved (missing from DB) are left with their ID intact.
func (s *Store) EnrichConditions(ctx context.Context, entityID string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	// Build lookup maps in one pair of queries.
	merchantsByID := map[string]CanonicalMerchantWithCounts{}
	aliasesByMerchantID := map[string][]string{}
	labelsByID := map[string]string{}

	if merchants, err := s.ListCanonicalMerchants(ctx, entityID, 1000, ""); err == nil {
		for _, m := range merchants {
			merchantsByID[m.ID] = m
		}
		if aliases, err := s.pool.Query(ctx, `
			SELECT canonical_merchant_id::text, normalized_name
			FROM canonical_merchant_aliases
			WHERE entity_id = $1
			ORDER BY normalized_name
		`, entityID); err == nil {
			for aliases.Next() {
				var mid, name string
				if aliases.Scan(&mid, &name) == nil {
					aliasesByMerchantID[mid] = append(aliasesByMerchantID[mid], name)
				}
			}
			aliases.Close()
		}
	}

	if labels, err := s.ListLabels(ctx, entityID, 1000, ""); err == nil {
		for _, l := range labels {
			labelsByID[l.ID] = l.Name
		}
	}

	var enrichNode func(node map[string]any) map[string]any
	enrichNode = func(node map[string]any) map[string]any {
		// Logical node — recurse into children.
		if children, ok := node["children"]; ok {
			if arr, ok := children.([]any); ok {
				enriched := make([]any, len(arr))
				for i, child := range arr {
					if childMap, ok := child.(map[string]any); ok {
						enriched[i] = enrichNode(childMap)
					} else {
						enriched[i] = child
					}
				}
				out := copyMap(node)
				out["children"] = enriched
				return out
			}
		}

		nodeType, _ := node["type"].(string)
		out := copyMap(node)

		switch nodeType {
		case "canonical_merchant":
			if id, ok := node["canonical_merchant_id"].(string); ok {
				if m, found := merchantsByID[id]; found {
					delete(out, "canonical_merchant_id")
					out["merchant"] = m.Name
					out["aliases"] = aliasesByMerchantID[id]
				}
			}
		case "label":
			if id, ok := node["label_id"].(string); ok {
				if name, found := labelsByID[id]; found {
					delete(out, "label_id")
					out["label"] = name
				}
			}
		}
		return out
	}

	enriched := enrichAny(raw, enrichNode)
	if b, err := json.Marshal(enriched); err == nil {
		return b
	}
	return raw
}

// ResolveConditions is the inverse of EnrichConditions: replaces human-readable names
// with machine IDs before storing to the DB or passing to the engine.
//
// If a merchant name is not found, it is created (source='user') before the ID is inserted.
// If a label name is not found, it is created before the ID is inserted.
func (s *Store) ResolveConditions(ctx context.Context, entityID string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}

	var resolveErr error

	var resolveNode func(node map[string]any) map[string]any
	resolveNode = func(node map[string]any) map[string]any {
		if resolveErr != nil {
			return node
		}

		// Logical node — recurse.
		if children, ok := node["children"]; ok {
			if arr, ok := children.([]any); ok {
				resolved := make([]any, len(arr))
				for i, child := range arr {
					if childMap, ok := child.(map[string]any); ok {
						resolved[i] = resolveNode(childMap)
					} else {
						resolved[i] = child
					}
				}
				out := copyMap(node)
				out["children"] = resolved
				return out
			}
		}

		nodeType, _ := node["type"].(string)
		out := copyMap(node)

		switch nodeType {
		case "canonical_merchant":
			if name, ok := node["merchant"].(string); ok && name != "" {
				// Look up or create the canonical merchant.
				merchants, err := s.ListCanonicalMerchants(ctx, entityID, 1000, "")
				if err != nil {
					resolveErr = err
					return out
				}
				var id string
				for _, m := range merchants {
					if m.Name == name {
						id = m.ID
						break
					}
				}
				if id == "" {
					created, err := s.CreateCanonicalMerchant(ctx, entityID, name)
					if err != nil {
						resolveErr = err
						return out
					}
					id = created.ID
				}
				delete(out, "merchant")
				delete(out, "aliases")
				out["canonical_merchant_id"] = id
			}
		case "label":
			if name, ok := node["label"].(string); ok && name != "" {
				labels, err := s.ListLabels(ctx, entityID, 1000, "")
				if err != nil {
					resolveErr = err
					return out
				}
				var id string
				for _, l := range labels {
					if l.Name == name {
						id = l.ID
						break
					}
				}
				if id == "" {
					created, err := s.CreateLabel(ctx, entityID, name)
					if err != nil {
						resolveErr = err
						return out
					}
					id = created.ID
				}
				delete(out, "label")
				out["label_id"] = id
			}
		}
		return out
	}

	resolved := enrichAny(raw, resolveNode)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if b, err := json.Marshal(resolved); err != nil {
		return nil, err
	} else {
		return b, nil
	}
}

// AutocompleteData holds the names available for conditions autocomplete.
type AutocompleteData struct {
	Merchants []string `json:"merchants"`
	Labels    []string `json:"labels"`
}

// ListAutocompleteData returns merchant and label names for the editor's autocomplete.
func (s *Store) ListAutocompleteData(ctx context.Context, entityID string) (AutocompleteData, error) {
	merchants, err := s.ListCanonicalMerchants(ctx, entityID, 1000, "")
	if err != nil {
		return AutocompleteData{}, err
	}
	labels, err := s.ListLabels(ctx, entityID, 1000, "")
	if err != nil {
		return AutocompleteData{}, err
	}

	names := make([]string, len(merchants))
	for i, m := range merchants {
		names[i] = m.Name
	}
	labelNames := make([]string, len(labels))
	for i, l := range labels {
		labelNames[i] = l.Name
	}
	return AutocompleteData{Merchants: names, Labels: labelNames}, nil
}

// ListUnaliasedTransactionMerchants returns distinct merchant_normalized strings from
// transactions for the entity that are not yet mapped to a canonical merchant alias.
// Used to populate the alias search in the Configuration merchants tab.
func (s *Store) ListUnaliasedTransactionMerchants(ctx context.Context, entityID, query string) ([]string, error) {
	var rows []string
	var err error

	if query == "" {
		err = s.pool.QueryRow(ctx, `
			SELECT COALESCE(array_agg(t.merchant_normalized ORDER BY cnt DESC), '{}')
			FROM (
				SELECT merchant_normalized, COUNT(*) AS cnt
				FROM transactions
				WHERE entity_id = $1
				  AND merchant_normalized IS NOT NULL
				  AND merchant_normalized NOT IN (
					SELECT normalized_name FROM canonical_merchant_aliases WHERE entity_id = $1
				  )
				GROUP BY merchant_normalized
				LIMIT 200
			) t
		`, entityID).Scan(&rows)
	} else {
		err = s.pool.QueryRow(ctx, `
			SELECT COALESCE(array_agg(t.merchant_normalized ORDER BY cnt DESC), '{}')
			FROM (
				SELECT merchant_normalized, COUNT(*) AS cnt
				FROM transactions
				WHERE entity_id = $1
				  AND merchant_normalized IS NOT NULL
				  AND merchant_normalized ILIKE '%' || $2 || '%'
				  AND merchant_normalized NOT IN (
					SELECT normalized_name FROM canonical_merchant_aliases WHERE entity_id = $1
				  )
				GROUP BY merchant_normalized
				LIMIT 50
			) t
		`, entityID, query).Scan(&rows)
	}
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []string{}
	}
	return rows, nil
}

// ── helpers ────────────────────────────────────────────────────────────────

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func enrichAny(raw json.RawMessage, fn func(map[string]any) map[string]any) any {
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil
	}
	return walkAny(node, fn)
}

func walkAny(node any, fn func(map[string]any) map[string]any) any {
	switch v := node.(type) {
	case map[string]any:
		return fn(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = walkAny(item, fn)
		}
		return out
	default:
		return node
	}
}
