# Orval + TanStack Client Overhaul Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hand-written axios client and manual data-fetching with orval-generated React Query hooks, TanStack Table for all table surfaces, and TanStack Virtual for virtual scrolling.

**Architecture:** orval owns the HTTP client entirely (Option C — no custom mutator). It calls the single global axios instance. `AuthProvider` registers interceptors on that instance once on mount. TanStack Table handles column model, sort, filter, and expandable rows. TanStack Virtual renders only visible rows and triggers pagination near the bottom.

**Tech Stack:** orval 7, @tanstack/react-query 5, @tanstack/react-table 8, @tanstack/react-virtual 3

## Global Constraints

- orval Option C: no custom mutator file; generated hooks call the global `axios` instance directly
- One axios instance in the entire app — `AuthProvider` registers interceptors exactly once
- `src/api/generated/` is never edited manually
- Pagination uses the `cursor` query param; response envelope has `meta.next_cursor` and `meta.has_more`
- All table surfaces follow: `useGet*Infinite()` → `useReactTable()` → `useVirtualizer()`
- No testing framework in scope for this overhaul

---

## File Map

### Go API changes (Task 1)
| Action | File |
|--------|------|
| Modify | `services/api/handler/transactions.go` |
| Modify | `services/api/store/transactions.go` |
| Modify | `services/api/handler/imports.go` |
| Modify | `services/api/store/imports.go` (or create if missing) |

### Frontend new files
| Action | File |
|--------|------|
| Create | `services/web/orval.config.ts` |
| Create | `services/web/src/auth/tokens.ts` |
| Create | `services/web/src/auth/interceptors.ts` |
| Create | `services/web/src/auth/useAuth.ts` |
| Create | `services/web/src/components/account/EntriesTable.tsx` |
| Create (generated) | `services/web/src/api/generated/**` |

### Frontend rebuilt files
| Action | File |
|--------|------|
| Rebuild | `services/web/src/auth/AuthProvider.tsx` |
| Rebuild | `services/web/src/main.tsx` |
| Rebuild | `services/web/src/components/account/TransactionsTab.tsx` |
| Rebuild | `services/web/src/components/account/ImportsTab.tsx` |
| Rebuild | `services/web/src/components/budget/StackPanel.tsx` |
| Rebuild | `services/web/src/pages/BudgetPage.tsx` (data fetching only) |
| Rebuild | `services/web/src/pages/ReviewPage.tsx` |
| Rebuild | `services/web/src/pages/ActivityPage.tsx` |
| Rebuild | `services/web/src/contexts/JobsContext.tsx` |

### Deleted files
| File |
|------|
| `services/web/src/api/client.ts` |
| `services/web/src/api/resources.ts` |
| `services/web/src/hooks/usePaginated.ts` |

---

## Task 1: Add filter params to API list endpoints

The API currently only supports `cursor` and `limit` on `/transactions` and `/imports`. The frontend needs `account_id` and `entry_id` filters on `/transactions`, and `account_id` on `/imports`.

**Files:**
- Modify: `services/api/handler/transactions.go`
- Modify: `services/api/store/transactions.go`
- Modify: `services/api/handler/imports.go`
- Modify: `services/api/store/imports.go`

**Interfaces:**
- Produces: `GET /transactions?account_id=&entry_id=&cursor=&limit=` and `GET /imports?account_id=&cursor=&limit=`

- [ ] **Step 1: Add filter params to `listTransactionsInput`**

In `services/api/handler/transactions.go`, replace:
```go
type listTransactionsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}
```
With:
```go
type listTransactionsInput struct {
	AccountID string `query:"account_id"`
	EntryID   string `query:"entry_id"`
	Cursor    string `query:"cursor"`
	Limit     int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}
```

- [ ] **Step 2: Pass filters through in `ListTransactions` handler**

In the same file, replace:
```go
	items, err := h.s.ListTransactions(ctx, entityID, limit+1, input.Cursor)
```
With:
```go
	items, err := h.s.ListTransactions(ctx, entityID, input.AccountID, input.EntryID, limit+1, input.Cursor)
```

- [ ] **Step 3: Update the store `ListTransactions` signature**

In `services/api/store/transactions.go`, replace:
```go
func (s *Store) ListTransactions(ctx context.Context, entityID string, limit int, cursor string) ([]Transaction, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM transactions
			WHERE entity_id = $1
			ORDER BY imported_at DESC, id DESC
			LIMIT $2
		`, transactionCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Transaction])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM transactions
		WHERE entity_id = $1
		  AND (imported_at, id::text) < ($2::timestamptz, $3)
		ORDER BY imported_at DESC, id DESC
		LIMIT $4
	`, transactionCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Transaction])
}
```

With:
```go
func (s *Store) ListTransactions(ctx context.Context, entityID, accountID, entryID string, limit int, cursor string) ([]Transaction, error) {
	// Build WHERE clauses dynamically
	args := []any{entityID}
	filters := "entity_id = $1"
	if accountID != "" {
		args = append(args, accountID)
		filters += fmt.Sprintf(" AND account_id = $%d", len(args))
	}
	if entryID != "" {
		args = append(args, entryID)
		filters += fmt.Sprintf(" AND entry_id = $%d", len(args))
	}

	if cursor == "" {
		args = append(args, limit)
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM transactions
			WHERE %s
			ORDER BY imported_at DESC, id DESC
			LIMIT $%d
		`, transactionCols, filters, len(args)), args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Transaction])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	args = append(args, cursorTS, cursorID, limit)
	pCursorTS := len(args) - 2
	pCursorID := len(args) - 1
	pLimit := len(args)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM transactions
		WHERE %s
		  AND (imported_at, id::text) < ($%d::timestamptz, $%d)
		ORDER BY imported_at DESC, id DESC
		LIMIT $%d
	`, transactionCols, filters, pCursorTS, pCursorID, pLimit), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Transaction])
}
```

**Note:** The `entry_id` column must exist in the `transactions` table. Verify with:
```sql
SELECT column_name FROM information_schema.columns
WHERE table_name = 'transactions' AND column_name = 'entry_id';
```
If missing, add it to `transactionCols` and the Store struct after confirming the schema.

- [ ] **Step 4: Add `account_id` filter to `listImportsInput`**

In `services/api/handler/imports.go`, find:
```go
type listImportsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}
```
Replace with:
```go
type listImportsInput struct {
	AccountID string `query:"account_id"`
	Cursor    string `query:"cursor"`
	Limit     int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}
```

Find the `ListImports` handler function body and update the store call to pass `input.AccountID`. Locate the store's `ListImports` method and add `accountID string` as the second argument (after `entityID`), then add:
```sql
AND account_id = $N
```
to the WHERE clause when `accountID != ""` (same dynamic pattern as transactions above).

- [ ] **Step 5: Verify the API compiles**

```bash
cd services/api && go build ./...
```
Expected: exits 0, no output.

- [ ] **Step 6: Regenerate the full spec chain**

```bash
just gen
```
Expected: writes `services/auth/api/openapi.json`, regenerates `services/api/authclient/`, patches it, writes `services/api/api/openapi.json`.

- [ ] **Step 7: Verify new params appear in the spec**

```bash
python3 -c "
import json
spec = json.load(open('services/api/api/openapi.json'))
for path in ['/transactions', '/imports']:
    params = spec['paths'][path]['get'].get('parameters', [])
    print(path, [p['name'] for p in params])
"
```
Expected output:
```
/transactions ['account_id', 'entry_id', 'cursor', 'limit']
/imports ['account_id', 'cursor', 'limit']
```

- [ ] **Step 8: Commit**

```bash
git add services/api/handler/transactions.go services/api/store/transactions.go \
        services/api/handler/imports.go services/api/api/openapi.json
git commit -m "feat: add account_id/entry_id filter params to transaction and import list endpoints"
```

---

## Task 2: Install frontend dependencies + orval config

**Files:**
- Modify: `services/web/package.json` (via npm install)
- Create: `services/web/orval.config.ts`

**Interfaces:**
- Produces: `npx orval` works and writes `src/api/generated/`

- [ ] **Step 1: Install TanStack + orval**

```bash
cd services/web
npm install @tanstack/react-query@^5 @tanstack/react-table@^8 @tanstack/react-virtual@^3
npm install --save-dev orval@^7
```
Expected: package.json updated, no peer-dep errors.

- [ ] **Step 2: Write `orval.config.ts`**

Create `services/web/orval.config.ts`:
```ts
import { defineConfig } from 'orval'

export default defineConfig({
  veloci: {
    input: { target: '../api/api/openapi.json' },
    output: {
      mode: 'split',
      target: 'src/api/generated',
      client: 'react-query',
      override: {
        query: {
          useInfiniteQuery: true,
          useQuery: true,
          options: {
            getNextPageParam: '(lastPage) => lastPage?.meta?.next_cursor ?? undefined',
          },
        },
      },
    },
  },
})
```

- [ ] **Step 3: Run orval**

```bash
cd services/web && npx orval
```
Expected: `src/api/generated/` directory created with files like `transactions.ts`, `entries.ts`, `imports.ts`, etc. No errors.

- [ ] **Step 4: Verify hook names**

```bash
grep -h "^export function use" services/web/src/api/generated/*.ts | sort | head -30
```
Expected: functions like `useListTransactions`, `useListTransactionsInfinite`, `useListEntries`, `useListEntriesInfinite`, `useLogin`, `useLogout`, etc.

- [ ] **Step 5: Add `gen-web` to Justfile**

In `Justfile`, find:
```just
gen: gen-api
```

Replace with:
```just
# Generate web client from api spec (requires gen-api to have run first)
gen-web:
    cd services/web && npx orval

gen: gen-api gen-web
```

- [ ] **Step 6: Commit**

```bash
cd /path/to/veloci
git add services/web/package.json services/web/package-lock.json \
        services/web/orval.config.ts services/web/src/api/generated/ Justfile
git commit -m "feat: add orval + TanStack deps, generate React Query client from API spec"
```

---

## Task 3: Auth package — tokens + interceptors

**Files:**
- Create: `services/web/src/auth/tokens.ts`
- Create: `services/web/src/auth/interceptors.ts`

**Interfaces:**
- Produces:
  - `getToken(): string | null`
  - `setToken(t: string): void`
  - `clearToken(): void`
  - `registerAuthInterceptors(instance: AxiosStatic): void`

- [ ] **Step 1: Create `tokens.ts`**

Create `services/web/src/auth/tokens.ts`:
```ts
const KEY = 'veloci_token'

export const getToken = (): string | null => localStorage.getItem(KEY)
export const setToken = (t: string): void => { localStorage.setItem(KEY, t) }
export const clearToken = (): void => { localStorage.removeItem(KEY) }
```

- [ ] **Step 2: Create `interceptors.ts`**

Create `services/web/src/auth/interceptors.ts`:
```ts
import type { AxiosStatic } from 'axios'
import { getToken, clearToken } from './tokens'

export function registerAuthInterceptors(instance: AxiosStatic): void {
  instance.defaults.baseURL = import.meta.env.VITE_API_URL ?? '/api'

  instance.interceptors.request.use((config) => {
    const token = getToken()
    if (token) config.headers.Authorization = `Bearer ${token}`
    return config
  })

  instance.interceptors.response.use(
    (r) => r,
    (err: unknown) => {
      if (
        err instanceof Object &&
        'response' in err &&
        (err as { response?: { status?: number } }).response?.status === 401
      ) {
        clearToken()
      }
      return Promise.reject(err)
    },
  )
}
```

- [ ] **Step 3: Commit**

```bash
git add services/web/src/auth/tokens.ts services/web/src/auth/interceptors.ts
git commit -m "feat: add auth token storage and axios interceptor registration"
```

---

## Task 4: Rebuild AuthProvider + add useAuth + update main.tsx

**Files:**
- Rebuild: `services/web/src/auth/AuthProvider.tsx`
- Create: `services/web/src/auth/useAuth.ts`
- Rebuild: `services/web/src/main.tsx`

**Interfaces:**
- Consumes: `tokens.ts` (getToken, setToken, clearToken), `interceptors.ts` (registerAuthInterceptors), generated `useLogin` / `useLogout` mutations
- Produces:
  - `<AuthProvider>` — wraps app in QueryClientProvider, registers interceptors, owns auth state
  - `useLogin()` → `(email, password) => Promise<void>`
  - `useLogout()` → `() => void`

- [ ] **Step 1: Rebuild `AuthProvider.tsx`**

Overwrite `services/web/src/auth/AuthProvider.tsx`:
```tsx
import React, { createContext, useContext, useState, useEffect } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import axios from 'axios'
import { registerAuthInterceptors } from './interceptors'
import { getToken } from './tokens'

interface AuthContextValue {
  authenticated: boolean
  setAuthenticated: (v: boolean) => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
    },
  },
})

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [authenticated, setAuthenticated] = useState(() => !!getToken())

  useEffect(() => {
    registerAuthInterceptors(axios)
  }, [])

  return (
    <QueryClientProvider client={queryClient}>
      <AuthContext.Provider value={{ authenticated, setAuthenticated }}>
        {children}
      </AuthContext.Provider>
    </QueryClientProvider>
  )
}

export function useAuthContext(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuthContext must be used within AuthProvider')
  return ctx
}
```

- [ ] **Step 2: Create `useAuth.ts`**

Create `services/web/src/auth/useAuth.ts`:
```ts
import { useNavigate } from 'react-router-dom'
import { useLogin as useLoginMutation, useLogout as useLogoutMutation } from '../api/generated/auth'
import { setToken, clearToken, getToken } from './tokens'
import { useAuthContext } from './AuthProvider'

export function useAuth() {
  const { authenticated, setAuthenticated } = useAuthContext()
  const navigate = useNavigate()
  const loginMutation = useLoginMutation()
  const logoutMutation = useLogoutMutation()

  function login(email: string, password: string): Promise<void> {
    return loginMutation.mutateAsync({ body: { email, password } }).then((data) => {
      setToken(data.data.token)
      setAuthenticated(true)
    })
  }

  function logout(): void {
    const token = getToken()
    if (token) {
      logoutMutation.mutate({ body: { jti: token } })
    }
    clearToken()
    setAuthenticated(false)
    navigate('/login')
  }

  return { authenticated, login, logout }
}
```

**Note:** The generated `useLogin` and `useLogout` hook names depend on orval's output. Check `src/api/generated/auth.ts` for the exact exported mutation hook names. If orval generates `useLogin` as a React Query mutation, the above is correct. If it's named differently (e.g., `useLoginMutation`), update the import accordingly.

- [ ] **Step 3: Update `LoginPage.tsx` to use `useAuth`**

Open `services/web/src/auth/LoginPage.tsx`. Find the import of auth functions and replace with:
```ts
import { useAuth } from './useAuth'
```

In the component, replace:
```ts
const { login } = useAuth() // old AuthProvider hook
```
with:
```ts
const { login } = useAuth() // new useAuth hook
```

The `login` signature is identical (`(email, password) => Promise<void>`) so no other changes should be needed.

- [ ] **Step 4: Rebuild `main.tsx`**

Overwrite `services/web/src/main.tsx`:
```tsx
import React from 'react'
import ReactDOM from 'react-dom/client'
import './index.css'
import App from './App'
import { AuthProvider } from './auth/AuthProvider'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <AuthProvider>
      <App />
    </AuthProvider>
  </React.StrictMode>,
)
```

- [ ] **Step 5: Update `App.tsx` to remove old QueryClient / auth wrapping if present**

Check `services/web/src/App.tsx` for any existing `AuthProvider` wrapping or QueryClient setup and remove it — those now live in `main.tsx`.

- [ ] **Step 6: TypeScript check**

```bash
cd services/web && npx tsc --noEmit
```
Expected: no errors in `src/auth/`.

- [ ] **Step 7: Commit**

```bash
git add services/web/src/auth/ services/web/src/main.tsx services/web/src/App.tsx
git commit -m "feat: rebuild AuthProvider with QueryClient + axios interceptors, add useAuth hook"
```

---

## Task 5: Delete old API layer

**Files:**
- Delete: `services/web/src/api/client.ts`
- Delete: `services/web/src/api/resources.ts`
- Delete: `services/web/src/hooks/usePaginated.ts`

- [ ] **Step 1: Delete files**

```bash
rm services/web/src/api/client.ts
rm services/web/src/api/resources.ts
rm services/web/src/hooks/usePaginated.ts
```

- [ ] **Step 2: Fix all broken imports**

```bash
cd services/web && npx tsc --noEmit 2>&1 | grep "Cannot find module"
```

For each broken import, update to use either the generated client (`../api/generated/...`) or the new auth package. Common replacements:

| Old import | New import |
|------------|------------|
| `from '../api/resources'` (types) | Use generated types from `../api/generated/<tag>` |
| `from '../api/client'` (login/logout) | `from '../auth/useAuth'` |
| `from '../hooks/usePaginated'` | Inline `useInfiniteQuery` from generated hook |

- [ ] **Step 3: TypeScript check**

```bash
cd services/web && npx tsc --noEmit
```
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add -A services/web/src/
git commit -m "refactor: remove hand-written api client, resources, and usePaginated"
```

---

## Task 6: Rebuild TransactionsTab (stage-0 flat table)

This is the **raw transactions** view — flat list, one row per transaction. No grouping. Uses the generated `useListTransactionsInfinite` hook.

**Files:**
- Rebuild: `services/web/src/components/account/TransactionsTab.tsx`

**Data shape from API:** `TransactionView` fields: `id`, `account_id`, `import_batch_id`, `date`, `amount_cents`, `imported_payee`, `merchant_normalized`, `imported_id`, `settlement_status`, `imported_at`

**Interfaces:**
- Consumes: `useListTransactionsInfinite` from `../../api/generated/transactions`
- Props: `accountId: string`

- [ ] **Step 1: Write the rebuilt component**

Overwrite `services/web/src/components/account/TransactionsTab.tsx`:
```tsx
import { useRef, useCallback } from 'react'
import {
  useReactTable,
  getCoreRowModel,
  getSortedRowModel,
  getFilteredRowModel,
  flexRender,
  createColumnHelper,
  type SortingState,
  type ColumnFiltersState,
} from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useListTransactionsInfinite } from '../../api/generated/transactions'
import type { TransactionView } from '../../api/generated/model'
import { useState } from 'react'

const columnHelper = createColumnHelper<TransactionView>()

function formatAmount(cents: number): string {
  return (Math.abs(cents) / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })
}

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })
}

const columns = [
  columnHelper.accessor('date', {
    header: 'Date',
    size: 90,
    cell: (info) => formatDate(info.getValue()),
  }),
  columnHelper.accessor('merchant_normalized', {
    header: 'Merchant',
    cell: (info) => info.getValue(),
  }),
  columnHelper.accessor('imported_payee', {
    header: 'Raw Payee',
    cell: (info) => info.getValue(),
  }),
  columnHelper.accessor('amount_cents', {
    header: 'Amount',
    size: 100,
    cell: (info) => formatAmount(info.getValue()),
  }),
  columnHelper.accessor('settlement_status', {
    header: 'Status',
    size: 80,
    cell: (info) => info.getValue(),
  }),
]

interface TransactionsTabProps {
  accountId: string
}

export function TransactionsTab({ accountId }: TransactionsTabProps) {
  const parentRef = useRef<HTMLDivElement>(null)
  const [sorting, setSorting] = useState<SortingState>([])
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])

  const { data, fetchNextPage, hasNextPage, isFetching } = useListTransactionsInfinite(
    { account_id: accountId, limit: 50 },
    { query: { enabled: !!accountId } },
  )

  const rows = data?.pages.flatMap((p) => p.data ?? []) ?? []

  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, columnFilters },
    onSortingChange: setSorting,
    onColumnFiltersChange: setColumnFilters,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
  })

  const tableRows = table.getRowModel().rows

  const virtualizer = useVirtualizer({
    count: tableRows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 36,
    overscan: 10,
    onChange: (instance) => {
      const lastItem = instance.getVirtualItems().at(-1)
      if (lastItem && lastItem.index >= tableRows.length - 20 && hasNextPage && !isFetching) {
        void fetchNextPage()
      }
    },
  })

  if (!data && isFetching) {
    return <div style={{ padding: 20, color: 'var(--text3)' }}>Loading...</div>
  }

  if (rows.length === 0 && !isFetching) {
    return (
      <div style={{ padding: 32, textAlign: 'center', color: 'var(--text3)' }}>
        No transactions for this account.
      </div>
    )
  }

  return (
    <div>
      {/* Filter bar */}
      <div style={{ display: 'flex', gap: 8, padding: '8px 16px', borderBottom: '1px solid var(--border)' }}>
        <input
          type="text"
          placeholder="Filter merchant..."
          onChange={(e) =>
            table.getColumn('merchant_normalized')?.setFilterValue(e.target.value)
          }
          style={{
            background: 'var(--surface2)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text)',
            fontSize: 12,
            outline: 'none',
            width: 200,
          }}
        />
        <select
          onChange={(e) =>
            table.getColumn('settlement_status')?.setFilterValue(e.target.value || undefined)
          }
          style={{
            background: 'var(--surface2)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text2)',
            fontSize: 12,
          }}
        >
          <option value="">All statuses</option>
          <option value="cleared">Cleared</option>
          <option value="pending">Pending</option>
        </select>
      </div>

      {/* Column headers */}
      <div style={{ borderBottom: '1px solid var(--border)', background: 'var(--bg)' }}>
        {table.getHeaderGroups().map((headerGroup) => (
          <div key={headerGroup.id} style={{ display: 'flex', padding: '5px 16px', gap: 8 }}>
            {headerGroup.headers.map((header) => (
              <div
                key={header.id}
                style={{
                  flex: header.column.getSize() ? `0 0 ${header.column.getSize()}px` : 1,
                  fontSize: 11,
                  color: 'var(--text3)',
                  textTransform: 'uppercase',
                  letterSpacing: '0.04em',
                  cursor: header.column.getCanSort() ? 'pointer' : 'default',
                  userSelect: 'none',
                }}
                onClick={header.column.getToggleSortingHandler()}
              >
                {flexRender(header.column.columnDef.header, header.getContext())}
                {header.column.getIsSorted() === 'asc' ? ' ↑' : header.column.getIsSorted() === 'desc' ? ' ↓' : ''}
              </div>
            ))}
          </div>
        ))}
      </div>

      {/* Virtual rows */}
      <div ref={parentRef} style={{ height: 480, overflowY: 'auto' }}>
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const row = tableRows[virtualRow.index]
            return (
              <div
                key={row.id}
                style={{
                  position: 'absolute',
                  top: virtualRow.start,
                  left: 0,
                  right: 0,
                  height: virtualRow.size,
                  display: 'flex',
                  alignItems: 'center',
                  padding: '0 16px',
                  gap: 8,
                  borderBottom: '1px solid var(--border)',
                  background: 'var(--surface)',
                }}
              >
                {row.getVisibleCells().map((cell) => (
                  <div
                    key={cell.id}
                    style={{
                      flex: cell.column.getSize() ? `0 0 ${cell.column.getSize()}px` : 1,
                      fontSize: 13,
                      color: 'var(--text)',
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                    }}
                  >
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </div>
                ))}
              </div>
            )
          })}
        </div>
      </div>

      {isFetching && (
        <div style={{ padding: '8px 16px', color: 'var(--text3)', fontSize: 12 }}>
          Loading more...
        </div>
      )}
    </div>
  )
}
```

**Note:** The exact generated hook name (`useListTransactionsInfinite`) and type path (`../../api/generated/model`) depend on orval's output. Verify by running:
```bash
grep -r "useListTransactions" services/web/src/api/generated/
grep -r "TransactionView" services/web/src/api/generated/
```
Adjust imports if names differ.

- [ ] **Step 2: TypeScript check**

```bash
cd services/web && npx tsc --noEmit
```
Expected: no errors in `TransactionsTab.tsx`.

- [ ] **Step 3: Commit**

```bash
git add services/web/src/components/account/TransactionsTab.tsx
git commit -m "feat: rebuild TransactionsTab as stage-0 flat virtual table via react-query + tanstack-table"
```

---

## Task 7: New EntriesTable (stage-3 entry rows + transaction detail)

This is the **entry-grouped view** — entry rows with expandable transaction detail. Two separate data layers: entries from `useListEntriesInfinite`, per-entry transactions from `useListTransactionsInfinite({ entry_id })` loaded on expansion.

**Files:**
- Create: `services/web/src/components/account/EntriesTable.tsx`

**Data shapes:**
- `EntryView`: `id`, `name`, `direction`, `entry_type`, `label_id`, `label_name`, `actual_rate`, `projected_rate`, `drift_rate`, `tag`, `period`, `status`, `start_date`, `end_date`, `priority`, `source`, `conditions`, `created_at`
- `TransactionView`: same fields as Task 6

**Interfaces:**
- Consumes: `useListEntriesInfinite`, `useListTransactionsInfinite`
- Props: `accountId: string` (for filtering; entry-level transactions are filtered by entry_id per expansion)

- [ ] **Step 1: Create `EntriesTable.tsx`**

Create `services/web/src/components/account/EntriesTable.tsx`:
```tsx
import { useState, useRef, Fragment } from 'react'
import {
  useReactTable,
  getCoreRowModel,
  getSortedRowModel,
  getFilteredRowModel,
  getExpandedRowModel,
  flexRender,
  createColumnHelper,
  type SortingState,
  type ColumnFiltersState,
  type ExpandedState,
} from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useListEntriesInfinite, useListTransactionsInfinite } from '../../api/generated'
import type { EntryView, TransactionView } from '../../api/generated/model'
import { LabelPill } from '../shared/LabelPill'

const columnHelper = createColumnHelper<EntryView>()

function formatRate(rate: number): string {
  const monthly = rate * 30
  return `$${(Math.abs(monthly) / 100).toFixed(0)}/mo`
}

const columns = [
  columnHelper.display({
    id: 'expander',
    size: 28,
    cell: ({ row }) => (
      <button
        onClick={row.getToggleExpandedHandler()}
        style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 0, color: 'var(--text3)' }}
      >
        {row.getIsExpanded() ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
      </button>
    ),
  }),
  columnHelper.accessor('name', {
    header: 'Entry',
    cell: (info) => (
      <span style={{ fontWeight: 600, fontSize: 13 }}>{info.getValue()}</span>
    ),
  }),
  columnHelper.accessor('entry_type', {
    header: 'Type',
    size: 90,
    cell: (info) => info.getValue(),
  }),
  columnHelper.accessor('direction', {
    header: 'Dir',
    size: 70,
    cell: (info) => (
      <span style={{ color: info.getValue() === 'income' ? 'var(--income)' : 'var(--commit)' }}>
        {info.getValue()}
      </span>
    ),
  }),
  columnHelper.accessor('actual_rate', {
    header: 'Rate/day',
    size: 90,
    cell: (info) => formatRate(info.getValue()),
  }),
  columnHelper.accessor('label_name', {
    header: 'Label',
    size: 100,
    cell: (info) => info.getValue() ? <LabelPill name={info.getValue()} /> : null,
  }),
  columnHelper.accessor('status', {
    header: 'Status',
    size: 80,
    cell: (info) => info.getValue(),
  }),
]

function TransactionSubTable({ entryId }: { entryId: string }) {
  const { data, fetchNextPage, hasNextPage, isFetching } = useListTransactionsInfinite(
    { entry_id: entryId, limit: 25 },
  )
  const rows = data?.pages.flatMap((p) => p.data ?? []) ?? []

  if (!data && isFetching) {
    return <div style={{ padding: '8px 28px', color: 'var(--text3)', fontSize: 12 }}>Loading transactions...</div>
  }

  if (rows.length === 0) {
    return <div style={{ padding: '8px 28px', color: 'var(--text3)', fontSize: 12 }}>No matched transactions.</div>
  }

  return (
    <div style={{ background: 'var(--bg)', borderTop: '1px solid var(--border)' }}>
      {/* Sub-table header */}
      <div style={{ display: 'flex', gap: 8, padding: '4px 28px', borderBottom: '1px solid var(--border)' }}>
        {['Date', 'Merchant', 'Amount'].map((h) => (
          <div key={h} style={{ flex: h === 'Amount' ? '0 0 90px' : 1, fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}>
            {h}
          </div>
        ))}
      </div>
      {rows.map((tx: TransactionView) => (
        <div key={tx.id} style={{ display: 'flex', gap: 8, padding: '5px 28px', borderBottom: '1px solid var(--border)', alignItems: 'center' }}>
          <div style={{ flex: 1, fontSize: 12, color: 'var(--text2)' }}>
            {new Date(tx.date).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}
          </div>
          <div style={{ flex: 3, fontSize: 12, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {tx.merchant_normalized}
          </div>
          <div style={{ flex: '0 0 90px', fontSize: 12, color: 'var(--text)', textAlign: 'right' }}>
            {(Math.abs(tx.amount_cents) / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })}
          </div>
        </div>
      ))}
      {hasNextPage && (
        <button
          onClick={() => void fetchNextPage()}
          disabled={isFetching}
          style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '6px 28px', color: 'var(--accent)', fontSize: 12 }}
        >
          {isFetching ? 'Loading...' : 'Load more'}
        </button>
      )}
    </div>
  )
}

interface EntriesTableProps {
  accountId?: string
}

export function EntriesTable({ accountId: _accountId }: EntriesTableProps) {
  const parentRef = useRef<HTMLDivElement>(null)
  const [sorting, setSorting] = useState<SortingState>([])
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])
  const [expanded, setExpanded] = useState<ExpandedState>({})

  const { data, fetchNextPage, hasNextPage, isFetching } = useListEntriesInfinite(
    { limit: 50 },
  )

  const rows = data?.pages.flatMap((p) => p.data ?? []) ?? []

  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, columnFilters, expanded },
    onSortingChange: setSorting,
    onColumnFiltersChange: setColumnFilters,
    onExpandedChange: setExpanded,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getExpandedRowModel: getExpandedRowModel(),
    getRowCanExpand: () => true,
  })

  const tableRows = table.getRowModel().rows

  const virtualizer = useVirtualizer({
    count: tableRows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: (i) => tableRows[i]?.getIsExpanded() ? 200 : 38,
    overscan: 5,
    onChange: (instance) => {
      const lastItem = instance.getVirtualItems().at(-1)
      if (lastItem && lastItem.index >= tableRows.length - 10 && hasNextPage && !isFetching) {
        void fetchNextPage()
      }
    },
  })

  if (!data && isFetching) {
    return <div style={{ padding: 20, color: 'var(--text3)' }}>Loading...</div>
  }

  if (rows.length === 0 && !isFetching) {
    return <div style={{ padding: 32, textAlign: 'center', color: 'var(--text3)' }}>No entries found.</div>
  }

  return (
    <div>
      {/* Filter bar */}
      <div style={{ display: 'flex', gap: 8, padding: '8px 16px', borderBottom: '1px solid var(--border)' }}>
        <select
          onChange={(e) => table.getColumn('direction')?.setFilterValue(e.target.value || undefined)}
          style={{ background: 'var(--surface2)', border: '1px solid var(--border)', borderRadius: 4, padding: '4px 8px', color: 'var(--text2)', fontSize: 12 }}
        >
          <option value="">All directions</option>
          <option value="income">Income</option>
          <option value="expense">Expense</option>
        </select>
        <select
          onChange={(e) => table.getColumn('entry_type')?.setFilterValue(e.target.value || undefined)}
          style={{ background: 'var(--surface2)', border: '1px solid var(--border)', borderRadius: 4, padding: '4px 8px', color: 'var(--text2)', fontSize: 12 }}
        >
          <option value="">All types</option>
          <option value="standing">Standing</option>
          <option value="variable">Variable</option>
          <option value="irregular">Irregular</option>
        </select>
        <select
          onChange={(e) => table.getColumn('status')?.setFilterValue(e.target.value || undefined)}
          style={{ background: 'var(--surface2)', border: '1px solid var(--border)', borderRadius: 4, padding: '4px 8px', color: 'var(--text2)', fontSize: 12 }}
        >
          <option value="">All statuses</option>
          <option value="active">Active</option>
          <option value="inactive">Inactive</option>
          <option value="pending_review">Pending review</option>
        </select>
      </div>

      {/* Column headers */}
      <div style={{ borderBottom: '1px solid var(--border)', background: 'var(--bg)' }}>
        {table.getHeaderGroups().map((headerGroup) => (
          <div key={headerGroup.id} style={{ display: 'flex', padding: '5px 16px', gap: 8, alignItems: 'center' }}>
            {headerGroup.headers.map((header) => (
              <div
                key={header.id}
                style={{
                  flex: header.column.getSize() ? `0 0 ${header.column.getSize()}px` : 1,
                  fontSize: 11,
                  color: 'var(--text3)',
                  textTransform: 'uppercase',
                  letterSpacing: '0.04em',
                  cursor: header.column.getCanSort() ? 'pointer' : 'default',
                  userSelect: 'none',
                }}
                onClick={header.column.getToggleSortingHandler()}
              >
                {flexRender(header.column.columnDef.header, header.getContext())}
                {header.column.getIsSorted() === 'asc' ? ' ↑' : header.column.getIsSorted() === 'desc' ? ' ↓' : ''}
              </div>
            ))}
          </div>
        ))}
      </div>

      {/* Rows (not fully virtualised when expanded — estimateSize handles the height jump) */}
      <div ref={parentRef} style={{ height: 520, overflowY: 'auto' }}>
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const row = tableRows[virtualRow.index]
            const isExpanded = row.getIsExpanded()
            return (
              <div
                key={row.id}
                style={{ position: 'absolute', top: virtualRow.start, left: 0, right: 0 }}
              >
                {/* Entry row */}
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    padding: '0 16px',
                    height: 38,
                    gap: 8,
                    borderBottom: isExpanded ? 'none' : '1px solid var(--border)',
                    background: isExpanded ? 'var(--surface2)' : 'var(--surface)',
                    cursor: 'pointer',
                  }}
                  onClick={row.getToggleExpandedHandler()}
                >
                  {row.getVisibleCells().map((cell) => (
                    <div
                      key={cell.id}
                      style={{
                        flex: cell.column.getSize() ? `0 0 ${cell.column.getSize()}px` : 1,
                        fontSize: 13,
                        color: 'var(--text)',
                        overflow: 'hidden',
                        textOverflow: 'ellipsis',
                        whiteSpace: 'nowrap',
                      }}
                    >
                      {flexRender(cell.column.columnDef.cell, cell.getContext())}
                    </div>
                  ))}
                </div>

                {/* Expanded transaction sub-table */}
                {isExpanded && (
                  <TransactionSubTable entryId={row.original.id} />
                )}
              </div>
            )
          })}
        </div>
      </div>

      {isFetching && (
        <div style={{ padding: '8px 16px', color: 'var(--text3)', fontSize: 12 }}>Loading more...</div>
      )}
    </div>
  )
}
```

- [ ] **Step 2: Wire into AccountPage**

Open `services/web/src/pages/AccountPage.tsx`. Find the tabs section and add an "Entries" tab that renders `<EntriesTable accountId={account.id} />`. Import `EntriesTable` from the component file. The existing `TransactionsTab` prop should already receive `accountId`.

- [ ] **Step 3: TypeScript check**

```bash
cd services/web && npx tsc --noEmit
```

- [ ] **Step 4: Commit**

```bash
git add services/web/src/components/account/EntriesTable.tsx services/web/src/pages/AccountPage.tsx
git commit -m "feat: add EntriesTable with expandable entry rows and per-entry transaction detail"
```

---

## Task 8: Rebuild ImportsTab

**Files:**
- Rebuild: `services/web/src/components/account/ImportsTab.tsx`

**Data shape from API:** `ImportView`: `id`, `account_id`, `institution_id`, `uploaded_by`, `uploaded_at`, `date_range_start`, `date_range_end`, `row_count`, `status`, `job_id`, `error`

**Note:** The old `ImportBatch` type had `processed_at`, `transactions_imported`, `transactions_skipped_duplicate`, `source_name`. The new `ImportView` uses `uploaded_at` (not `processed_at`), `row_count`, and lacks `source_name`. Adapt column headers accordingly.

- [ ] **Step 1: Write the rebuilt component**

Overwrite `services/web/src/components/account/ImportsTab.tsx`:
```tsx
import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useListImports, useListTransactionsInfinite } from '../../api/generated'
import type { ImportView, TransactionView } from '../../api/generated/model'

interface ImportsTabProps {
  accountId: string
}

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString('en-US', { month: 'short', day: 'numeric', year: 'numeric' })
}

function formatAmount(cents: number): string {
  return (Math.abs(cents) / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })
}

function BatchTransactions({ importBatchId }: { importBatchId: string }) {
  const { data, fetchNextPage, hasNextPage, isFetching } = useListTransactionsInfinite(
    { import_batch_id: importBatchId, limit: 100 },
  )
  const txs = data?.pages.flatMap((p) => p.data ?? []) ?? []

  if (!data && isFetching) {
    return <div style={{ padding: '8px 28px', color: 'var(--text3)', fontSize: 12 }}>Loading...</div>
  }

  return (
    <div style={{ background: 'var(--bg)', borderTop: '1px solid var(--border)' }}>
      {/* Header */}
      <div style={{ display: 'flex', gap: 8, padding: '4px 28px', borderBottom: '1px solid var(--border)' }}>
        {['Date', 'Raw Payee', 'Normalized', 'Amount'].map((h) => (
          <div
            key={h}
            style={{ flex: h === 'Amount' ? '0 0 88px' : 1, fontSize: 10, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.04em' }}
          >
            {h}
          </div>
        ))}
      </div>
      {txs.map((tx: TransactionView) => (
        <div key={tx.id} style={{ display: 'flex', gap: 8, padding: '5px 28px', borderBottom: '1px solid var(--border)', alignItems: 'center' }}>
          <div style={{ flex: 1, fontSize: 12, color: 'var(--text2)' }}>
            {new Date(tx.date).toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}
          </div>
          <div style={{ flex: 2, fontSize: 12, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {tx.imported_payee}
          </div>
          <div style={{ flex: 2, fontSize: 12, color: 'var(--text3)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {tx.merchant_normalized}
          </div>
          <div style={{ flex: '0 0 88px', fontSize: 12, color: 'var(--text)', textAlign: 'right' }}>
            {formatAmount(tx.amount_cents)}
          </div>
        </div>
      ))}
      {hasNextPage && (
        <button
          onClick={() => void fetchNextPage()}
          disabled={isFetching}
          style={{ background: 'none', border: 'none', cursor: 'pointer', padding: '6px 28px', color: 'var(--accent)', fontSize: 12 }}
        >
          {isFetching ? 'Loading...' : 'Load more'}
        </button>
      )}
      {txs.length === 0 && !isFetching && (
        <div style={{ padding: '8px 28px', color: 'var(--text3)', fontSize: 12 }}>No transactions in this import.</div>
      )}
    </div>
  )
}

export function ImportsTab({ accountId }: ImportsTabProps) {
  const [expandedBatches, setExpandedBatches] = useState<Set<string>>(new Set())
  const [searchTerm, setSearchTerm] = useState('')

  const { data, isFetching } = useListImports({ account_id: accountId, limit: 50 })
  const batches: ImportView[] = data?.data ?? []

  function toggleBatch(id: string) {
    setExpandedBatches((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  if (!data && isFetching) {
    return <div style={{ padding: 20, color: 'var(--text3)' }}>Loading...</div>
  }

  if (batches.length === 0) {
    return (
      <div style={{ padding: 32, textAlign: 'center', color: 'var(--text3)' }}>
        No imports for this account.
      </div>
    )
  }

  return (
    <div>
      {/* Filter bar */}
      <div style={{ display: 'flex', gap: 8, padding: '8px 16px', borderBottom: '1px solid var(--border)' }}>
        <input
          type="text"
          placeholder="Search merchant..."
          value={searchTerm}
          onChange={(e) => setSearchTerm(e.target.value)}
          style={{ background: 'var(--surface2)', border: '1px solid var(--border)', borderRadius: 4, padding: '4px 8px', color: 'var(--text)', fontSize: 12, outline: 'none', width: 200 }}
        />
      </div>

      {batches.map((batch) => {
        const isExpanded = expandedBatches.has(batch.id)
        return (
          <div key={batch.id} style={{ borderBottom: '1px solid var(--border)' }}>
            <div
              style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '10px 16px', cursor: 'pointer', background: 'var(--surface)', userSelect: 'none' }}
              onClick={() => toggleBatch(batch.id)}
            >
              <span style={{ color: 'var(--text3)', flexShrink: 0 }}>
                {isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              </span>
              <span style={{ fontWeight: 600, fontSize: 13, color: 'var(--text)' }}>
                Uploaded {formatDate(batch.uploaded_at)}
              </span>
              <span style={{ fontSize: 12, color: 'var(--text2)' }}>
                {batch.status}
              </span>
              <div style={{ flex: 1 }} />
              {batch.row_count != null && (
                <span style={{ fontSize: 12, color: 'var(--text2)' }}>
                  {batch.row_count} rows
                </span>
              )}
              {batch.date_range_start && batch.date_range_end && (
                <span style={{ fontSize: 12, color: 'var(--text3)' }}>
                  {formatDate(batch.date_range_start)} – {formatDate(batch.date_range_end)}
                </span>
              )}
            </div>
            {isExpanded && <BatchTransactions importBatchId={batch.id} />}
          </div>
        )
      })}
    </div>
  )
}
```

**Note:** The `import_batch_id` filter on transactions requires the API change from Task 1. Also check if `useListImports` is the generated hook name (it may be `useGetImports` or `useListImports` — verify with `grep "export function use" src/api/generated/imports.ts`).

- [ ] **Step 2: TypeScript check**

```bash
cd services/web && npx tsc --noEmit
```

- [ ] **Step 3: Commit**

```bash
git add services/web/src/components/account/ImportsTab.tsx
git commit -m "feat: rebuild ImportsTab using generated hooks and ImportView schema"
```

---

## Task 9: Rebuild StackPanel (Budget page entry list)

The `StackPanel` receives entries as props from `BudgetPage`. We update `BudgetPage` to use the generated hook and update `StackPanel` to use `EntryView` field names. No TanStack Table needed — it's a label-grouped expandable UI, not a sortable table.

**Files:**
- Rebuild: `services/web/src/pages/BudgetPage.tsx` (data fetch section only)
- Rebuild: `services/web/src/components/budget/StackPanel.tsx` (field names)

**Field name changes:**
- `Entry.entry_type` → `EntryView.entry_type` (same)
- `Entry.label_id` / `label_name` → `EntryView.label_id` / `label_name` (same)
- `Entry.actual_rate` → `EntryView.actual_rate` (same)
- `Entry.tag` → `EntryView.tag` (same)
- No field changes needed — `EntryView` matches the old `Entry` interface for StackPanel's used fields

- [ ] **Step 1: Update `BudgetPage.tsx` data fetching**

Open `services/web/src/pages/BudgetPage.tsx`. Find the data-fetching code (currently calls `getEntries()` from resources). Replace the import and data fetch:

Old:
```ts
import { getEntries } from '../api/resources'
// ...
const [entries, setEntries] = useState<Entry[]>([])
useEffect(() => { void getEntries().then(setEntries) }, [])
```

New:
```ts
import { useListEntries } from '../api/generated/entries'
// ...
const { data, isFetching } = useListEntries({})
const entries = data?.data ?? []
const loading = isFetching
```

Remove the `useState` and `useEffect` for entries. Pass `entries` and `loading` to `<StackPanel>`.

- [ ] **Step 2: Update `StackPanel.tsx` type imports**

Open `services/web/src/components/budget/StackPanel.tsx`. Replace:
```ts
import type { Entry } from '../../api/resources'
```
With:
```ts
import type { EntryView } from '../../api/generated/model'
```

Replace all occurrences of `Entry` type with `EntryView` in the file. The field names used by StackPanel (`id`, `name`, `direction`, `entry_type`, `label_id`, `label_name`, `actual_rate`, `projected_rate`, `drift_rate`, `tag`, `period`, `status`) all exist on `EntryView`.

- [ ] **Step 3: TypeScript check**

```bash
cd services/web && npx tsc --noEmit
```

- [ ] **Step 4: Commit**

```bash
git add services/web/src/pages/BudgetPage.tsx services/web/src/components/budget/StackPanel.tsx
git commit -m "feat: update BudgetPage and StackPanel to use generated useListEntries hook"
```

---

## Task 10: Rebuild ReviewPage

Replace manual cursor pagination with `useListReviewInfinite`. Keep card layout — no TanStack Table.

**Files:**
- Rebuild: `services/web/src/pages/ReviewPage.tsx`

**Field name changes from `ReviewItem` → `ReviewView`:**
- `ReviewItem.suggested_name` → `ReviewView.suggested_name` ✓
- `ReviewItem.alert_type` → `ReviewView.alert_type` ✓
- `ReviewItem.confidence` → `ReviewView.confidence` ✓
- `ReviewItem.transaction_count` → `ReviewView.matched_transaction_count`
- `ReviewItem.sample_merchants` → `ReviewView.sample_merchants` ✓
- `ReviewItem.suggested_entry_type` → `ReviewView.suggested_entry_type` ✓
- `ReviewItem.suggested_rate_per_day` → `ReviewView.suggested_rate_per_day` ✓
- Old drift/ended fields (`old_rate_per_day`, `new_rate_per_day`, etc.) → now in `suggested_conditions` (JSON field)

- [ ] **Step 1: Rewrite ReviewPage data fetching**

Open `services/web/src/pages/ReviewPage.tsx`. Replace the entire data-fetching section:

Old (remove):
```ts
const [items, setItems] = useState<ReviewItem[]>([])
const [loading, setLoading] = useState(true)
const [cursor, setCursor] = useState<string | undefined>(undefined)
const [hasMore, setHasMore] = useState(false)
const [loadingMore, setLoadingMore] = useState(false)

const loadItems = useCallback(async (after?: string, reset = false) => { ... }, [])
useEffect(() => { void loadItems(undefined, true) }, [loadItems])
```

New:
```ts
import { useListReviewInfinite, useApproveReview, useRejectReview } from '../api/generated/review'
import type { ReviewView } from '../api/generated/model'

// Inside component:
const { data, fetchNextPage, hasNextPage, isFetching } = useListReviewInfinite({ limit: 50 })
const items = data?.pages.flatMap((p) => p.data ?? []) ?? []
const loading = !data && isFetching
```

Replace the `handleAction` function (which called `loadItems(undefined, true)` to reload) with React Query's `queryClient.invalidateQueries()`:
```ts
import { useQueryClient } from '@tanstack/react-query'
const queryClient = useQueryClient()
function handleAction() {
  void queryClient.invalidateQueries({ queryKey: ['review'] })
}
```

Replace the "load more" button with:
```tsx
{hasNextPage && (
  <button onClick={() => void fetchNextPage()} disabled={isFetching}>
    {isFetching ? 'Loading...' : 'Load older'}
  </button>
)}
```

- [ ] **Step 2: Fix field name references in card components**

In `NewCard.tsx`, `DriftCard.tsx`, `EndedCard.tsx` — replace `ReviewItem` type with `ReviewView` and update any field names that differ:
- `item.transaction_count` → `item.matched_transaction_count`
- Drift-specific fields like `old_rate_per_day`, `new_rate_per_day` may now live in `suggested_conditions` — read these from the conditions object or remove if the API no longer provides them separately.

Run `npx tsc --noEmit` to find exactly which fields are now missing; update them one by one.

- [ ] **Step 3: Update approve/reject calls**

The existing `approveReview`/`rejectReview` functions in resources.ts are gone. Replace with generated mutation hooks in each card component:
```ts
import { useApproveReview, useRejectReview, useUpdateReview } from '../../api/generated/review'

// Inside card component:
const approveMutation = useApproveReview()
function handleApprove() {
  approveMutation.mutate({ id: item.id, body: {} }, { onSuccess: onAction })
}
```

- [ ] **Step 4: TypeScript check**

```bash
cd services/web && npx tsc --noEmit
```

- [ ] **Step 5: Commit**

```bash
git add services/web/src/pages/ReviewPage.tsx \
        services/web/src/components/review/
git commit -m "feat: rebuild ReviewPage with useListReviewInfinite, fix ReviewView field names"
```

---

## Task 11: Rebuild ActivityPage + update JobsContext

**Files:**
- Rebuild: `services/web/src/pages/ActivityPage.tsx`
- Rebuild: `services/web/src/contexts/JobsContext.tsx`

**Field name changes from `Job` → `JobView`:**
- `Job.stages` → not in `JobView` (remove stage progress rendering or use `metadata`)
- `Job.current_stage`, `total_stages`, `current_stage_name` → not in `JobView`
- `Job.started_at` → `JobView.started_at` (new — use instead of computed from stages)
- `Job.retriable` → not in `JobView` (remove retry button for now)

- [ ] **Step 1: Rewrite ActivityPage data fetching**

Open `services/web/src/pages/ActivityPage.tsx`. Replace the data-fetch section:

Old (remove):
```ts
const { jobs, setJobs } = useJobs()
const [loading, setLoading] = useState(true)
const [hasMore, setHasMore] = useState(false)
const [cursor, setCursor] = useState<string | undefined>(undefined)
const loadJobs = useCallback(async (after?: string, reset = false) => { ... }, [...])
useEffect(() => { void loadJobs(undefined, true) }, [])
```

New:
```ts
import { useListJobsInfinite } from '../api/generated/jobs'

// Inside component:
const { data, fetchNextPage, hasNextPage, isFetching } = useListJobsInfinite({ limit: 50 })
const jobs = data?.pages.flatMap((p) => p.data ?? []) ?? []
const loading = !data && isFetching
```

Replace load-more button:
```tsx
{hasNextPage && (
  <button onClick={() => void fetchNextPage()} disabled={isFetching}>
    {isFetching ? 'Loading...' : 'Load older'}
  </button>
)}
```

- [ ] **Step 2: Update JobsContext to use `JobView`**

Open `services/web/src/contexts/JobsContext.tsx`. Replace:
```ts
import type { Job, SseJobEvent } from '../api/resources'
```
With:
```ts
import type { JobView } from '../api/generated/model'

// SSE event shape (kept separate — not from the API spec):
interface SseJobEvent {
  job_id: string
  job_type: string
  status: string
  error: string | null
  queued_at: string
  completed_at: string | null
}
```

Replace all `Job` type references with `JobView`. In `upsertJobFromEvent`, the minimal job object must use `JobView` fields only (no `stages`, `current_stage`, `retriable` — remove those fields):
```ts
const newJob: JobView = {
  id: event.job_id,
  job_type: event.job_type as JobView['job_type'],
  status: event.status as JobView['status'],
  error: event.error,
  queued_at: event.queued_at,
  started_at: null,
  completed_at: event.completed_at,
  triggered_by: '',
  metadata: {},
}
```

- [ ] **Step 3: Update JobCard to use `JobView`**

Open `services/web/src/components/activity/JobCard.tsx`. Replace `Job` type import with `JobView`. Remove any rendering of `stages`, `current_stage`, `total_stages`, `current_stage_name` — use `started_at` and `completed_at` for timing instead. Remove the retry button if it depended on `retriable`.

- [ ] **Step 4: Update `useJobStream.ts`**

Open `services/web/src/hooks/useJobStream.ts`. The `SseJobEvent` type is now local to `JobsContext`. Import it from there, or keep a local copy in the hook. No generated type for SSE events — they come over a websocket/SSE channel, not a REST endpoint.

- [ ] **Step 5: TypeScript check**

```bash
cd services/web && npx tsc --noEmit
```
Expected: zero errors.

- [ ] **Step 6: Final build check**

```bash
cd services/web && npm run build
```
Expected: clean build with no type errors.

- [ ] **Step 7: Commit**

```bash
git add services/web/src/pages/ActivityPage.tsx \
        services/web/src/contexts/JobsContext.tsx \
        services/web/src/components/activity/ \
        services/web/src/hooks/useJobStream.ts
git commit -m "feat: rebuild ActivityPage with useListJobsInfinite, update JobsContext to JobView"
```

---

## Spec coverage check

| Spec section | Plan task |
|---|---|
| §1 Ownership boundaries | Tasks 3–5 (auth package, generated dir) |
| §2 File structure (new/deleted/rebuilt) | File Map + Tasks 5, 6, 7, 8, 9, 10, 11 |
| §3 HTTP setup and auth package | Tasks 3, 4 |
| §4 orval configuration | Task 2 |
| §5 Pagination pattern | Tasks 6–11 (all use `data?.pages.flatMap(p => p.data)`) |
| §6.1 Transactions Table | Task 6 |
| §6.2 Entries Table | Task 7 |
| §6.3 Imports Table | Task 8 |
| §6.4 Stack Panel | Task 9 |
| §6.5 Review Page | Task 10 |
| §6.6 Activity Page | Task 11 |
| §7 Dependencies | Task 2 |
| §8 Generation chain | Tasks 1, 2 (Justfile gen-web) |
| §9 Out of scope (HorizonGraph, Settings, Glossary) | Not in plan ✓ |

**Additional pre-requisite not in spec:** Task 1 adds `account_id`/`entry_id` filter params to the Go API. The spec assumed these exist; they were missing from the generated schema. This is required for the generated hooks to carry those params.
