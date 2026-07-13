import React, { useState, useEffect } from 'react'
import { useParams } from 'react-router-dom'
import { TransactionsTab } from '../components/account/TransactionsTab'
import { ImportsTab } from '../components/account/ImportsTab'
import { getAccount } from '../api/resources'
import type { Account } from '../api/resources'

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

type TabId = 'transactions' | 'imports'

export function AccountPage() {
  const { id } = useParams<{ id: string }>()
  const [account, setAccount] = useState<Account | null>(null)
  const [loading, setLoading] = useState(true)
  const [activeTab, setActiveTab] = useState<TabId>('transactions')

  useEffect(() => {
    if (!id) return
    setLoading(true)
    getAccount(id)
      .then(setAccount)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [id])

  if (!id) return null

  const tabStyle = (isActive: boolean): React.CSSProperties => ({
    padding: '10px 16px',
    cursor: 'pointer',
    fontSize: 13,
    fontWeight: isActive ? 600 : 400,
    color: isActive ? 'var(--text)' : 'var(--text2)',
    background: 'none',
    border: 'none',
    borderBottom: isActive ? '2px solid var(--accent)' : '2px solid transparent',
    transition: 'all 0.1s',
  })

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

      {/* Tab strip */}
      <div
        style={{
          display: 'flex',
          gap: 0,
          padding: '0 4px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        <button
          style={tabStyle(activeTab === 'transactions')}
          onClick={() => setActiveTab('transactions')}
        >
          Transactions
        </button>
        <button
          style={tabStyle(activeTab === 'imports')}
          onClick={() => setActiveTab('imports')}
        >
          Imports
        </button>
      </div>

      {/* Tab content */}
      <div style={{ flex: 1, overflow: 'auto' }}>
        {activeTab === 'transactions' && <TransactionsTab accountId={id} />}
        {activeTab === 'imports' && <ImportsTab accountId={id} />}
      </div>
    </div>
  )
}
