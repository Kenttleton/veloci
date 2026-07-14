# Orval + TanStack Client Overhaul

**Date:** 2026-07-14  
**Status:** Approved for implementation

## Overview

Replace the hand-written axios client and all manual data-fetching with a generated, fully-typed HTTP layer. orval owns the HTTP client entirely, generating React Query hooks from the API OpenAPI spec. TanStack Table and TanStack Virtual handle all tabular surfaces. The auth package owns token storage, axios interceptor registration, and the login/logout side-effect layer.

---

## 1. Ownership Boundaries

| Layer | Owner | Files |
|---|---|---|
| HTTP client + types + hooks | orval (generated) | `src/api/generated/**` |
| Auth interceptors + token storage | hand-written auth package | `src/auth/` |
| Table column defs + virtualizer | hand-written per surface | component files |
| App bootstrap | hand-written | `main.tsx` |

---

## 2. File Structure

### New files
```
services/web/
  orval.config.ts

  src/
    auth/
      tokens.ts           ← getToken / setToken / clearToken over localStorage
      interceptors.ts     ← registerAuthInterceptors(instance: AxiosStatic)
      useAuth.ts          ← wraps generated login/logout mutations with token side-effects
      AuthProvider.tsx    ← updated: calls registerAuthInterceptors, owns full lifecycle

    api/
      generated/          ← never edit; fully owned by orval
        accounts.ts
        transactions.ts
        entries.ts
        auth.ts
        labels.ts
        imports.ts
        review.ts
        jobs.ts
        snapshots.ts
        projections.ts
        ... (one file per API tag)
```

### Deleted
```
src/api/client.ts
src/api/resources.ts
src/hooks/usePaginated.ts
```

### Renamed / rebuilt
```
TransactionsTab.tsx   → rebuilt: flat raw-transaction table (stage 0)
(new)                 → EntriesTable.tsx: entry rows + collapsible transaction detail (stage 3)
```

---

## 3. HTTP Setup and Auth Package

orval uses no custom mutator. It calls the global `axios` instance directly. `AuthProvider` registers interceptors on that same instance exactly once on mount — one instance, no collision.

### `tokens.ts`
```ts
const KEY = 'veloci_token'
export const getToken = () => localStorage.getItem(KEY)
export const setToken = (t: string) => localStorage.setItem(KEY, t)
export const clearToken = () => localStorage.removeItem(KEY)
```

### `interceptors.ts`
Takes `AxiosStatic` explicitly — makes it clear what instance is being configured and avoids global `axios` being imported inside a side-effectful module.

```ts
export function registerAuthInterceptors(instance: AxiosStatic): void {
  instance.defaults.baseURL = import.meta.env.VITE_API_URL ?? '/api'

  instance.interceptors.request.use(config => {
    const token = getToken()
    if (token) config.headers.Authorization = `Bearer ${token}`
    return config
  })

  instance.interceptors.response.use(
    r => r,
    err => {
      if (err.response?.status === 401) clearToken()
      return Promise.reject(err)
    }
  )
}
```

### `AuthProvider.tsx`
Calls `registerAuthInterceptors(axios)` once on mount. Owns token state, redirect logic, and interceptor lifetime. Wraps the app in `QueryClientProvider`.

### `useAuth.ts`
Pre-layer hooks that wrap the generated login/logout mutations with `localStorage` side-effects. The generated hooks handle HTTP; this layer handles storage and navigation.

```ts
export function useLogin() {
  const mutation = useLoginMutation()
  return (email: string, password: string) =>
    mutation.mutateAsync({ body: { email, password } })
      .then(data => setToken(data.token))
}

export function useLogout(jti: string) {
  const mutation = useLogoutMutation()
  return () => mutation.mutate({ body: { jti } }, { onSettled: clearToken })
}
```

### `main.tsx`
Renders `<AuthProvider>` only — no axios setup here.

```tsx
<AuthProvider>
  <App />
</AuthProvider>
```

---

## 4. orval Configuration

```ts
// orval.config.ts
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
          useInfiniteQuery: {
            param: 'after',
            initialPageParam: undefined,
          },
          options: {
            getNextPageParam: '(lastPage) => lastPage?.meta?.next_cursor ?? undefined',
          },
        },
      },
    },
  },
})
```

Endpoints with no `after` param get a regular `useQuery` hook only. Endpoints with `after` get both `useQuery` and `useInfiniteQuery` variants.

---

## 5. Pagination Pattern

`usePaginated` is deleted. Every paginated surface uses the generated infinite hook directly.

```ts
const { data, fetchNextPage, hasNextPage, isFetching } =
  useGetTransactionsInfinite({ account_id, limit: 50 })

const rows = data?.pages.flatMap(p => p.data) ?? []
```

| Old | New |
|---|---|
| `items` | `data?.pages.flatMap(p => p.data) ?? []` |
| `hasMore` | `hasNextPage` |
| `loadMore()` | `fetchNextPage()` |
| `loading` | `isFetching` |
| `reset()` | `queryClient.resetQueries(...)` |

---

## 6. Table Surfaces

All tabular surfaces use the same three-layer composition:

```
useGet*Infinite()     → data pages
useReactTable()       → column model, sort, filter, optional grouping
useVirtualizer()      → renders only visible rows, triggers fetchNextPage near bottom
```

### 6.1 Transactions Table (stage 0)
**File:** `TransactionsTab.tsx` (rebuilt)  
**Data:** `useGetTransactionsInfinite({ account_id })`  
**Structure:** Flat rows — one row per raw transaction  
**Columns:** Date, Payee, Amount, Pending, Confidence  
**Sort:** All columns  
**Filter:** Payee text search, pending toggle, amount range

### 6.2 Entries Table (stage 3)
**File:** `EntriesTable.tsx` (new)  
**Data:** `useGetEntriesInfinite()` + per-entry `useGetTransactionsInfinite({ entry_id })`  
**Structure:** Grouped — entry row expands to show matched transactions  
**TanStack Table:** `getGroupedRowModel()` for the entry → transaction hierarchy  
**Entry columns:** Name, Type, Direction, Rate/day, Label, Status  
**Transaction sub-columns:** Date, Payee, Amount, Confidence  
**Sort:** Entry-level columns  
**Filter:** Direction, entry type, label, status

### 6.3 Imports Table
**File:** `ImportsTab.tsx` (rebuilt)  
**Data:** `useGetImports({ account_id })` (batches), per-batch `useGetTransactions({ import_batch_id })`  
**Structure:** Batch rows expand to nested transaction table  
**Columns (batch):** Date, Source, Imported count, Skipped count  
**Columns (transactions):** Payee, Amount, Date, Duplicate flag  
**Filter:** Batch selector, merchant text search, duplicate toggle  
**Sort:** Transaction columns within each batch

### 6.4 Stack Panel (Budget page)
**File:** `StackPanel.tsx` (rebuilt)  
**Data:** `useGetEntries()` (non-paginated)  
**Structure:** Label group headers → entry rows  
**Columns:** Name, Type, Rate/day, Drift, Label  
**Sort:** Entry-level columns within each label group  
**Filter:** Direction toggle (income / expense)  
**Virtual scroll:** Yes — entry rows only; label headers are sticky

### 6.5 Review Page
**File:** `ReviewPage.tsx` (pagination updated)  
**Data:** `useGetReviewInfinite()`  
**Structure:** Cards — `NewCard` / `DriftCard` / `EndedCard` per alert type  
**No TanStack Table** — card layout varies too much by type  
**Sort/Filter:** Kept in local state (type toggles, confidence sort)  
**Change:** Manual cursor pagination replaced by `useInfiniteQuery`

### 6.6 Activity Page
**File:** `ActivityPage.tsx` (pagination updated)  
**Data:** `useGetJobsInfinite()`  
**Structure:** Job cards  
**No TanStack Table** — card layout, no sort/filter needed  
**Change:** Manual "load older" cursor pagination replaced by `useInfiniteQuery`

---

## 7. Dependencies

```json
dependencies:
  "@tanstack/react-query": "^5"
  "@tanstack/react-table": "^8"
  "@tanstack/react-virtual": "^3"

devDependencies:
  "orval": "^7"
```

---

## 8. Generation Chain

```
just gen
  → gen-auth   (auth OpenAPI spec)
  → gen-api    (ogen authclient + patchclient + api OpenAPI spec)
  → gen-web    (orval: api spec → React Query hooks)
```

Justfile additions:
```just
# Generate web client from api spec
gen-web:
    cd services/web && npx orval

gen: gen-api gen-web
```

---

## 9. What Is Not In Scope

- **Budget page Horizon graph** (`HorizonGraph.tsx`) — not a table, deferred
- **Settings page** — no tables
- **Glossary page** — no tables
- **Snapshot / projection endpoints** — consumed by Budget graph, deferred with it
