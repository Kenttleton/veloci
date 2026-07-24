package store

import (
	"encoding/json"
	"testing"
)

// ── helpers ────────────────────────────────────────────────────────────────

func mustUnmarshal(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

var emptyLU = displayLookups{
	labelsByID:   map[string]string{},
	accountsByID: map[string]string{},
	instByID:     map[string]string{},
}

var emptyStoreLU = storageLookups{
	accountsByName: map[string]string{},
	accountsByID:   map[string]bool{},
	instByName:     map[string]string{},
	instByID:       map[string]bool{},
}

var noErr error
var noCreate = func(name string) (string, error) { return "label-uuid", nil }

// roundTrip converts Schema B → A → B, passing through JSON marshal/unmarshal
// in between to normalize Go types (the same path taken when storing to the DB).
func roundTrip(t *testing.T, b map[string]any) map[string]any {
	t.Helper()
	var resolveErr error
	schemaA := storageNode(b, emptyStoreLU, noCreate, &resolveErr)
	if resolveErr != nil {
		t.Fatalf("storageNode error: %v", resolveErr)
	}
	// JSON-normalize: int64/int → float64, matching what json.Unmarshal produces.
	jsonA, err := json.Marshal(schemaA)
	if err != nil {
		t.Fatalf("marshal schema A: %v", err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(jsonA, &normalized); err != nil {
		t.Fatalf("unmarshal schema A: %v", err)
	}
	return displayNode(normalized, emptyLU)
}

// ── Schema B → A (storageNode) ─────────────────────────────────────────────

func TestStorageNode_PayeeExact(t *testing.T) {
	in := mustUnmarshal(t, `{"payee_exact":"NETFLIX"}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if out["type"] != "payee_exact" || out["value"] != "NETFLIX" {
		t.Fatalf("unexpected Schema A: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_PayeeOneOf(t *testing.T) {
	in := mustUnmarshal(t, `{"payee_one_of":["Netflix","Hulu"]}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "imported_payee_one_of" {
		t.Fatalf("expected imported_payee_one_of, got %v", mustMarshal(t, out))
	}
}

func TestStorageNode_AmountRange_CentsConversion(t *testing.T) {
	in := mustUnmarshal(t, `{"amount_range":{"min":-50.25,"max":-10.00}}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "amount_range" {
		t.Fatalf("wrong type: %v", out["type"])
	}
	if out["min_cents"] != int64(-5025) {
		t.Errorf("min_cents want -5025, got %v", out["min_cents"])
	}
	if out["max_cents"] != int64(-1000) {
		t.Errorf("max_cents want -1000, got %v", out["max_cents"])
	}
}

func TestStorageNode_AmountRange_PartialBounds(t *testing.T) {
	in := mustUnmarshal(t, `{"amount_range":{"min":100}}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if _, hasMax := out["max_cents"]; hasMax {
		t.Errorf("max_cents should be absent when max not supplied")
	}
}

func TestStorageNode_DateDayOfMonth(t *testing.T) {
	in := mustUnmarshal(t, `{"date_day_of_month":{"day":15,"tolerance_days":2}}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "date_day_of_month" || out["day"] != 15 || out["tolerance_days"] != 2 {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_DateRange(t *testing.T) {
	in := mustUnmarshal(t, `{"date_range":{"start":"2026-01-01","end":"2026-12-31"}}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "date_range" || out["start"] != "2026-01-01" || out["end"] != "2026-12-31" {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_EntryDirection(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_direction":"income"}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "entry_direction" || out["direction"] != "income" {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_EntryType(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_type":"standing"}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "entry_type" || out["entry_type"] != "standing" {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_EntryPeriod(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_period":{"min_days":25,"max_days":35}}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "entry_period" || out["min_days"] != 25 || out["max_days"] != 35 {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_EntryFitness(t *testing.T) {
	score := map[string]any{"overall": map[string]any{"min": 0.8}}
	in := map[string]any{"entry_fitness": score}
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "entry_fitness" {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_EntryProjectedRate(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_projected_rate":{"min":1.5,"max":5.0}}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "entry_projected_rate" || out["min"] != 1.5 || out["max"] != 5.0 {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_EntryRecurrenceAnchor(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_recurrence_anchor":"dom:-1"}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "entry_recurrence_anchor" || out["recurrence_anchor"] != "dom:-1" {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_LegacyRecurrenceAnchor(t *testing.T) {
	// Old Schema B key "recurrence_anchor" must produce same Schema A output.
	in := mustUnmarshal(t, `{"recurrence_anchor":"dom:15"}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["type"] != "entry_recurrence_anchor" {
		t.Fatalf("expected entry_recurrence_anchor type, got %v", out["type"])
	}
}

func TestStorageNode_LabelMatched(t *testing.T) {
	in := mustUnmarshal(t, `{"label_matched":"Netflix"}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, func(name string) (string, error) {
		return "label-abc", nil
	}, &resolveErr)
	if out["type"] != "label_matched" || out["label_id"] != "label-abc" {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_Account_ByName(t *testing.T) {
	lu := storageLookups{
		accountsByName: map[string]string{"chase checking": "acct-uuid"},
		accountsByID:   map[string]bool{},
		instByName:     map[string]string{},
		instByID:       map[string]bool{},
	}
	in := mustUnmarshal(t, `{"account":"Chase Checking"}`)
	var resolveErr error
	out := storageNode(in, lu, noCreate, &resolveErr)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if out["type"] != "account_id" || out["value"] != "acct-uuid" {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_Institution_ByName(t *testing.T) {
	lu := storageLookups{
		accountsByName: map[string]string{},
		accountsByID:   map[string]bool{},
		instByName:     map[string]string{"chase": "inst-uuid"},
		instByID:       map[string]bool{},
	}
	in := mustUnmarshal(t, `{"institution":"Chase"}`)
	var resolveErr error
	out := storageNode(in, lu, noCreate, &resolveErr)
	if resolveErr != nil {
		t.Fatal(resolveErr)
	}
	if out["type"] != "institution_id" || out["value"] != "inst-uuid" {
		t.Fatalf("unexpected: %v", mustMarshal(t, out))
	}
}

func TestStorageNode_And(t *testing.T) {
	in := mustUnmarshal(t, `{"and":[{"payee_exact":"A"},{"payee_exact":"B"}]}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["op"] != "AND" {
		t.Fatalf("expected AND, got %v", mustMarshal(t, out))
	}
	children, _ := out["children"].([]any)
	if len(children) != 2 {
		t.Fatalf("expected 2 children")
	}
}

func TestStorageNode_Not(t *testing.T) {
	in := mustUnmarshal(t, `{"not":{"payee_exact":"NETFLIX"}}`)
	var resolveErr error
	out := storageNode(in, emptyStoreLU, noCreate, &resolveErr)
	if out["op"] != "NOT" {
		t.Fatalf("expected NOT, got %v", mustMarshal(t, out))
	}
}

// ── Schema A → B (displayNode) ─────────────────────────────────────────────

func TestDisplayNode_PayeeContains(t *testing.T) {
	a := mustUnmarshal(t, `{"type":"payee_contains","value":"NETFLIX"}`)
	b := displayNode(a, emptyLU)
	if b["payee_contains"] != "NETFLIX" {
		t.Fatalf("unexpected: %v", mustMarshal(t, b))
	}
}

func TestDisplayNode_ImportedPayeeOneOf(t *testing.T) {
	a := mustUnmarshal(t, `{"type":"imported_payee_one_of","value":["Netflix","Hulu"]}`)
	b := displayNode(a, emptyLU)
	if _, ok := b["payee_one_of"]; !ok {
		t.Fatalf("expected payee_one_of key: %v", mustMarshal(t, b))
	}
}

func TestDisplayNode_AmountRange_DollarsConversion(t *testing.T) {
	a := mustUnmarshal(t, `{"type":"amount_range","min_cents":-5025,"max_cents":-1000}`)
	b := displayNode(a, emptyLU)
	inner, _ := b["amount_range"].(map[string]any)
	if inner["min"] != -50.25 {
		t.Errorf("min want -50.25, got %v", inner["min"])
	}
	if inner["max"] != -10.0 {
		t.Errorf("max want -10.0, got %v", inner["max"])
	}
}

func TestDisplayNode_DateDayOfMonth(t *testing.T) {
	a := mustUnmarshal(t, `{"type":"date_day_of_month","day":15,"tolerance_days":2}`)
	b := displayNode(a, emptyLU)
	inner, _ := b["date_day_of_month"].(map[string]any)
	if inner["day"] == nil {
		t.Fatalf("missing day: %v", mustMarshal(t, b))
	}
}

func TestDisplayNode_DateRange(t *testing.T) {
	a := mustUnmarshal(t, `{"type":"date_range","start":"2026-01-01","end":"2026-12-31"}`)
	b := displayNode(a, emptyLU)
	inner, _ := b["date_range"].(map[string]any)
	if inner["start"] != "2026-01-01" || inner["end"] != "2026-12-31" {
		t.Fatalf("unexpected: %v", mustMarshal(t, b))
	}
}

func TestDisplayNode_EntryRecurrenceAnchor(t *testing.T) {
	a := mustUnmarshal(t, `{"type":"entry_recurrence_anchor","recurrence_anchor":"dom:-1"}`)
	b := displayNode(a, emptyLU)
	if b["entry_recurrence_anchor"] != "dom:-1" {
		t.Fatalf("unexpected: %v", mustMarshal(t, b))
	}
}

func TestDisplayNode_LegacyRecurrenceAnchorType(t *testing.T) {
	// Old Schema A type "recurrence_anchor" should produce canonical Schema B key.
	a := mustUnmarshal(t, `{"type":"recurrence_anchor","recurrence_anchor":"dom:15"}`)
	b := displayNode(a, emptyLU)
	if _, ok := b["entry_recurrence_anchor"]; !ok {
		t.Fatalf("expected entry_recurrence_anchor key: %v", mustMarshal(t, b))
	}
}

func TestDisplayNode_LabelMatched_NameResolution(t *testing.T) {
	lu := displayLookups{
		labelsByID:   map[string]string{"label-abc": "Netflix"},
		accountsByID: map[string]string{},
		instByID:     map[string]string{},
	}
	a := mustUnmarshal(t, `{"type":"label_matched","label_id":"label-abc"}`)
	b := displayNode(a, lu)
	if b["label_matched"] != "Netflix" {
		t.Fatalf("expected Netflix, got %v", b["label_matched"])
	}
}

func TestDisplayNode_AccountID_NameResolution(t *testing.T) {
	lu := displayLookups{
		labelsByID:   map[string]string{},
		accountsByID: map[string]string{"acct-uuid": "Chase Checking"},
		instByID:     map[string]string{},
	}
	a := mustUnmarshal(t, `{"type":"account_id","value":"acct-uuid"}`)
	b := displayNode(a, lu)
	if b["account"] != "Chase Checking" {
		t.Fatalf("expected 'Chase Checking', got %v", b["account"])
	}
}

func TestDisplayNode_And(t *testing.T) {
	a := mustUnmarshal(t, `{"op":"AND","children":[{"type":"payee_exact","value":"A"}]}`)
	b := displayNode(a, emptyLU)
	if _, ok := b["and"]; !ok {
		t.Fatalf("expected 'and' key: %v", mustMarshal(t, b))
	}
}

func TestDisplayNode_Not(t *testing.T) {
	a := mustUnmarshal(t, `{"op":"NOT","children":[{"type":"payee_exact","value":"A"}]}`)
	b := displayNode(a, emptyLU)
	if _, ok := b["not"]; !ok {
		t.Fatalf("expected 'not' key: %v", mustMarshal(t, b))
	}
}

// ── Round-trip tests (B → A → B) ──────────────────────────────────────────

func TestRoundTrip_PayeeExact(t *testing.T) {
	in := mustUnmarshal(t, `{"payee_exact":"NETFLIX"}`)
	out := roundTrip(t, in)
	if out["payee_exact"] != "NETFLIX" {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_PayeeOneOf(t *testing.T) {
	in := mustUnmarshal(t, `{"payee_one_of":["Netflix","Hulu"]}`)
	out := roundTrip(t, in)
	if _, ok := out["payee_one_of"]; !ok {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_AmountRange(t *testing.T) {
	in := mustUnmarshal(t, `{"amount_range":{"min":-50.25,"max":-10.00}}`)
	out := roundTrip(t, in)
	inner, _ := out["amount_range"].(map[string]any)
	if inner["min"] != -50.25 || inner["max"] != -10.0 {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_AmountRange_OnlyMin(t *testing.T) {
	in := mustUnmarshal(t, `{"amount_range":{"min":100}}`)
	out := roundTrip(t, in)
	inner, _ := out["amount_range"].(map[string]any)
	if inner["min"] != 100.0 {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
	if _, ok := inner["max"]; ok {
		t.Fatalf("max should be absent: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_DateDayOfMonth(t *testing.T) {
	in := mustUnmarshal(t, `{"date_day_of_month":{"day":15,"tolerance_days":2}}`)
	out := roundTrip(t, in)
	inner, _ := out["date_day_of_month"].(map[string]any)
	if inner["day"].(float64) != 15 || inner["tolerance_days"].(float64) != 2 {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_DateRange(t *testing.T) {
	in := mustUnmarshal(t, `{"date_range":{"start":"2026-01-01","end":"2026-12-31"}}`)
	out := roundTrip(t, in)
	inner, _ := out["date_range"].(map[string]any)
	if inner["start"] != "2026-01-01" || inner["end"] != "2026-12-31" {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_EntryDirection(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_direction":"spend"}`)
	out := roundTrip(t, in)
	if out["entry_direction"] != "spend" {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_EntryType(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_type":"variable"}`)
	out := roundTrip(t, in)
	if out["entry_type"] != "variable" {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_EntryPeriod(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_period":{"min_days":25,"max_days":35}}`)
	out := roundTrip(t, in)
	inner, _ := out["entry_period"].(map[string]any)
	if inner["min_days"].(float64) != 25 || inner["max_days"].(float64) != 35 {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_EntryFitness(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_fitness":{"overall":{"min":0.8}}}`)
	out := roundTrip(t, in)
	if _, ok := out["entry_fitness"]; !ok {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_EntryProjectedRate(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_projected_rate":{"min":1.5,"max":5.0}}`)
	out := roundTrip(t, in)
	inner, _ := out["entry_projected_rate"].(map[string]any)
	if inner["min"] != 1.5 || inner["max"] != 5.0 {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_EntryRecurrenceAnchor(t *testing.T) {
	in := mustUnmarshal(t, `{"entry_recurrence_anchor":"dom:-1"}`)
	out := roundTrip(t, in)
	if out["entry_recurrence_anchor"] != "dom:-1" {
		t.Fatalf("round-trip failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_LegacyRecurrenceAnchorUpgrade(t *testing.T) {
	// A Schema B document that uses the old key should round-trip to the new key.
	in := mustUnmarshal(t, `{"recurrence_anchor":"dom:15"}`)
	out := roundTrip(t, in)
	if out["entry_recurrence_anchor"] != "dom:15" {
		t.Fatalf("expected canonical key in output: %v", mustMarshal(t, out))
	}
	if _, old := out["recurrence_anchor"]; old {
		t.Fatalf("old key must be gone after round-trip: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_NestedAnd(t *testing.T) {
	in := mustUnmarshal(t, `{"and":[{"payee_exact":"A"},{"entry_direction":"spend"}]}`)
	out := roundTrip(t, in)
	arr, _ := out["and"].([]any)
	if len(arr) != 2 {
		t.Fatalf("round-trip nested AND failed: %v", mustMarshal(t, out))
	}
}

func TestRoundTrip_NotWrapped(t *testing.T) {
	in := mustUnmarshal(t, `{"not":{"payee_contains":"REFUND"}}`)
	out := roundTrip(t, in)
	inner, _ := out["not"].(map[string]any)
	if inner["payee_contains"] != "REFUND" {
		t.Fatalf("round-trip NOT failed: %v", mustMarshal(t, out))
	}
}
