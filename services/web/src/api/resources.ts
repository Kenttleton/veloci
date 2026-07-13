import { apiFetch } from './client'

// ─── Response envelope types ───────────────────────────────────────────────

export interface ApiMeta {
  next_cursor?: string
  limit?: number
  has_more?: boolean
}

export interface ApiResponse<T> {
  data: T
  meta: ApiMeta
}

// ─── Domain types ──────────────────────────────────────────────────────────

export interface Account {
  id: string
  entity_id: string
  institution_id: string | null
  name: string
  account_type: 'checking' | 'savings' | 'credit' | 'loan' | 'mortgage' | 'investment'
  status: 'active' | 'passive'
  interest_rate: number | null
  balance_cents: number | null
  credit_limit_cents: number | null
  created_at: string
}

export interface Label {
  id: string
  entity_id: string
  name: string
  created_at: string
  rule_count?: number
}

export interface Rule {
  id: string
  entity_id: string
  name: string
  direction: 'income' | 'expense'
  entry_type: 'standing' | 'variable' | 'one_time'
  period_days: number
  variable_method: 'avg' | 'max' | null
  projected_rate_per_day: number | null
  conditions: unknown
  label_id: string | null
  stage: 'pre' | 'post'
  priority: number
  status: 'pending_review' | 'active' | 'inactive'
  source: 'user' | 'engine'
  created_at: string
}

export interface SnapshotSummary {
  income_rate: number
  commitments_rate: number
  margin_rate: number
  projection_rate: number
  drift_rate: number
  period: string
  actual: boolean
}

export interface SnapshotCandle {
  period_start: string
  period_end: string
  open: number
  close: number
  high: number
  low: number
  actual_rate_per_day: number
  projected_rate_per_day: number
  drift_per_day: number
  slope_per_day: number
  epoch_start: string
  epoch_end: string | null
}

export interface SnapshotHistoryResponse {
  data: SnapshotCandle[]
  next_cursor: string | null
  has_more: boolean
}

export interface Entry {
  id: string
  rule_id: string
  name: string
  direction: 'income' | 'expense'
  entry_type: 'standing' | 'variable' | 'one_time'
  label_id: string | null
  label_name: string | null
  actual_rate: number
  projected_rate: number
  drift_rate: number
  tag: 'hit' | 'boost' | null
  period: string
  status: 'active' | 'inactive'
}

export interface Transaction {
  id: string
  account_id: string
  rule_id: string | null
  rule_name: string | null
  label_id: string | null
  imported_payee: string
  merchant_normalized: string
  amount_cents: number
  date: string
  pending_review: boolean
  confidence: number | null
  import_batch_id: string
  created_at: string
}

export interface ImportBatch {
  id: string
  account_id: string
  processed_at: string
  transactions_imported: number
  transactions_skipped_duplicate: number
  source_name: string
}

export interface ImportTransaction {
  id: string
  account_id: string
  import_batch_id: string
  imported_payee: string
  merchant_normalized: string
  amount_cents: number
  date: string
  is_duplicate: boolean
  created_at: string
}

export interface ReviewItem {
  id: string
  rule_id: string
  rule_name: string
  alert_type: 'new' | 'drift' | 'ended'
  status: 'pending' | 'approved' | 'rejected'
  confidence: number
  merchant_confidence: number
  timing_confidence: number
  amount_confidence: number
  suggested_entry_type: 'standing' | 'variable' | 'one_time'
  suggested_rate_per_day: number
  recurrence_anchor: string | null
  sample_merchants: Array<{ date: string; payee: string; amount_cents: number }>
  transaction_count: number
  // drift-specific
  old_rate_per_day?: number
  new_rate_per_day?: number
  old_timing?: string
  new_timing?: string
  transaction_evidence?: Array<{ date: string; payee: string; amount_cents: number }>
  has_manual_projection?: boolean
  manual_projection_per_day?: number
  // ended-specific
  last_seen_date?: string
  next_due_date?: string
  days_overdue?: number
  current_rate_per_day?: number
  created_at: string
}

export interface Job {
  id: string
  entity_id: string
  job_type: 'import.process' | 'rules.reprocess' | 'account.analyze'
  status: 'queued' | 'processing' | 'complete' | 'failed'
  error: string | null
  retriable: boolean
  queued_at: string
  completed_at: string | null
  triggered_by: string
  account_id: string | null
  account_name: string | null
  current_stage: number | null
  total_stages: number | null
  current_stage_name: string | null
  metadata: {
    transactions_imported?: number
    transactions_skipped_duplicate?: number
    rules_processed?: number
    snapshots_written?: number
  }
  stages: Array<{
    stage_number: number
    name: string
    status: 'pending' | 'running' | 'complete' | 'failed'
    elapsed_ms: number | null
    error: string | null
  }>
}

export interface SseJobEvent {
  job_id: string
  job_type: 'import.process' | 'rules.reprocess' | 'account.analyze'
  status: 'queued' | 'processing' | 'complete' | 'failed'
  error: string | null
  queued_at: string
  completed_at: string | null
}

// ─── API functions ─────────────────────────────────────────────────────────

export async function getAccounts(): Promise<Account[]> {
  const res = await apiFetch<ApiResponse<Account[]>>('/accounts')
  return res.data
}

export async function getAccount(id: string): Promise<Account> {
  const res = await apiFetch<ApiResponse<Account>>(`/accounts/${id}`)
  return res.data
}

export async function getSnapshotSummary(): Promise<SnapshotSummary> {
  const res = await apiFetch<ApiResponse<SnapshotSummary>>('/snapshots/summary')
  return res.data
}

export async function getSnapshotHistory(
  nodeId: string,
  params: { before?: string; limit?: number; granularity?: 'day' | 'month' | 'year' },
): Promise<SnapshotHistoryResponse> {
  const search = new URLSearchParams()
  if (params.before) search.set('before', params.before)
  if (params.limit) search.set('limit', String(params.limit))
  if (params.granularity) search.set('granularity', params.granularity)
  const qs = search.toString()
  return apiFetch<SnapshotHistoryResponse>(
    `/snapshots/${nodeId}/history${qs ? `?${qs}` : ''}`,
  )
}

export async function getEntries(): Promise<Entry[]> {
  const res = await apiFetch<ApiResponse<Entry[]>>('/entries')
  return res.data
}

export async function getTransactions(params: {
  account_id?: string
  rule_id?: string
  after?: string
  limit?: number
}): Promise<{ data: Transaction[]; meta: ApiMeta }> {
  const search = new URLSearchParams()
  if (params.account_id) search.set('account_id', params.account_id)
  if (params.rule_id) search.set('rule_id', params.rule_id)
  if (params.after) search.set('after', params.after)
  if (params.limit) search.set('limit', String(params.limit))
  const qs = search.toString()
  return apiFetch<{ data: Transaction[]; meta: ApiMeta }>(
    `/transactions${qs ? `?${qs}` : ''}`,
  )
}

export async function getImports(params: {
  account_id?: string
  after?: string
  limit?: number
}): Promise<{ data: ImportBatch[]; meta: ApiMeta }> {
  const search = new URLSearchParams()
  if (params.account_id) search.set('account_id', params.account_id)
  if (params.after) search.set('after', params.after)
  if (params.limit) search.set('limit', String(params.limit))
  const qs = search.toString()
  return apiFetch<{ data: ImportBatch[]; meta: ApiMeta }>(
    `/imports${qs ? `?${qs}` : ''}`,
  )
}

export async function getImportTransactions(params: {
  account_id?: string
  import_batch_id?: string
  after?: string
  limit?: number
}): Promise<{ data: ImportTransaction[]; meta: ApiMeta }> {
  const search = new URLSearchParams()
  if (params.account_id) search.set('account_id', params.account_id)
  if (params.import_batch_id) search.set('import_batch_id', params.import_batch_id)
  if (params.after) search.set('after', params.after)
  if (params.limit) search.set('limit', String(params.limit))
  const qs = search.toString()
  return apiFetch<{ data: ImportTransaction[]; meta: ApiMeta }>(
    `/transactions${qs ? `?${qs}` : ''}`,
  )
}

export async function getReview(params: {
  after?: string
  limit?: number
}): Promise<{ data: ReviewItem[]; meta: ApiMeta }> {
  const search = new URLSearchParams()
  if (params.after) search.set('after', params.after)
  if (params.limit) search.set('limit', String(params.limit))
  const qs = search.toString()
  return apiFetch<{ data: ReviewItem[]; meta: ApiMeta }>(
    `/review${qs ? `?${qs}` : ''}`,
  )
}

export async function approveReview(
  id: string,
  payload?: { correction?: boolean; version?: boolean; epoch_end?: string },
): Promise<void> {
  await apiFetch<unknown>(`/review/${id}/approve`, { method: 'POST', data: payload ?? {} })
}

export async function rejectReview(id: string): Promise<void> {
  await apiFetch<unknown>(`/review/${id}/reject`, { method: 'POST' })
}

export async function updateReview(
  id: string,
  payload: Partial<{
    name: string
    entry_type: string
    label_id: string
    period_days: number
    projected_rate_per_day: number
    correction: boolean
    version: boolean
    epoch_end: string
  }>,
): Promise<void> {
  await apiFetch<unknown>(`/review/${id}`, { method: 'PUT', data: payload })
}

export async function getLabels(): Promise<Label[]> {
  const res = await apiFetch<ApiResponse<Label[]>>('/labels')
  return res.data
}

export async function createLabel(name: string): Promise<Label> {
  const res = await apiFetch<ApiResponse<Label>>('/labels', {
    method: 'POST',
    data: { name },
  })
  return res.data
}

export async function updateLabel(id: string, name: string): Promise<Label> {
  const res = await apiFetch<ApiResponse<Label>>(`/labels/${id}`, {
    method: 'PUT',
    data: { name },
  })
  return res.data
}

export async function getLabelRuleCount(id: string): Promise<number> {
  const res = await apiFetch<ApiResponse<Rule[]>>(`/labels/${id}/rules`)
  return res.data.length
}

export async function getJobs(params: {
  after?: string
  limit?: number
}): Promise<{ data: Job[]; meta: ApiMeta }> {
  const search = new URLSearchParams()
  if (params.after) search.set('after', params.after)
  if (params.limit) search.set('limit', String(params.limit))
  const qs = search.toString()
  return apiFetch<{ data: Job[]; meta: ApiMeta }>(`/jobs${qs ? `?${qs}` : ''}`)
}

export async function retryJob(jobId: string): Promise<void> {
  await apiFetch<unknown>(`/jobs/${jobId}/retry`, { method: 'POST' })
}
