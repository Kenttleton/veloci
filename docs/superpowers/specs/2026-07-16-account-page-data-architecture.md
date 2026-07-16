# Account Page Data Architecture + Controls

**Date:** 2026-07-16  
**Status:** Approved

## Overview

Redesign the AccountPage around a centralized Zustand store that persists all account data and UI state across route changes. TanStack Query becomes the fetch layer only — the UI reads exclusively from the store. SSE job completions drive targeted cache invalidation and refetch. Two controls are added to the page: CSV upload and account deletion.

---

## 1. Zustand Account Store

A single module-level Zustand store, keyed per account ID, persists for the lifetime of the authenticated session.

### Shape

```ts
type FetchStatus = 'idle' | 'loading' | 'error' | 'success'
type TabId = 'entries' | 'transactions'

interface AccountTabState {
  transactions: TransactionView[]
  entries: EntryView[]
  transactionsStatus: FetchStatus
  entriesStatus: FetchStatus
  activeTab: TabId
  scroll: { entries: number; transactions: number }
}

interface AccountStore {
  accounts: Record<string, AccountTabState>
  setActiveTab: (id: string, tab: TabId) => void
  setScroll: (id: string, tab: TabId, offset: number) => void
  setTransactions: (id: string, data: TransactionView[], status: FetchStatus) => void
  setEntries: (id: string, data: EntryView[], status: FetchStatus) => void
  clear: () => void
}
```

### Initialization

When an account is first accessed, its slot is created with empty arrays and `status: 'loading'`. Subsequent visits reuse the existing slot — data and UI state are preserved.

### Clearing

`clear()` wipes all account slots. It is called:

- On token expiry (the 30s interval in `AuthProvider`)
- On explicit logout via `useAuth`

No financial data or navigation state survives the session end.

---

## 2. TanStack Query as Fetch Layer

TanStack Query fires network requests and writes results into the Zustand store on success. UI components do not read from TanStack hooks directly.

### Fetch Hooks

Two hooks per account, called from `AccountPage` as a coordinator:

```text
useGetAccountTransactions(id) → on success: store.setTransactions(id, data, 'success')
useGetAccountEntries(id)      → on success: store.setEntries(id, data, 'success')
```

If the store already has data for this account (from a prior visit), the UI renders immediately from store while TanStack revalidates in the background. No loading spinner on return visits.

### SSE-Driven Invalidation

When `JobsContext` receives a job completion event for an account, it calls:

```ts
queryClient.invalidateQueries(['transactions', accountId])
queryClient.invalidateQueries(['entries', accountId])
```

TanStack refetches and writes updated data into the store. UI reacts automatically.

---

## 3. AccountPage Shell

### Route Structure

`AccountPageWrapper` is retired. `AccountPage` reads `id` from `useParams` and the store directly. `key={id}` is removed — the fiber reuse issue is eliminated because the component never calls TanStack hooks with a changing `id`; it reads from `store.accounts[id]` which is always keyed correctly.

```text
/accounts/:id                  → AccountPage
  index                        → AccountTabRedirect
  /accounts/:id/entries        → EntriesRouteTab
  /accounts/:id/transactions   → TransactionsRouteTab
```

`AccountTabRedirect` reads `store.accounts[id].activeTab`, falls back to `entries`, and issues a `<Navigate replace>` to the appropriate subroute.

`EntriesRouteTab` and `TransactionsRouteTab` are thin wrappers that read `id` from `useParams` and pass it to the existing `EntriesTable` and `TransactionsTab` components as `accountId` — no changes to those components' interfaces.

### Tab Strip

`<NavLink>` replaces the current `<button>` + `useState` pattern. React Router owns active styling. Entries listed first.

On tab click: update `store.activeTab` and navigate to the subroute. Both tab components are always mounted within `AccountPage` and toggled via CSS so the virtualizer never tears down during a tab switch.

### CSS Toggle Implementation Note

`display: none` collapses the container to zero height, which breaks TanStack Virtual's item measurement. The inactive tab uses `opacity: 0; pointer-events: none; position: absolute; inset: 0` instead, keeping the container in layout so the virtualizer retains valid dimensions.

### Tab State Priority on Load

1. URL path (`/entries` or `/transactions`)
2. `store.accounts[id].activeTab`
3. Default: `entries`

URL and store are kept in sync on every tab change.

### Scroll Position

Each tab component writes its virtualizer scroll offset to `store.accounts[id].scroll[tab]` on scroll (debounced). On mount, it reads this value and passes it as `initialOffset` to the virtualizer, restoring position immediately without a post-mount scroll call.

---

## 4. Layout

```text
┌─────────────────────────────────────────┐
│  Account Name  [type]          $1,234   │  ← topbar (unchanged)
├─────────────────────────────────────────┤
│  ↑ Upload CSV            Delete Account │  ← actions bar (~36px)
├─────────────────────────────────────────┤
│  Entries   Transactions                 │  ← tab strip (NavLink)
├─────────────────────────────────────────┤
│                                         │
│  <EntriesTable />    (CSS toggle)       │  ← both always mounted
│  <TransactionsTab /> (CSS toggle)       │
│                                         │
└─────────────────────────────────────────┘
```

Actions bar: `flexShrink: 0`, horizontal padding matches topbar (20px). Upload CSV left-aligned, Delete Account right-aligned in muted danger color.

---

## 5. Upload CSV Control

The existing `UploadImportModal` (3-step: pick file → mapping preview → mapping editor) is reused without changes. The trigger moves from `ImportsTab` to the actions bar.

`ImportsTab` is removed entirely — files are not retained after upload.

---

## 6. Delete Account

A type-to-confirm modal:

- Title: "Delete [Account Name]"
- Body: brief explanation that this cannot be undone
- Input: user must type the exact account name; Delete button remains disabled until it matches
- On confirm: `DELETE /accounts/{id}`, then navigate to `/budget`

---

## 7. Logging

This spec introduces `pino` as the frontend logging library. Only the Zustand store registry and account page data functions are covered here. Converting existing `console.*` calls elsewhere in the app is a follow-up task.

### Library

`pino` with `pino/browser`. Structured JSON output, log levels, and a `transmit.send` hook that is the single extension point for routing logs to an aggregator (Datadog, Axiom, Better Stack, etc.) when the time comes. No other code changes when the transport is configured.

### Logger Utility

`src/lib/logger.ts` creates the root pino instance:

```ts
import pino from 'pino'

const logger = pino({
  level: import.meta.env.DEV ? 'debug' : 'info',
  browser: {
    asObject: true,
    transmit: {
      level: 'warn',
      send: (_level, _logEvent) => {
        // configure aggregator transport here when ready
      },
    },
  },
})

export default logger
```

Domain-scoped child loggers are created at the module level in each file that needs them:

```ts
const log = logger.child({ domain: 'store:accounts' })
log.error({ err }, 'fetch failed')
```

Child loggers inherit level and transport from the root and automatically include the `domain` field in every entry.

### Logging Scope for This Spec

**`src/store/registry.ts`** — store teardown failures:

```ts
const log = logger.child({ domain: 'store:registry' })

// in clearAllStores(), replace console.error:
log.error({ err: result.reason, store: registry[i].domain }, 'store clear failed')
```

**`src/store/accountStore.ts`** — fetch status transitions written to store:

```ts
const log = logger.child({ domain: 'store:accounts' })

// on TanStack error callback writing to store:
log.warn({ accountId, err }, 'transaction fetch failed')
log.warn({ accountId, err }, 'entries fetch failed')
```

**`src/pages/AccountPage.tsx`** — tab navigation and data coordination:

```ts
const log = logger.child({ domain: 'account-page' })

// on tab change:
log.debug({ accountId, tab }, 'tab changed')

// on clearAppData triggering store clear from this context:
log.info({ accountId }, 'account data cleared on session end')
```

---

## 8. Auth Data Clearing

### Store Registry

A central registry in `src/store/registry.ts` manages all Zustand stores. Each store self-registers at module load time — no manual wiring in `clearAppData()` as new stores are added.

```ts
interface StoreRegistration {
  domain: string
  clear: () => void | Promise<void>
}

const registry: StoreRegistration[] = []

export function registerStore(domain: string, clear: () => void | Promise<void>): void {
  registry.push({ domain, clear })
}

export async function clearAllStores(): Promise<void> {
  const log = logger.child({ domain: 'store:registry' })
  const results = await Promise.allSettled(
    registry.map(({ clear }) => Promise.resolve(clear()))
  )
  results.forEach((result, i) => {
    if (result.status === 'rejected') {
      log.error({ err: result.reason, store: registry[i].domain }, 'store clear failed')
    }
  })
}
```

`Promise.allSettled()` ensures every store is cleared regardless of individual failures. Future stores just call `registerStore()` — cleanup and error reporting come for free.

Each store self-registers:

```ts
// store/accountStore.ts
registerStore('accounts', () => useAccountStore.getState().clear())
```

### clearAppData

`clearAppData()` in `tokens.ts` is the single call site for session teardown:

```ts
export const clearAppData = async (): Promise<void> => {
  clearToken()
  await clearAllStores()
  Object.keys(localStorage)
    .filter(k => k.startsWith('veloci_'))
    .forEach(k => localStorage.removeItem(k))
}
```

Called from:

- `AuthProvider` interval expiry handler
- `AuthProvider` `auth:expired` event handler
- `useAuth` logout function

---

## 8. Files Affected

### New

- `src/lib/logger.ts` — pino root instance, domain child logger pattern, aggregator transmit hook
- `src/store/registry.ts` — store registry, `registerStore()`, `clearAllStores()`
- `src/store/accountStore.ts` — Zustand store definition, self-registers on import

### Modified

- `src/App.tsx` — nested routes under `accounts/:id`, remove `AccountPageWrapper`
- `src/pages/AccountPage.tsx` — reads from store, CSS-toggled tab components, actions bar added
- `src/auth/AuthProvider.tsx` — calls `clearAppData()` in expiry/logout paths
- `src/auth/tokens.ts` — adds `clearAppData()`

### New (small)

- `src/pages/account/AccountTabRedirect.tsx` — index route redirect
- `src/pages/account/EntriesRouteTab.tsx` — thin route wrapper
- `src/pages/account/TransactionsRouteTab.tsx` — thin route wrapper
- `src/components/account/DeleteAccountModal.tsx` — type-to-confirm modal

### Removed

- `src/components/account/ImportsTab.tsx`
