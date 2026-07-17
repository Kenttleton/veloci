/**
 * Cursor-aware infinite query wrappers.
 *
 * orval v7 has a bug where `getNextPageParam` is always stripped from generated
 * `useXxxInfinite` hooks even when supplied via the `options` config key
 * (the omit predicate in @orval/query is logically inverted). This module
 * re-exports every paginated list hook with `getNextPageParam` baked in so
 * that `hasNextPage` works correctly at runtime.
 *
 * Usage: import from here instead of from './generated/velociAPI'.
 *
 * When orval fixes the bug (or a future version adds first-class support),
 * this file can be deleted and import paths updated back to velociAPI.
 */

import type { AxiosResponse } from 'axios'
import type { QueryClient } from '@tanstack/react-query'

import type {
  GetSnapshotHistoryParams,
  ListClassificationsParams,
  ListEntriesParams,
  ListImportsParams,
  ListInstitutionAccountsParams,
  ListJobsParams,
  ListLabelEntriesParams,
  ListLabelsParams,
  ListProjectionsParams,
  ListSnapshotsParams,
  ListTransactionsParams,
} from './generated/velociAPI.schemas'
import {
  useGetSnapshotHistoryInfinite as _useGetSnapshotHistoryInfinite,
  useListClassificationsInfinite as _useListClassificationsInfinite,
  useListEntriesInfinite as _useListEntriesInfinite,
  useListImportsInfinite as _useListImportsInfinite,
  useListInstitutionAccountsInfinite as _useListInstitutionAccountsInfinite,
  useListJobsInfinite as _useListJobsInfinite,
  useListLabelEntriesInfinite as _useListLabelEntriesInfinite,
  useListLabelsInfinite as _useListLabelsInfinite,
  useListProjectionsInfinite as _useListProjectionsInfinite,
  useListSnapshotsInfinite as _useListSnapshotsInfinite,
  useListTransactionsInfinite as _useListTransactionsInfinite,
} from './generated/velociAPI'

/** Extracts `next_cursor` from an AxiosResponse envelope's meta field. */
function getNextPageParam(lastPage: AxiosResponse<{ meta?: { next_cursor?: string } }>) {
  return lastPage?.data?.meta?.next_cursor ?? undefined
}

const initialPageParam = undefined as string | undefined

// ---------------------------------------------------------------------------
// Helpers to merge getNextPageParam into the options argument
// ---------------------------------------------------------------------------

type QueryOptions = { query?: Record<string, unknown>; axios?: unknown }

function withCursor<T extends QueryOptions | undefined>(options: T): T {
  return {
    ...options,
    query: {
      getNextPageParam,
      initialPageParam,
      ...(options as QueryOptions | undefined)?.query,
    },
  } as T
}

// ---------------------------------------------------------------------------
// Hooks with signature: (params?, options?, queryClient?)
// ---------------------------------------------------------------------------

/* eslint-disable @typescript-eslint/no-explicit-any */

export const useListTransactionsInfinite = (
  params?: ListTransactionsParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListTransactionsInfinite(params, withCursor(options) as any, queryClient)

export const useListEntriesInfinite = (
  params?: ListEntriesParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListEntriesInfinite(params, withCursor(options) as any, queryClient)

export const useListImportsInfinite = (
  params?: ListImportsParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListImportsInfinite(params, withCursor(options) as any, queryClient)

export const useListJobsInfinite = (
  params?: ListJobsParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListJobsInfinite(params, withCursor(options) as any, queryClient)

export const useListLabelsInfinite = (
  params?: ListLabelsParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListLabelsInfinite(params, withCursor(options) as any, queryClient)

export const useListProjectionsInfinite = (
  params?: ListProjectionsParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListProjectionsInfinite(params, withCursor(options) as any, queryClient)

export const useListSnapshotsInfinite = (
  params?: ListSnapshotsParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListSnapshotsInfinite(params, withCursor(options) as any, queryClient)

export const useListClassificationsInfinite = (
  params?: ListClassificationsParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListClassificationsInfinite(params, withCursor(options) as any, queryClient)

// ---------------------------------------------------------------------------
// Hooks with signature: (id, params?, options?, queryClient?)
// ---------------------------------------------------------------------------

export const useListLabelEntriesInfinite = (
  id: string,
  params?: ListLabelEntriesParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListLabelEntriesInfinite(id, params, withCursor(options) as any, queryClient)

export const useListInstitutionAccountsInfinite = (
  id: string,
  params?: ListInstitutionAccountsParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useListInstitutionAccountsInfinite(id, params, withCursor(options) as any, queryClient)

export const useGetSnapshotHistoryInfinite = (
  nodeId: string,
  params?: GetSnapshotHistoryParams,
  options?: QueryOptions,
  queryClient?: QueryClient,
) => _useGetSnapshotHistoryInfinite(nodeId, params, withCursor(options) as any, queryClient)

/* eslint-enable @typescript-eslint/no-explicit-any */
