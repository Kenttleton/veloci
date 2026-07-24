package store

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ── Schema reference ───────────────────────────────────────────────────────
//
// Schema A (DB/engine): typed nodes with explicit "op"/"type" fields.
//   {"op":"AND","children":[{"type":"payee_exact","value":"X"}]}
//
// Schema B (editor/API): compact key-value form; names instead of UUIDs.
//   {"and":[{"payee_exact":"X"}]}
//
// displayNode converts A → B; storageNode converts B → A.
// Both are extracted as package-level functions so conditions_test.go can
// call them directly with pre-populated lookup maps (no DB required).

// ── Lookup structs ─────────────────────────────────────────────────────────

// displayLookups holds name maps needed to convert Schema A UUIDs → names.
type displayLookups struct {
	labelsByID   map[string]string // UUID → name
	accountsByID map[string]string // UUID → name
	instByID     map[string]string // UUID → institution_name
}

// storageLookups holds ID maps needed to convert Schema B names → UUIDs.
type storageLookups struct {
	accountsByName map[string]string // lower(name) → UUID
	accountsByID   map[string]bool   // UUID → exists (legacy UUID passthrough)
	instByName     map[string]string // lower(institution_name) → UUID
	instByID       map[string]bool   // UUID → exists
}

// ── displayNode (Schema A → Schema B) ─────────────────────────────────────

// displayNode converts a single Schema A node map to Schema B.
// Unresolvable UUIDs are passed through as-is.
func displayNode(node map[string]any, lu displayLookups) map[string]any {
	resolve := func(id string, byID map[string]string) string {
		if name, ok := byID[id]; ok {
			return name
		}
		return id
	}

	// ── Schema A logical node ──────────────────────────────────────────
	if op, ok := node["op"].(string); ok {
		switch op {
		case "AND", "OR", "XOR":
			if arr, ok := node["children"].([]any); ok {
				enriched := make([]any, len(arr))
				for i, child := range arr {
					if cm, ok := child.(map[string]any); ok {
						enriched[i] = displayNode(cm, lu)
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
				if cm, ok := arr[0].(map[string]any); ok {
					return map[string]any{"not": displayNode(cm, lu)}
				}
			}
			if cm, ok := node["child"].(map[string]any); ok {
				return map[string]any{"not": displayNode(cm, lu)}
			}
		}
	}

	// ── Schema A leaf node ─────────────────────────────────────────────
	if nodeType, ok := node["type"].(string); ok {
		switch nodeType {
		// Payee leaves — value field passes through.
		case "payee_exact", "payee_contains", "payee_starts_with",
			"payee_ends_with", "payee_not_contains", "payee_regex":
			value, _ := node["value"].(string)
			return map[string]any{nodeType: value}

		case "imported_payee_one_of":
			arr, _ := node["value"].([]any)
			return map[string]any{"payee_one_of": arr}

		// Amount range — cents → dollars (÷ 100).
		case "amount_range":
			inner := map[string]any{}
			if minC, ok := node["min_cents"].(float64); ok {
				inner["min"] = minC / 100
			}
			if maxC, ok := node["max_cents"].(float64); ok {
				inner["max"] = maxC / 100
			}
			return map[string]any{"amount_range": inner}

		case "date_day_of_month":
			inner := map[string]any{}
			if day, ok := node["day"]; ok {
				inner["day"] = day
			}
			if tol, ok := node["tolerance_days"]; ok {
				inner["tolerance_days"] = tol
			}
			return map[string]any{"date_day_of_month": inner}

		case "date_range":
			inner := map[string]any{}
			if s, ok := node["start"].(string); ok {
				inner["start"] = s
			}
			if e, ok := node["end"].(string); ok {
				inner["end"] = e
			}
			return map[string]any{"date_range": inner}

		case "account", "account_id":
			id, _ := node["value"].(string)
			return map[string]any{"account": resolve(id, lu.accountsByID)}

		case "institution", "institution_id":
			id, _ := node["value"].(string)
			return map[string]any{"institution": resolve(id, lu.instByID)}

		case "label_matched":
			id, _ := node["label_id"].(string)
			return map[string]any{"label_matched": resolve(id, lu.labelsByID)}

		case "entry_direction":
			dir, _ := node["direction"].(string)
			return map[string]any{"entry_direction": dir}

		case "entry_type":
			et, _ := node["entry_type"].(string)
			return map[string]any{"entry_type": et}

		case "entry_period":
			inner := map[string]any{}
			if v, ok := node["min_days"]; ok {
				inner["min_days"] = v
			}
			if v, ok := node["max_days"]; ok {
				inner["max_days"] = v
			}
			return map[string]any{"entry_period": inner}

		case "entry_fitness":
			score := node["score"]
			return map[string]any{"entry_fitness": score}

		case "entry_projected_rate":
			inner := map[string]any{}
			if v, ok := node["min"]; ok {
				inner["min"] = v
			}
			if v, ok := node["max"]; ok {
				inner["max"] = v
			}
			return map[string]any{"entry_projected_rate": inner}

		// Both old ("recurrence_anchor") and new ("entry_recurrence_anchor") Schema A types.
		case "entry_recurrence_anchor", "recurrence_anchor":
			anchor, _ := node["recurrence_anchor"].(string)
			return map[string]any{"entry_recurrence_anchor": anchor}
		}
		// Unknown Schema A type — pass through as-is.
		return node
	}

	// ── Schema B node (already in editor format) ───────────────────────
	// Recurse into logical containers; resolve any UUID values for
	// account/institution that may have been stored raw (legacy).
	out := copyMap(node)
	for key, val := range node {
		switch key {
		case "and", "or", "xor":
			if arr, ok := val.([]any); ok {
				enriched := make([]any, len(arr))
				for i, child := range arr {
					if cm, ok := child.(map[string]any); ok {
						enriched[i] = displayNode(cm, lu)
					} else {
						enriched[i] = child
					}
				}
				out[key] = enriched
			}
		case "not":
			if cm, ok := val.(map[string]any); ok {
				out[key] = displayNode(cm, lu)
			}
		case "account":
			if id, ok := val.(string); ok {
				out["account"] = resolve(id, lu.accountsByID)
			}
		case "institution":
			if id, ok := val.(string); ok {
				out["institution"] = resolve(id, lu.instByID)
			}
		// Legacy Schema B key → canonical form.
		case "recurrence_anchor":
			if s, ok := val.(string); ok {
				out["entry_recurrence_anchor"] = s
				delete(out, "recurrence_anchor")
			}
		}
	}
	return out
}

// ── storageNode (Schema B → Schema A) ─────────────────────────────────────

// storageNode converts a single Schema B node map to Schema A.
// resolveErr is set on first error; subsequent calls are no-ops.
func storageNode(
	node map[string]any,
	lu storageLookups,
	createLabel func(name string) (string, error),
	resolveErr *error,
) map[string]any {
	if *resolveErr != nil {
		return node
	}

	resolveName := func(name string, byName map[string]string, byID map[string]bool, kind string) (string, error) {
		if id, ok := byName[strings.ToLower(name)]; ok {
			return id, nil
		}
		if byID[name] {
			return name, nil // legacy UUID passthrough
		}
		return "", fmt.Errorf("%s %q not found", kind, name)
	}

	// ── Schema A passthrough — already in engine format ────────────────
	if _, hasOp := node["op"]; hasOp {
		if arr, ok := node["children"].([]any); ok {
			resolved := make([]any, len(arr))
			for i, child := range arr {
				if cm, ok := child.(map[string]any); ok {
					resolved[i] = storageNode(cm, lu, createLabel, resolveErr)
				} else {
					resolved[i] = child
				}
			}
			out := copyMap(node)
			out["children"] = resolved
			return out
		}
		if cm, ok := node["child"].(map[string]any); ok {
			out := copyMap(node)
			out["child"] = storageNode(cm, lu, createLabel, resolveErr)
			return out
		}
		return node
	}
	if _, hasType := node["type"]; hasType {
		return node // Schema A leaf — pass through
	}

	// ── Schema B logical nodes → Schema A ─────────────────────────────
	for _, pair := range []struct{ key, op string }{
		{"and", "AND"}, {"or", "OR"}, {"xor", "XOR"},
	} {
		if val, ok := node[pair.key]; ok {
			if arr, ok := val.([]any); ok {
				resolved := make([]any, len(arr))
				for i, child := range arr {
					if cm, ok := child.(map[string]any); ok {
						resolved[i] = storageNode(cm, lu, createLabel, resolveErr)
					} else {
						resolved[i] = child
					}
				}
				return map[string]any{"op": pair.op, "children": resolved}
			}
		}
	}
	if not, ok := node["not"]; ok {
		if cm, ok := not.(map[string]any); ok {
			return map[string]any{"op": "NOT", "children": []any{storageNode(cm, lu, createLabel, resolveErr)}}
		}
	}

	// ── Schema B leaf nodes → Schema A ────────────────────────────────
	for key, val := range node {
		switch key {
		// Payee — value is the match target string.
		case "payee_exact", "payee_contains", "payee_starts_with",
			"payee_ends_with", "payee_not_contains", "payee_regex":
			return map[string]any{"type": key, "value": val}

		case "payee_one_of":
			return map[string]any{"type": "imported_payee_one_of", "value": val}

		// Amount range — dollars → cents (× 100, rounded).
		case "amount_range":
			obj, _ := val.(map[string]any)
			out := map[string]any{"type": "amount_range"}
			if min, ok := obj["min"].(float64); ok {
				out["min_cents"] = int64(math.Round(min * 100))
			}
			if max, ok := obj["max"].(float64); ok {
				out["max_cents"] = int64(math.Round(max * 100))
			}
			return out

		case "date_day_of_month":
			obj, _ := val.(map[string]any)
			out := map[string]any{"type": "date_day_of_month"}
			if day, ok := obj["day"].(float64); ok {
				out["day"] = int(day)
			}
			if tol, ok := obj["tolerance_days"].(float64); ok {
				out["tolerance_days"] = int(tol)
			}
			return out

		case "date_range":
			obj, _ := val.(map[string]any)
			out := map[string]any{"type": "date_range"}
			if s, ok := obj["start"].(string); ok {
				out["start"] = s
			}
			if e, ok := obj["end"].(string); ok {
				out["end"] = e
			}
			return out

		case "label_matched":
			name, _ := val.(string)
			id, err := createLabel(name)
			if err != nil {
				*resolveErr = err
				return node
			}
			return map[string]any{"type": "label_matched", "label_id": id}

		case "entry_direction":
			return map[string]any{"type": "entry_direction", "direction": val}

		case "entry_type":
			return map[string]any{"type": "entry_type", "entry_type": val}

		case "account":
			name, _ := val.(string)
			id, err := resolveName(name, lu.accountsByName, lu.accountsByID, "account")
			if err != nil {
				*resolveErr = err
				return node
			}
			return map[string]any{"type": "account_id", "value": id}

		case "institution":
			name, _ := val.(string)
			id, err := resolveName(name, lu.instByName, lu.instByID, "institution")
			if err != nil {
				*resolveErr = err
				return node
			}
			return map[string]any{"type": "institution_id", "value": id}

		case "entry_period":
			obj, _ := val.(map[string]any)
			out := map[string]any{"type": "entry_period"}
			if v, ok := obj["min_days"].(float64); ok {
				out["min_days"] = int(v)
			}
			if v, ok := obj["max_days"].(float64); ok {
				out["max_days"] = int(v)
			}
			return out

		case "entry_fitness":
			return map[string]any{"type": "entry_fitness", "score": val}

		case "entry_projected_rate":
			obj, _ := val.(map[string]any)
			out := map[string]any{"type": "entry_projected_rate"}
			if v, ok := obj["min"].(float64); ok {
				out["min"] = v
			}
			if v, ok := obj["max"].(float64); ok {
				out["max"] = v
			}
			return out

		// Both canonical and legacy Schema B keys map to entry_recurrence_anchor.
		case "entry_recurrence_anchor", "recurrence_anchor":
			return map[string]any{"type": "entry_recurrence_anchor", "recurrence_anchor": val}
		}
	}

	return node // unknown node — pass through
}

// ── Public store methods ───────────────────────────────────────────────────

// ConditionsForDisplay converts conditions from Schema A to Schema B,
// resolving UUIDs to display names. Safe to call with nil/empty raw.
func (s *Store) ConditionsForDisplay(ctx context.Context, entityID string, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	lu := s.buildDisplayLookups(ctx, entityID)
	enriched := enrichAny(raw, func(node map[string]any) map[string]any {
		return displayNode(node, lu)
	})
	if b, err := json.Marshal(enriched); err == nil {
		return b
	}
	return raw
}

// ConditionsForStorage converts conditions from Schema B to Schema A,
// resolving names to UUIDs. Creates a label if label_matched names one
// that does not exist. Returns an error if account/institution is unresolvable.
func (s *Store) ConditionsForStorage(ctx context.Context, entityID string, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	lu := s.buildStorageLookups(ctx, entityID)

	var resolveErr error
	createLabel := func(name string) (string, error) {
		labels, err := s.ListLabels(ctx, entityID, 1000, "")
		if err != nil {
			return "", err
		}
		for _, l := range labels {
			if l.Name == name {
				return l.ID, nil
			}
		}
		created, err := s.CreateLabel(ctx, entityID, name)
		if err != nil {
			return "", err
		}
		return created.ID, nil
	}

	resolved := enrichAny(raw, func(node map[string]any) map[string]any {
		return storageNode(node, lu, createLabel, &resolveErr)
	})
	if resolveErr != nil {
		return nil, resolveErr
	}
	if b, err := json.Marshal(resolved); err != nil {
		return nil, err
	} else {
		return b, nil
	}
}

// ── Lookup builders ────────────────────────────────────────────────────────

func (s *Store) buildDisplayLookups(ctx context.Context, entityID string) displayLookups {
	lu := displayLookups{
		labelsByID:   map[string]string{},
		accountsByID: map[string]string{},
		instByID:     map[string]string{},
	}
	if labels, err := s.ListLabels(ctx, entityID, 1000, ""); err == nil {
		for _, l := range labels {
			lu.labelsByID[l.ID] = l.Name
		}
	}
	if accounts, err := s.ListAccounts(ctx, entityID, 1000, ""); err == nil {
		for _, a := range accounts {
			lu.accountsByID[a.ID] = a.Name
		}
	}
	if insts, err := s.ListInstitutions(ctx, entityID); err == nil {
		for _, i := range insts {
			lu.instByID[i.ID] = i.InstitutionName
		}
	}
	return lu
}

func (s *Store) buildStorageLookups(ctx context.Context, entityID string) storageLookups {
	lu := storageLookups{
		accountsByName: map[string]string{},
		accountsByID:   map[string]bool{},
		instByName:     map[string]string{},
		instByID:       map[string]bool{},
	}
	if accounts, err := s.ListAccounts(ctx, entityID, 1000, ""); err == nil {
		for _, a := range accounts {
			lu.accountsByName[strings.ToLower(a.Name)] = a.ID
			lu.accountsByID[a.ID] = true
		}
	}
	if insts, err := s.ListInstitutions(ctx, entityID); err == nil {
		for _, i := range insts {
			lu.instByName[strings.ToLower(i.InstitutionName)] = i.ID
			lu.instByID[i.ID] = true
		}
	}
	return lu
}

// ── Autocomplete data ──────────────────────────────────────────────────────

// AutocompleteData holds the names available for conditions autocomplete.
type AutocompleteData struct {
	Merchants []string `json:"merchants"`
	Labels    []string `json:"labels"`
}

// ListAutocompleteData returns merchant strings and label names for the editor.
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

// ListTransactionMerchants returns distinct merchant_normalized strings,
// optionally filtered by query substring. Used for payee_* autocomplete.
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

// ListUnaliasedTransactionMerchants is retained for handler compatibility.
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
