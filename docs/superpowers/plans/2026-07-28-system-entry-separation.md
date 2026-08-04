# System Entry Separation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move system entries (All/Income/Spend) out of the workflow tabs (Review/Live/Ended) and into a dedicated "System" pill filter, while keeping them visible in the global "All" tab.

**Architecture:** Three-layer change — store adds `ListSystemEntries` (fetch + application-side sort by name map) and excludes system entries from `ListEntries` and `CountEntriesByStatus`; the handler adds a `filter=system` branch that bypasses all sub-filtering; the template adds the System pill and hides direction/type/sort controls when the System filter is active.

**Tech Stack:** Go 1.26, Templ (HTML templating), PostgreSQL (pgx/v5)

## Global Constraints

- System entries are identified by `e.source = 'system'` in the `entries` table
- `entryCols` and `entryFrom` constants in `store/entries.go` must be reused verbatim in `ListSystemEntries`
- Template uses inline CSS only — no external stylesheets or class additions
- `ledgerPillColor` switch in `ledger.templ` must be extended for `"system"`
- `ledgerFilterURL` already handles `filter=system` generically — no changes needed there

---

### Task 1: Store — `ListSystemEntries` + exclude system from `ListEntries` + update `CountEntriesByStatus`

**Files:**
- Modify: `services/web/store/entries.go` (lines 70–153 `ListEntries`, lines 542–570 `CountEntriesByStatus`, after line 601 add `ListSystemEntries`)
- Test: `services/web/store/entries_system_test.go` (new file)

**Interfaces:**
- Produces: `func (s *Store) ListSystemEntries(ctx context.Context, entityID string) ([]EntryRow, error)`

---

- [ ] **Step 1: Write the failing test for `ListSystemEntries` sort ordering**

Create `services/web/store/entries_system_test.go`:

```go
package store

import (
	"testing"
)

func TestSortSystemEntries(t *testing.T) {
	name := func(s string) *string { return &s }

	entries := []EntryRow{
		{ID: "c", LabelName: name("Spend")},
		{ID: "a", LabelName: name("All")},
		{ID: "x", LabelName: name("Unrecognized")},
		{ID: "b", LabelName: name("Income")},
	}

	sortSystemEntries(entries)

	want := []string{"All", "Income", "Spend", "Unrecognized"}
	for i, e := range entries {
		got := ""
		if e.LabelName != nil {
			got = *e.LabelName
		}
		if got != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got, want[i])
		}
	}
}

func TestSortSystemEntriesNilLabel(t *testing.T) {
	entries := []EntryRow{
		{ID: "b", LabelName: nil},
		{ID: "a", LabelName: func() *string { s := "Income"; return &s }()},
	}

	sortSystemEntries(entries)

	// "Income" is mapped (rank 1), nil label is unmapped — Income should come first
	if entries[0].ID != "a" {
		t.Errorf("expected Income (mapped) first, got ID=%q", entries[0].ID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd services/web && go test ./store/... -run TestSortSystemEntries -v
```

Expected: `FAIL` — `sortSystemEntries` undefined

- [ ] **Step 3: Add `sortSystemEntries` and `ListSystemEntries` to `store/entries.go`**

Add after the closing brace of `ListAllEntriesSorted` (after line 601):

```go
// systemEntryOrder defines the display rank for system entries by label name.
// Unmapped entries sort after all mapped ones, preserving DB retrieval order.
var systemEntryOrder = map[string]int{
	"All":    0,
	"Income": 1,
	"Spend":  2,
}

func sortSystemEntries(entries []EntryRow) {
	rank := func(e EntryRow) int {
		if e.LabelName != nil {
			if r, ok := systemEntryOrder[*e.LabelName]; ok {
				return r
			}
		}
		return len(systemEntryOrder) + 1
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return rank(entries[i]) < rank(entries[j])
	})
}

// ListSystemEntries returns all entries with source='system' for the entity,
// sorted by the hardcoded systemEntryOrder map (All → Income → Spend).
// Unmapped future system entries appear after the known ones in retrieval order.
func (s *Store) ListSystemEntries(ctx context.Context, entityID string) ([]EntryRow, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM entries e
		LEFT JOIN labels l ON l.id = e.label_id
		LEFT JOIN LATERAL (
			SELECT actual_rate_per_day, drift_per_day
			FROM snapshots
			WHERE entity_id = e.entity_id AND node_id = e.id AND node_type = 'entry'
			ORDER BY snapshot_date DESC LIMIT 1
		) s ON true
		WHERE e.entity_id = $1 AND e.source = 'system'
	`, entryCols), entityID)
	if err != nil {
		return nil, err
	}
	entries, err := pgx.CollectRows(rows, pgx.RowToStructByName[EntryRow])
	if err != nil {
		return nil, err
	}
	sortSystemEntries(entries)
	return entries, nil
}
```

Note: `sort` is already imported in `store/entries.go` — check with `grep '"sort"' store/entries.go`. If not present, add it to the import block.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd services/web && go test ./store/... -run TestSortSystemEntries -v
```

Expected: `PASS`

- [ ] **Step 5: Update `EntryCounts` and `CountEntriesByStatus` to track system entries separately**

The All tab includes system entries, so the All pill count must include them. Review/Live/Ended pill counts must not include them. The solution: add a `System int` field to `EntryCounts` and count them separately.

In `store/entries.go`, update the struct:

```go
// EntryCounts holds per-status counts for Ledger filter pills.
type EntryCounts struct {
    Pending int
    Live    int
    Ended   int
    System  int
}
```

Replace the `CountEntriesByStatus` query to exclude system entries from status counts and count them separately:

```go
func (s *Store) CountEntriesByStatus(ctx context.Context, entityID string) (EntryCounts, error) {
    rows, err := s.pool.Query(ctx, `
        SELECT status, COUNT(*)::int
        FROM entries
        WHERE entity_id = $1 AND source != 'system'
        GROUP BY status
    `, entityID)
    if err != nil {
        return EntryCounts{}, err
    }
    defer rows.Close()
    var c EntryCounts
    for rows.Next() {
        var status string
        var count int
        if err := rows.Scan(&status, &count); err != nil {
            return EntryCounts{}, err
        }
        switch status {
        case "pending":
            c.Pending = count
        case "live":
            c.Live = count
        case "ended":
            c.Ended = count
        }
    }
    if err := rows.Err(); err != nil {
        return EntryCounts{}, err
    }

    // Count system entries separately so the All pill can include them.
    if err := s.pool.QueryRow(ctx, `
        SELECT COUNT(*)::int FROM entries WHERE entity_id = $1 AND source = 'system'
    `, entityID).Scan(&c.System); err != nil {
        return EntryCounts{}, err
    }
    return c, nil
}
```

Then in `ledger.templ`, update the All pill to include system entries in its count:

```templ
@ledgerPill("all", "All", data, data.Counts.Pending+data.Counts.Live+data.Counts.Ended+data.Counts.System)
```

- [ ] **Step 6: Update `ListEntries` to exclude system entries**

In `store/entries.go`, change the `statusCond` default from:

```go
statusCond := `e.status = 'live'`
```

to:

```go
statusCond := `e.status = 'live' AND e.source != 'system'`
```

And update the other branches:

```go
switch statusFilter {
case "all":
    statusCond = `e.source != 'system'`
case "pending", "ended":
    statusCond = `e.status = '` + statusFilter + `' AND e.source != 'system'`
}
```

- [ ] **Step 7: Build to verify no compile errors**

```bash
cd services/web && go build ./...
```

Expected: clean build

- [ ] **Step 8: Commit**

```bash
git add services/web/store/entries.go services/web/store/entries_system_test.go
git commit -m "feat: add ListSystemEntries; exclude system entries from ListEntries and counts"
```

---

### Task 2: Handler — add `filter=system` branch in `Ledger`

**Files:**
- Modify: `services/web/page/handler.go` (lines 562–670, the `Ledger` function)

**Interfaces:**
- Consumes: `store.ListSystemEntries(ctx, entityID) ([]EntryRow, error)` from Task 1
- The `LedgerData.Filter` field already accepts any string value — `"system"` works with no struct changes

---

- [ ] **Step 1: Add the `filter=system` branch in `Ledger`**

In `handler.go`, the entry-fetch block currently reads:

```go
var entries []store.EntryRow
if filter == "all" {
    entries, _ = s.store.ListAllEntriesSorted(ctx, entityID)
} else {
    entries, _ = s.store.ListEntries(ctx, entityID, store.DateRange{}, "", filter, 500, "")
}
```

Replace with:

```go
var entries []store.EntryRow
switch filter {
case "all":
    entries, _ = s.store.ListAllEntriesSorted(ctx, entityID)
case "system":
    entries, _ = s.store.ListSystemEntries(ctx, entityID)
default:
    entries, _ = s.store.ListEntries(ctx, entityID, store.DateRange{}, "", filter, 500, "")
}
```

- [ ] **Step 2: Skip sub-filters and sort when `filter=system`**

The in-memory filter block (label, direction, type) and the sort block must not run when filter is `"system"`. Replace the two existing blocks with:

```go
// Apply additional filters (label, direction, type) in Go after fetch.
if filter != "system" && (labelFilter != "" || dirFilter != "" || typeFilter != "") {
	filtered := entries[:0]
	for _, e := range entries {
		if labelFilter != "" && (e.LabelID == nil || *e.LabelID != labelFilter) {
			continue
		}
		if dirFilter != "" && e.Direction != dirFilter {
			continue
		}
		if typeFilter != "" && e.EntryType != typeFilter {
			continue
		}
		filtered = append(filtered, e)
	}
	entries = filtered
}

// Apply non-default sort.
if filter != "system" {
	switch srt {
	case "rate":
		sort.SliceStable(entries, func(i, j int) bool {
			ri, rj := entries[i].ActualRatePerDay, entries[j].ActualRatePerDay
			if ri == nil && rj == nil {
				return false
			}
			if ri == nil {
				return false
			}
			if rj == nil {
				return true
			}
			return *ri > *rj
		})
	case "fitness":
		sort.SliceStable(entries, func(i, j int) bool {
			ci, cj := entries[i].Fitness, entries[j].Fitness
			if ci == nil && cj == nil {
				return false
			}
			if ci == nil {
				return false
			}
			if cj == nil {
				return true
			}
			return *ci > *cj
		})
	case "label":
		sort.SliceStable(entries, func(i, j int) bool {
			li, lj := "", ""
			if entries[i].LabelName != nil {
				li = *entries[i].LabelName
			}
			if entries[j].LabelName != nil {
				lj = *entries[j].LabelName
			}
			return li < lj
		})
	}
}
```

- [ ] **Step 3: Build to verify no compile errors**

```bash
cd services/web && go build ./...
```

Expected: clean build

- [ ] **Step 4: Commit**

```bash
git add services/web/page/handler.go
git commit -m "feat: add system filter branch to Ledger handler"
```

---

### Task 3: Template — System pill + hide sub-filters/sort when active

**Files:**
- Modify: `services/web/page/ledger.templ`
- Regenerate: `services/web/page/ledger_templ.go` (via `go generate`)

---

- [ ] **Step 1: Add `"system"` case to `ledgerPillColor`**

In `ledger.templ`, find `ledgerPillColor` (around line 1166) and add a case before `default`:

```go
func ledgerPillColor(value string) string {
    switch value {
    case "pending":
        return "var(--accent)"
    case "live":
        return "var(--income)"
    case "ended":
        return "var(--text3)"
    case "system":
        return "var(--text3)"
    default:
        return "var(--text2)"
    }
}
```

- [ ] **Step 2: Add the System pill to the pill bar**

In `ledger.templ`, the pill bar (around lines 14–17) currently ends with the Ended pill:

```templ
@ledgerPill("all", "All", data, data.Counts.Pending+data.Counts.Live+data.Counts.Ended)
@ledgerPill("pending", "Review", data, data.Counts.Pending)
@ledgerPill("live", "Live", data, data.Counts.Live)
@ledgerPill("ended", "Ended", data, data.Counts.Ended)
```

The System pill has no count. Add a new pill component `ledgerSystemPill` and call it after Ended:

In the pill bar, after `@ledgerPill("ended", ...)`:

```templ
@ledgerSystemPill(data)
```

Add the new component near the other pill components (e.g. after `ledgerTypePill`):

```templ
templ ledgerSystemPill(data LedgerData) {
    <a
        href={ templ.SafeURL(ledgerFilterURL(data, "filter", "system")) }
        style={ ledgerPillStyle("system", data.Filter == "system") }
    >System</a>
}
```

Note: `ledgerPill` renders a count badge; `ledgerSystemPill` does not — that's intentional.

- [ ] **Step 3: Hide direction/type sub-filters and sort when `filter=system`**

The direction/type pills (around lines 19–24) and the sort dropdown (around lines 40–51) should not render when the System filter is active. Wrap them:

Direction/type pills — find the divider and direction/type pills block:

```templ
if data.Filter != "system" {
    <div style="width:1px;height:16px;background:var(--border);margin:0 4px"></div>
    @ledgerDirectionPill("income", "Income", data)
    @ledgerDirectionPill("spend", "Spend", data)
    <div style="width:1px;height:16px;background:var(--border);margin:0 4px"></div>
    @ledgerTypePill("standing", "Standing", data)
    @ledgerTypePill("variable", "Variable", data)
    @ledgerTypePill("irregular", "Irregular", data)
}
```

Sort dropdown — find the `<label>Sort:</label>` and `<select id="ledger-sort">` block and wrap it:

```templ
if data.Filter != "system" {
    <label style="font-size:11px;color:var(--text3)">Sort:</label>
    <select
        id="ledger-sort"
        ...
    >
        ...
    </select>
    <div style="width:1px;height:16px;background:var(--border)"></div>
}
```

- [ ] **Step 4: Regenerate the compiled template**

```bash
cd services/web && go generate ./...
```

If `go generate` is not wired, run templ directly:

```bash
templ generate services/web/page/ledger.templ
```

- [ ] **Step 5: Build and verify**

```bash
cd services/web && go build ./...
```

Expected: clean build

- [ ] **Step 6: Commit**

```bash
git add services/web/page/ledger.templ services/web/page/ledger_templ.go
git commit -m "feat: add System pill to ledger, hide sub-filters when System tab active"
```
