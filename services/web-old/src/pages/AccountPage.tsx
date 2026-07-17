import { useState } from 'react'
import { useParams } from 'react-router-dom'
import { Transactions } from '../components/account/Transactions'
import { UploadImportModal } from '../components/account/UploadImportModal'
import { DeleteAccountModal } from '../components/account/DeleteAccountModal'
import { useGetAccount } from '../api/generated/velociAPI'
import type { AccountView } from '../api/generated/velociAPI.schemas'

type Account = AccountView

const ACCOUNT_TYPE_LABELS: Record<string, string> = {
  checking: 'Checking',
  savings: 'Savings',
  credit: 'Credit',
  loan: 'Loan',
  mortgage: 'Mortgage',
  investment: 'Investment',
}

function formatBalance(cents: number | null): string {
  if (cents === null) return '—'
  return (cents / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })
}

function isNegativeLiability(account: Account): boolean {
  return (
    (account.account_type === 'credit' ||
      account.account_type === 'loan' ||
      account.account_type === 'mortgage') &&
    account.balance_cents !== null &&
    account.balance_cents < 0
  )
}

export function AccountPage() {
  const { id } = useParams<{ id: string }>()
  const [uploadOpen, setUploadOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)

  const accountQuery = useGetAccount(id ?? '', { query: { enabled: !!id } })
  const account: Account | null = accountQuery.data?.data.data ?? null
  const loading = accountQuery.isLoading
  const fetchError = accountQuery.isError

  if (!id) return null

  if (!loading && fetchError) {
    return (
      <div style={{ padding: 32, color: 'var(--text2)', fontSize: 14 }}>
        Could not load account. Check your connection and try again.
      </div>
    )
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      {/* Topbar */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '14px 20px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        {loading ? (
          <div style={{ height: 24, width: 200, background: 'var(--surface)', borderRadius: 4, opacity: 0.4 }} />
        ) : (
          <>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
              <h1
                style={{
                  margin: 0,
                  fontSize: 18,
                  fontWeight: 700,
                  color: 'var(--text)',
                  letterSpacing: '-0.02em',
                }}
              >
                {account?.name ?? '—'}
              </h1>
              {account && (
                <span
                  style={{
                    fontSize: 11,
                    padding: '2px 8px',
                    borderRadius: 4,
                    background: 'var(--surface2)',
                    color: 'var(--text2)',
                    fontWeight: 500,
                  }}
                >
                  {ACCOUNT_TYPE_LABELS[account.account_type] ?? account.account_type}
                </span>
              )}
            </div>
            {account && (
              <span
                style={{
                  fontSize: 18,
                  fontWeight: 600,
                  color: isNegativeLiability(account) ? 'var(--commit)' : 'var(--text)',
                }}
              >
                {formatBalance(account.balance_cents)}
              </span>
            )}
          </>
        )}
      </div>

      {/* Actions bar */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          padding: '0 20px',
          height: 36,
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        <button
          type="button"
          onClick={() => setUploadOpen(true)}
          style={{
            background: 'none',
            border: '1px solid var(--border)',
            borderRadius: 4,
            cursor: 'pointer',
            color: 'var(--text2)',
            fontSize: 12,
            padding: '3px 10px',
          }}
        >
          Upload CSV
        </button>
        <button
          type="button"
          onClick={() => setDeleteOpen(true)}
          style={{
            background: 'none',
            border: 'none',
            cursor: 'pointer',
            color: 'var(--text3)',
            fontSize: 12,
            padding: '3px 10px',
            borderRadius: 4,
            transition: 'color 0.1s',
          }}
          onMouseEnter={(e) => { (e.currentTarget as HTMLButtonElement).style.color = 'var(--commit)' }}
          onMouseLeave={(e) => { (e.currentTarget as HTMLButtonElement).style.color = 'var(--text3)' }}
        >
          Delete Account
        </button>
      </div>

      <Transactions accountId={id} />

      <UploadImportModal
        accountId={id}
        open={uploadOpen}
        onClose={() => setUploadOpen(false)}
      />
      <DeleteAccountModal
        account={account}
        open={deleteOpen}
        onClose={() => setDeleteOpen(false)}
      />
    </div>
  )
}
