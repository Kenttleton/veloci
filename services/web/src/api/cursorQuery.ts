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
  ListInstitutionsParams,
  ListJobsParams,
  ListLabelEntriesParams,
  ListLabelsParams,
  ListProjectionsParams,
  ListReviewParams,
  ListSnapshotsParams,
  ListTransactionsParams,
} from './generated/velociAPI.schemas'
import {
  useGetSnapshotHistoryInfinite as _useGetSnapshotHistoryInfinite,
  useListClassificationsInfinite as _useListClassificationsInfinite,
  useListEntriesInfinite as _useListEntriesInfinite,
  useListImportsInfinite as _useListImportsInfinite,
  useListInstitutionAccountsInfinite as _useListInstitutionAccountsInfinite,
  useListInstitutionsInfinite as _useListInstitutionsInfinite,
  useListJobsInfinite as _useListJobsInfinite,
  useListLabelEntriesInfinite as _useListLabelEntriesInfinite,
  useListLabelsInfinite as _useListLabelsInfinite,
  useListProjectionsInfinite as _useListProjectionsInfinite,
  useListReviewInfinite as _useListReviewInfinite,
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

export const useListTransactionsInfinite = (
  params?: ListTransactionsParams,
  options?: Parameters<typeof _useListTransactionsInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListTransactionsInfinite> =>
  _useListTransactionsInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListTransactionsInfinite
  >

export const useListEntriesInfinite = (
  params?: ListEntriesParams,
  options?: Parameters<typeof _useListEntriesInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListEntriesInfinite> =>
  _useListEntriesInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListEntriesInfinite
  >

export const useListImportsInfinite = (
  params?: ListImportsParams,
  options?: Parameters<typeof _useListImportsInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListImportsInfinite> =>
  _useListImportsInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListImportsInfinite
  >

export const useListInstitutionsInfinite = (
  params?: ListInstitutionsParams,
  options?: Parameters<typeof _useListInstitutionsInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListInstitutionsInfinite> =>
  _useListInstitutionsInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListInstitutionsInfinite
  >

export const useListJobsInfinite = (
  params?: ListJobsParams,
  options?: Parameters<typeof _useListJobsInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListJobsInfinite> =>
  _useListJobsInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListJobsInfinite
  >

export const useListLabelsInfinite = (
  params?: ListLabelsParams,
  options?: Parameters<typeof _useListLabelsInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListLabelsInfinite> =>
  _useListLabelsInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListLabelsInfinite
  >

export const useListLabelEntriesInfinite = (
  params?: ListLabelEntriesParams,
  options?: Parameters<typeof _useListLabelEntriesInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListLabelEntriesInfinite> =>
  _useListLabelEntriesInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListLabelEntriesInfinite
  >

export const useListProjectionsInfinite = (
  params?: ListProjectionsParams,
  options?: Parameters<typeof _useListProjectionsInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListProjectionsInfinite> =>
  _useListProjectionsInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListProjectionsInfinite
  >

export const useListReviewInfinite = (
  params?: ListReviewParams,
  options?: Parameters<typeof _useListReviewInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListReviewInfinite> =>
  _useListReviewInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListReviewInfinite
  >

export const useListSnapshotsInfinite = (
  params?: ListSnapshotsParams,
  options?: Parameters<typeof _useListSnapshotsInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListSnapshotsInfinite> =>
  _useListSnapshotsInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListSnapshotsInfinite
  >

export const useListClassificationsInfinite = (
  params?: ListClassificationsParams,
  options?: Parameters<typeof _useListClassificationsInfinite>[1],
  queryClient?: QueryClient,
): ReturnType<typeof _useListClassificationsInfinite> =>
  _useListClassificationsInfinite(params, withCursor(options), queryClient) as ReturnType<
    typeof _useListClassificationsInfinite
  >

// ---------------------------------------------------------------------------
// Hooks with signature: (id, params?, options?, queryClient?)
// ---------------------------------------------------------------------------

export const useListInstitutionAccountsInfinite = (
  id: string,
  params?: ListInstitutionAccountsParams,
  options?: Parameters<typeof _useListInstitutionAccountsInfinite>[2],
  queryClient?: QueryClient,
): ReturnType<typeof _useListInstitutionAccountsInfinite> =>
  _useListInstitutionAccountsInfinite(id, params, withCursor(options), queryClient) as ReturnType<
    typeof _useListInstitutionAccountsInfinite
  >

export const useGetSnapshotHistoryInfinite = (
  nodeId: string,
  params?: GetSnapshotHistoryParams,
  options?: Parameters<typeof _useGetSnapshotHistoryInfinite>[2],
  queryClient?: QueryClient,
): ReturnType<typeof _useGetSnapshotHistoryInfinite> =>
  _useGetSnapshotHistoryInfinite(nodeId, params, withCursor(options), queryClient) as ReturnType<
    typeof _useGetSnapshotHistoryInfinite
  >
