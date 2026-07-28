# Ledger SSR Refactor Design

## Goal

Remove nine SSR anti-patterns from the ledger page: push filtering and sorting into SQL, consolidate DB round-trips, fix a silently-broken sort option, replace JS-driven navigation with native HTML forms, and strip stale filter badges when the System tab is active.

Rate granularity (day/mo/yr) remains in `localStorage` — it is an app-wide user preference shared across multiple pages, so URL-param movement would break cross-page persistence. It is out of scope.

---

## Scope

Three files change: `store/entries.go`, `page/handler.go`, `page/ledger.templ`.

---

## Store Layer

### Rename `ListEntries` → `ListEntriesByStatus`

`ListEntries` is status-aware but its name gives no hint of that. Rename to `ListEntriesByStatus`. All callers update.

### Add filter and sort parameters to `ListEntriesByStatus`

Current signature:
```go
func (s *Store) ListEntries(ctx, entityID, dr, accountID, statusFilter string, limit int, cursor string) ([]EntryRow, error)
```

New signature:
```go
type EntryFilter struct {
    Status    string // "live" | "pending" | "ended" | "all"
    Direction string // "income" | "spend" | "mixed" | ""
    EntryType string // "standing" | "variable" | "irregular" | ""
    LabelID   string // uuid or ""
    Sort      string // "label" | "rate" | "fitness" | "start_date" | ""
    Limit     int
    Cursor    string
}

func (s *Store) ListEntriesByStatus(ctx context.Context, entityID string, dr DateRange, accountID string, f EntryFilter) ([]EntryRow, error)
```

`Direction`, `EntryType`, and `LabelID` map to SQL `AND` predicates appended to `extraFilters`. `Sort` maps to `ORDER BY`:

| `Sort` value | SQL ORDER BY |
|---|---|
| `""` / `"label"` | `l.name ASC NULLS LAST, e.start_date DESC` |
| `"start_date"` | `e.start_date DESC, e.id DESC` |
| `"rate"` | `s.actual_rate_per_day DESC NULLS LAST, e.start_date DESC` |
| `"fitness"` | `e.fitness DESC NULLS LAST, e.start_date DESC` |

The status interpolation vulnerability (raw string concat for `"pending"`/`"ended"`) is resolved by validating the status value against a whitelist at the top of the function and returning an error for unknown values — no user-controlled string ever reaches the SQL string.

### Rationalize `ListAllEntriesSorted`

Keep as a separate function — its `ORDER BY CASE e.status … END` grouping is genuinely distinct from any single-status sort. Remove the 1000-row cap comment (the limit is kept but documented as a practical ceiling, not a design constraint). Add `Direction`, `EntryType`, and `LabelID` filter params to it using the same `extraFilters` pattern so the "all" tab also benefits from SQL filtering.

The function does not take a `Sort` param — status-grouping is always its order. Post-refactor the sort select is hidden on the "all" tab: the "all" tab is a status-grouped overview, not a sortable list. Currently the Go sort.SliceStable runs on its result and silently overrides the status-group ordering on every default-sort page load — removing that is a correctness fix, not a regression.

### Consolidate `CountEntriesByStatus` into one query

Replace the current two sequential queries with a single conditional-aggregation query:

```sql
SELECT
    COUNT(*) FILTER (WHERE status = 'pending' AND source != 'system') AS pending,
    COUNT(*) FILTER (WHERE status = 'live'    AND source != 'system') AS live,
    COUNT(*) FILTER (WHERE status = 'ended'   AND source != 'system') AS ended,
    COUNT(*) FILTER (WHERE source = 'system')                         AS system
FROM entries
WHERE entity_id = $1
```

Scan directly into `EntryCounts` via `pgx.QueryRow` + `Scan`. One round-trip, one table scan.

---

## Handler Layer

### `Ledger` function cleanup

- Pass `direction`, `entryType`, `labelID`, and `sort` into `ListEntriesByStatus` and `ListAllEntriesSorted` (where applicable) rather than reading a full list and filtering in Go.
- Remove the in-memory filter loop entirely (the `filtered := entries[:0]` block).
- Remove the `sort.SliceStable` blocks entirely.
- When `filter == "system"`: zero out `LabelFilter`, `DirectionFilter`, and `TypeFilter` on `LedgerData` before rendering. This ensures stale badges never render regardless of what the URL contained.
- Hide the sort select on the "all" tab: add a `ShowSort bool` field to `LedgerData`, set to `true` only when `filter` is one of `"live"`, `"pending"`, `"ended"`, `"system"` is excluded (no sort), `"all"` is excluded (fixed status-group order).

### `LedgerData` additions

```go
type LedgerData struct {
    // existing fields unchanged
    ShowSort bool // true only on Live/Review/Ended tabs
}
```

---

## Template Layer

### Replace JS sort navigation with a native form

The sort `<select>` currently uses a 12-line JS block to read the current URL, mutate `?sort=`, and navigate. Replace with a `<form method="get" action="/ledger">` wrapping the select, with hidden `<input>` elements for each active filter param (`filter`, `direction`, `entry_type`, `label`). Add `onchange="this.form.submit()"` to the select. The JS block is removed.

`ledgerFilterURL` already omits `sort=label` from generated URLs (it's the default) — the form submission must match this: if `sort == "label"` omit the hidden input, otherwise include it. The form action is `/ledger` and the hidden inputs carry only non-default values, matching the existing URL conventions.

### Conditionalize sort select on `data.ShowSort`

```templ
if data.ShowSort {
    <label>Sort:</label>
    <form method="get" action="/ledger">
        <!-- hidden inputs for active filters -->
        <select name="sort" onchange="this.form.submit()">...</select>
    </form>
}
```

The sort select is hidden on the "all" and "system" tabs.

### Fix stale label badge

No template change needed — zeroing `LabelFilter`/`DirectionFilter`/`TypeFilter` in the handler when `filter == "system"` means the badge condition `data.LabelFilter != ""` is already false. The existing template guard is sufficient.

---

## What Does Not Change

- `ListSystemEntries` — untouched
- `ListAllEntriesSorted` signature for callers — the handler still calls it for the "all" tab; it gains optional filter params internally
- Rate granularity (`localStorage`) — out of scope
- All non-ledger pages and handlers
