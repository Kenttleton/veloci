package store

import (
	"context"
	"encoding/json"
)

// ConditionsForDisplay converts a conditions tree from engine Schema A format to the
// editor-friendly Schema B format, replacing label UUIDs with human-readable names.
//
// Schema A (DB/engine): {"op":"AND","children":[{"type":"payee_exact","value":"X"}]}
// Schema B (editor):    {"and":[{"payee_exact":"X"}]}
//
// Nodes already in Schema B (e.g. existing user-edited conditions) are passed through
// with recursion so any nested Schema A nodes are still converted.
func (s *Store) ConditionsForDisplay(ctx context.Context, entityID string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}

	labelsByID := map[string]string{}
	if labels, err := s.ListLabels(ctx, entityID, 1000, ""); err == nil {
		for _, l := range labels {
			labelsByID[l.ID] = l.Name
		}
	}

	var enrichNode func(node map[string]any) map[string]any
	enrichNode = func(node map[string]any) map[string]any {
		// Schema A logical node: {"op": "AND|OR|NOT|XOR", "children": [...]}
		if op, ok := node["op"].(string); ok {
			switch op {
			case "AND", "OR", "XOR":
				if arr, ok := node["children"].([]any); ok {
					enriched := make([]any, len(arr))
					for i, child := range arr {
						if childMap, ok := child.(map[string]any); ok {
							enriched[i] = enrichNode(childMap)
						} else {
							enriched[i] = child
						}
					}
					var key string
					switch op {
					case "AND":
						key = "and"
					case "OR":
						key = "or"
					case "XOR":
						key = "xor"
					}
					return map[string]any{key: enriched}
				}
			case "NOT":
				if arr, ok := node["children"].([]any); ok && len(arr) > 0 {
					if childMap, ok := arr[0].(map[string]any); ok {
						return map[string]any{"not": enrichNode(childMap)}
					}
				}
				if childMap, ok := node["child"].(map[string]any); ok {
					return map[string]any{"not": enrichNode(childMap)}
				}
			}
		}

		// Schema A leaf node: {"type": "<kind>", <field>: <value>}
		if nodeType, ok := node["type"].(string); ok {
			switch nodeType {
			case "payee_exact", "payee_contains", "payee_starts_with",
				"payee_ends_with", "payee_not_contains", "payee_regex":
				value, _ := node["value"].(string)
				return map[string]any{nodeType: value}
			case "label_matched":
				id, _ := node["label_id"].(string)
				name := id
				if n, found := labelsByID[id]; found {
					name = n
				}
				return map[string]any{"label_matched": name}
			case "entry_direction":
				direction, _ := node["direction"].(string)
				return map[string]any{"entry_direction": direction}
			case "entry_type":
				entryType, _ := node["entry_type"].(string)
				return map[string]any{"entry_type": entryType}
			case "account", "account_id":
				id, _ := node["value"].(string)
				return map[string]any{"account": id}
			case "recurrence_anchor":
				anchor, _ := node["recurrence_anchor"].(string)
				return map[string]any{"recurrence_anchor": anchor}
			}
			// Unknown Schema A type — pass through as-is.
			return node
		}

		// Schema B node (already in editor format) — recurse into logical containers
		// in case any nested children are still in Schema A format.
		out := copyMap(node)
		for key, val := range node {
			switch key {
			case "and", "or", "xor":
				if arr, ok := val.([]any); ok {
					enriched := make([]any, len(arr))
					for i, child := range arr {
						if childMap, ok := child.(map[string]any); ok {
							enriched[i] = enrichNode(childMap)
						} else {
							enriched[i] = child
						}
					}
					out[key] = enriched
				}
			case "not":
				if childMap, ok := val.(map[string]any); ok {
					out[key] = enrichNode(childMap)
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

// ConditionsForStorage converts editor Schema B format back to engine Schema A format,
// replacing label names with UUIDs (creating labels if needed).
//
// Also handles Schema A input gracefully (pass-through) for legacy stored conditions.
func (s *Store) ConditionsForStorage(ctx context.Context, entityID string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}

	var resolveErr error

	var resolveNode func(node map[string]any) map[string]any
	resolveNode = func(node map[string]any) map[string]any {
		if resolveErr != nil {
			return node
		}

		// Schema A passthrough — already in engine format; recurse for label resolution.
		if _, hasOp := node["op"]; hasOp {
			if arr, ok := node["children"].([]any); ok {
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
			if childMap, ok := node["child"].(map[string]any); ok {
				out := copyMap(node)
				out["child"] = resolveNode(childMap)
				return out
			}
			return node
		}
		if _, hasType := node["type"]; hasType {
			// Schema A leaf — pass through (label_id is already resolved).
			return node
		}

		// Schema B logical nodes → Schema A.
		for _, entry := range []struct{ key, op string }{
			{"and", "AND"}, {"or", "OR"}, {"xor", "XOR"},
		} {
			if val, ok := node[entry.key]; ok {
				if arr, ok := val.([]any); ok {
					resolved := make([]any, len(arr))
					for i, child := range arr {
						if childMap, ok := child.(map[string]any); ok {
							resolved[i] = resolveNode(childMap)
						} else {
							resolved[i] = child
						}
					}
					return map[string]any{"op": entry.op, "children": resolved}
				}
			}
		}
		if not, ok := node["not"]; ok {
			if childMap, ok := not.(map[string]any); ok {
				return map[string]any{"op": "NOT", "children": []any{resolveNode(childMap)}}
			}
		}

		// Schema B leaf nodes → Schema A.
		for key, val := range node {
			switch key {
			case "payee_exact", "payee_contains", "payee_starts_with",
				"payee_ends_with", "payee_not_contains", "payee_regex":
				return map[string]any{"type": key, "value": val}
			case "label_matched":
				name, _ := val.(string)
				labels, err := s.ListLabels(ctx, entityID, 1000, "")
				if err != nil {
					resolveErr = err
					return node
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
						return node
					}
					id = created.ID
				}
				return map[string]any{"type": "label_matched", "label_id": id}
			case "entry_direction":
				return map[string]any{"type": "entry_direction", "direction": val}
			case "entry_type":
				return map[string]any{"type": "entry_type", "entry_type": val}
			case "account":
				return map[string]any{"type": "account_id", "value": val}
			case "recurrence_anchor":
				return map[string]any{"type": "recurrence_anchor", "recurrence_anchor": val}
			}
		}

		// Unknown node — pass through.
		return node
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
	// Merchants contains distinct merchant_normalized strings from the entity's
	// transaction history, for autocomplete of payee_* condition values.
	Merchants []string `json:"merchants"`
	Labels    []string `json:"labels"`
}

// ListAutocompleteData returns merchant strings (from transaction history) and
// label names for the conditions editor's autocomplete.
func (s *Store) ListAutocompleteData(ctx context.Context, entityID string) (AutocompleteData, error) {
	var merchants []string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(array_agg(DISTINCT merchant_normalized ORDER BY merchant_normalized), '{}')
		FROM transactions
		WHERE entity_id = $1
		  AND merchant_normalized IS NOT NULL
		  AND merchant_normalized <> ''
	`, entityID).Scan(&merchants)
	if err != nil {
		return AutocompleteData{}, err
	}
	if merchants == nil {
		merchants = []string{}
	}

	labels, err := s.ListLabels(ctx, entityID, 1000, "")
	if err != nil {
		return AutocompleteData{}, err
	}

	labelNames := make([]string, len(labels))
	for i, l := range labels {
		labelNames[i] = l.Name
	}
	return AutocompleteData{Merchants: merchants, Labels: labelNames}, nil
}

// ListTransactionMerchants returns distinct merchant_normalized strings from
// transactions for the entity, optionally filtered by a query substring.
// Used to populate payee_* autocomplete in the conditions editor.
func (s *Store) ListTransactionMerchants(ctx context.Context, entityID, query string) ([]string, error) {
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
				  AND merchant_normalized <> ''
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
				  AND merchant_normalized <> ''
				  AND merchant_normalized ILIKE '%' || $2 || '%'
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

// ListUnaliasedTransactionMerchants is an alias for ListTransactionMerchants retained
// for handler compatibility. The "unaliased" concept was removed with canonical_merchants.
func (s *Store) ListUnaliasedTransactionMerchants(ctx context.Context, entityID, query string) ([]string, error) {
	return s.ListTransactionMerchants(ctx, entityID, query)
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
