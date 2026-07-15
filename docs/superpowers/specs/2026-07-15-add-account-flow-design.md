# Add Account Flow — Design

## Context

The Sidebar's "+" buttons next to the Active and Passive account sections are
no-ops today (`services/web/src/layout/Sidebar.tsx`) — there is no way to
create an account from the UI at all. Separately, accounts have partial CRUD
support in the API: `GET /accounts` (list), `GET/PUT/DELETE /accounts/{id}`
all exist, but the only creation route is `POST /institutions/{id}/accounts`,
which requires an institution to already exist. Institutions themselves have
full CRUD (`GET/POST /institutions`, `GET/PUT/DELETE /institutions/{id}`).

This work closes both gaps: it gives accounts the same standalone-create
capability institutions already have, and wires a form up to it so users can
actually create an account from the Sidebar.

This is the first of two related features — CSV import (uploading
transactions into an account, including first-time institution column
mapping) is designed and planned separately, after this one ships.

## Requirements

- Clicking "+" in the Active section opens an Add Account form pre-seeded
  with `status: active`; "+" in the Passive section pre-seeds
  `status: passive`. Both remain editable in the form.
- The form collects: `name` (required), `account_type` (checking / savings /
  credit / loan / mortgage / investment), `status` (active / passive),
  optional `balance_cents`, and — only for credit/loan/mortgage — optional
  `interest_rate`, and — only for credit — optional `credit_limit_cents`.
- Institution linking is a three-way choice on the same form:
  1. **Link to existing institution** — select from `useListInstitutions()`.
  2. **Create new institution** — just a name is required. All other
     institution fields (`source_type`, `amount_sign_convention`,
     `dedup_window_days`, `settlement_window_days`, `amount_tolerance_pct`,
     `date_col`/`amount_col`/`merchant_col`) are shown in a collapsed
     "Advanced settings" section, pre-filled with sensible defaults, with a
     note that they'll be finalized during the first CSV import.
  3. **No institution** — a manual/cash account with `institution_id: null`.
     This is the path with no backend support today (see below).
- After creation, the Sidebar's account list refreshes to show the new
  account without a manual reload.

## Frontend (built this session)

- `services/web/src/components/shared/Modal.tsx` — generic reusable modal,
  built on `@radix-ui/react-dialog` (already a dependency, same
  "unstyled Radix primitive + inline theme styles" pattern as the existing
  `TermTooltip`). Handles backdrop click, Escape, and focus management.
  No Add-Account-specific logic — this is a shared primitive.
- `services/web/src/components/account/AddAccountModal.tsx` — the form
  described above, matching the existing input/select/button styling
  conventions from `NewCard.tsx` / `SettingsPage.tsx` (inline styles, CSS
  custom properties, `mutateAsync`/try-catch mutation pattern). The
  "existing" and "new" institution paths already call
  `useCreateInstitutionAccount` (and `useCreateInstitution` for the new-
  institution case) and work end-to-end. The "none" path currently shows an
  explicit error explaining that no standalone create endpoint exists yet —
  this is the gap this design closes.

## Backend addition needed

Add a standalone create route, following the same pattern as the `GET
/accounts` list endpoint added earlier:

- `POST /accounts` in `services/api/handler/accounts.go`, registered
  alongside the existing list/get/update/delete routes in
  `RegisterAccountsRoutes`.
- Request body: same shape as `CreateInstitutionAccountInputBody` (`name`,
  `account_type`, `status`, optional `interest_rate`, `balance_cents`,
  `credit_limit_cents`) plus an optional `institution_id` field.
- Calls the existing `store.CreateAccount(ctx, entityID, store.Account{...})`
  — no store changes needed, since `Account.InstitutionID` is already
  `*string` and the SQL already handles a null institution_id.
- Permission: `accounts:write` (matching the existing update/delete routes).

## Integration work

1. Add the `POST /accounts` handler + route (above).
2. Regenerate the orval client (`just generate` or equivalent) so a
   `useCreateAccount` hook is produced from the new OpenAPI operation.
3. Update `AddAccountModal`'s "none" branch to call `useCreateAccount` with
   `institution_id: null` instead of showing the TODO error.
4. Wire the two Sidebar "+" buttons' `onClick` to open `AddAccountModal`
   with `defaultStatus` set to `'active'` or `'passive'` respectively.
5. Replace Sidebar's manual `getAccounts()` fetch (flagged with
   `// TODO(task-6-11)`) with the generated `useListAccounts` hook. This
   both removes hand-rolled fetch/token code and means the Sidebar's account
   list auto-refreshes via TanStack Query cache invalidation once the create
   mutations succeed (invalidate the accounts list query key on success in
   `AddAccountModal`).

## Verification

- `go build ./...` in `services/api` after adding the handler.
- `tsc -b` in `services/web` after regenerating the client and wiring
  Sidebar.
- Manual check: open the app, click "+" under Active, create a manual
  account with no institution, confirm it appears in the Sidebar list
  without a page reload. Repeat for "link existing" and "create new"
  institution paths, and for the Passive section.
