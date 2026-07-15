# CSV Import Flow — Design

## Context

This is the second half of a two-part feature (the first half, Add Account, shipped
earlier — see `2026-07-15-add-account-flow-design.md`). The backend for CSV import
already exists end-to-end: `POST /imports` (multipart `account_id` + `csv` file)
creates a `pending_imports` row, enqueues an `import.process` job over RabbitMQ, and
the Rust engine's Stage 0 consumes it using the account's `institution_mappings` row
to resolve column positions, sign convention, and settlement window. `institution_mappings`
CRUD (`GET/POST/PUT/DELETE /institutions`) is also fully built. What's missing is
entirely on the frontend: `ImportsTab.tsx` only renders a read-only import-history
table — there is no upload button and no column-mapping UI anywhere in the app.

Working through the design surfaced a domain clarification and a related gap in the
just-shipped Add Account flow, both captured below.

## Domain model clarification

An institution (`institution_mappings` row) *is* a mapping — one unique name, one set
of column/settlement/sign-convention fields. Multiple accounts commonly share one
institution (e.g. "Ally" used by both a checking and a savings account) — this is the
normal case, not an edge case. An account belongs to exactly one institution at a time.

When a bank's CSV export format differs across account types, the correct way to
model that is distinctly-named institutions, not variants within one institution —
e.g. "Ally" (checking/savings), "Ally Credit", "Ally Money Market", each with its own
mapping. There is no per-institution "sub-mapping" concept; a differently-shaped CSV
means a differently-named institution.

## Core mechanic: one shared mapping editor, two contexts

Both "set up a mapping during account creation" and "edit a mapping during CSV
upload" reduce to the same operation: start from some set of mapping field values
(blank defaults, or an existing institution's real saved values), let the user edit
any field including the name, and on submit:

- **Name unchanged** (matches the institution the editor started from) → `PUT
  /institutions/{id}`, update in place. This affects every account currently sharing
  that institution.
- **Name is new or changed** (no starting institution, or the user renamed it) →
  `POST /institutions`, create a fresh institution, then relink the current account
  to the new institution's id.

This is one reusable editor + one submit function, parameterized by its starting
point — not two separate implementations.

### Backend gap this requires closing

`PUT /accounts/{id}` (`services/api/handler/accounts.go`, `UpdateAccount`) currently
never touches `institution_id` — confirmed the SQL `UPDATE accounts SET ...` clause
in `store.UpdateAccount` omits it entirely. The fork case above needs to relink an
account to a newly-created institution, so `institution_id` must become an optional
updatable field on this endpoint (store SQL updated to include it in the `SET`
clause when provided).

### Read-only mapping preview whenever linking to an existing institution

Whenever the user selects/links an existing institution — in Add Account's "Link
existing" path, or CSV upload's "Use existing mapping" path — show a read-only
preview of that institution's actual mapping fields (date/amount/merchant columns,
optional balance/debit-credit/imported-id columns, sign convention, dedup/settlement/
tolerance settings) immediately after selection. Users should never have to rely on
memory for what a mapping contains. `useListInstitutions()` already returns every
mapping field on each row (confirmed: `InstitutionView` includes all of
`date_col`/`amount_col`/`merchant_col`/`balance_col`/`debit_credit_col`/
`imported_id_col`/`amount_sign_convention`/`dedup_window_days`/
`settlement_window_days`/`amount_tolerance_pct`), so no extra fetch is needed — the
already-loaded list data is sufficient.

## Amendment to Add Account (already shipped)

`AddAccountModal`'s three institution radio options change meaning slightly:

- **Link to existing institution** — unchanged mechanically, but now also renders
  the read-only mapping preview immediately below the select, once an institution is
  chosen.
- **Create new institution** — routes through the shared mapping editor, starting
  from blank defaults (empty name, required; column/settlement fields at the same
  engineering defaults already implemented: `date`/`amount`/`description`,
  `positive_is_credit`, dedup 3 / settlement 14 / tolerance 0.5%).
- **Skip for now** (renamed from "No institution (cash/manual account)") — also
  routes through the shared mapping editor, but pre-filled with the account's own
  name (guaranteed unique, since account names are already unique per entity) and
  the same engineering defaults. This eliminates the prior "no institution possible"
  error state entirely — every account now always ends up with a real institution
  and mapping by construction. The standalone `POST /accounts` endpoint (added in the
  first phase) is no longer required for this specific path, since account creation
  now always goes through `POST /institutions` + `POST /institutions/{id}/accounts`,
  but the endpoint remains for general CRUD completeness.

## New CSV Upload flow

1. `ImportsTab.tsx` gets an "Upload CSV" button, opening a new `UploadImportModal`
   (built on the existing shared `Modal.tsx`).
2. User picks a file. The file is parsed client-side with `papaparse` (new
   dependency — needed for correct handling of quoted fields containing commas,
   common in real bank exports) to extract the header row and a handful of sample
   rows.
3. Ternary choice, presented after file selection:
   - **Use existing mapping** — shows the read-only mapping preview (same component
     as the Add Account amendment above) for the account's current institution, then
     uploads directly on confirmation.
   - **Edit mapping** — opens the shared mapping editor, pre-filled with the
     account's current institution's real saved values. Column fields (date/amount/
     merchant/balance/debit-credit/imported-id) render as dropdowns populated from
     the real CSV headers parsed in step 2, instead of the free-text inputs used in
     the Add Account context (there's no CSV to reference at account-creation time).
     Includes a live preview table of the first few rows using the current field
     selections. If the institution is shared by other accounts, shows a warning
     naming how many/which accounts are affected before the fork-vs-update choice
     from the shared submit logic applies — computed client-side by filtering
     `useListAccounts()` data on `institution_id`, no new endpoint needed since
     `AccountView` already exposes it.
4. Submit: (if "Edit mapping" was used) run the shared fork-or-update logic, then
   always `POST /imports` (multipart: `account_id` + file).
5. On success: close the modal, invalidate the imports-list query so `ImportsTab`
   refreshes with the new pending import.

## Verification

- `go build ./...` in `services/api` after the `PUT /accounts/{id}` amendment.
- `tsc -b` in `services/web` after the new dependency and components land.
- Manual: create an account via "Skip for now," confirm an institution named after
  it exists with default mapping. Upload a CSV, choose "Use existing mapping,"
  confirm the preview matches. Upload again, choose "Edit mapping," change only a
  column, confirm the account's institution mapping updates in place. Create a
  second account sharing that institution, upload for it, choose "Edit mapping,"
  change the name — confirm a new institution is created and only the second
  account relinks to it (first account's institution/mapping untouched).
