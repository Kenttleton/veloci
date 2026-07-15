import React, { useEffect, useState } from 'react'
import { NavLink } from 'react-router-dom'
import {
  BarChart2,
  FileText,
  CheckSquare,
  Activity,
  Settings,
  BookOpen,
  Plus,
} from 'lucide-react'
import { useAuth } from '../auth/AuthProvider'
import { useJobs } from '../contexts/JobsContext'
// TODO(task-6-11): getAccounts will be replaced with useListAccounts from generated API
import type { AccountView } from '../api/generated/velociAPI.schemas'
import { getToken } from '../auth/tokens'

type Account = AccountView

async function getAccounts(): Promise<Account[]> {
  const token = getToken()
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
  const res = await fetch(`${base}/accounts`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
  })
  const json = (await res.json()) as { data: Account[] }
  return json.data ?? []
}

function formatBalance(cents: number | null): string {
  if (cents === null) return '—'
  const dollars = cents / 100
  return dollars.toLocaleString('en-US', { style: 'currency', currency: 'USD' })
}

function isNegativeBalance(account: Account): boolean {
  return (
    account.balance_cents !== null &&
    account.balance_cents < 0 &&
    (account.account_type === 'credit' ||
      account.account_type === 'loan' ||
      account.account_type === 'mortgage')
  )
}

export function Sidebar() {
  const { logout } = useAuth()
  const { hasRunningJobs } = useJobs()
  const [accounts, setAccounts] = useState<Account[]>([])
  const [reviewCount] = useState(0)

  useEffect(() => {
    getAccounts()
      .then(setAccounts)
      .catch(() => {})
  }, [])

  const activeAccounts = accounts.filter((a) => a.status === 'active')
  const passiveAccounts = accounts.filter((a) => a.status === 'passive')

  const navItemStyle = (isActive: boolean): React.CSSProperties => ({
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    padding: '7px 12px',
    borderRadius: 6,
    cursor: 'pointer',
    color: isActive ? 'var(--text)' : 'var(--text2)',
    background: isActive ? 'var(--surface2)' : 'transparent',
    textDecoration: 'none',
    fontSize: '13.5px',
    fontWeight: isActive ? 500 : 400,
    transition: 'background 0.1s',
  })

  return (
    <aside
      style={{
        width: 220,
        flexShrink: 0,
        background: 'var(--surface)',
        borderRight: '1px solid var(--border)',
        display: 'flex',
        flexDirection: 'column',
        height: '100vh',
        overflow: 'hidden',
      }}
    >
      {/* Logo / entity */}
      <div
        style={{
          padding: '16px 16px 12px',
          borderBottom: '1px solid var(--border)',
        }}
      >
        <span style={{ fontWeight: 700, fontSize: 16, color: 'var(--text)', letterSpacing: '-0.02em' }}>
          Veloci
        </span>
      </div>

      {/* Nav */}
      <nav style={{ padding: '8px 8px 0' }}>
        <NavLink
          to="/budget"
          style={({ isActive }) => navItemStyle(isActive)}
        >
          <BarChart2 size={15} />
          Budget
        </NavLink>

        <NavLink
          to="/reports"
          style={({ isActive }) => navItemStyle(isActive)}
        >
          <FileText size={15} />
          Reports
        </NavLink>

        <NavLink
          to="/review"
          style={({ isActive }) => ({
            ...navItemStyle(isActive),
            position: 'relative',
          })}
        >
          <CheckSquare
            size={15}
            style={{
              opacity: hasRunningJobs ? 0.55 : 1,
              outline: hasRunningJobs ? '1px solid var(--accent)' : undefined,
              borderRadius: 2,
            }}
          />
          Review
          {reviewCount > 0 && (
            <span
              style={{
                marginLeft: 'auto',
                background: 'var(--accent)',
                color: '#fff',
                borderRadius: 10,
                fontSize: 10,
                fontWeight: 700,
                padding: '0 6px',
                lineHeight: '16px',
                opacity: hasRunningJobs ? 0.55 : 1,
                outline: hasRunningJobs ? '1px solid var(--accent)' : undefined,
              }}
            >
              {reviewCount}
            </span>
          )}
        </NavLink>

        <NavLink
          to="/activity"
          style={({ isActive }) => navItemStyle(isActive)}
        >
          <Activity size={15} />
          Activity
        </NavLink>
      </nav>

      {/* Accounts section */}
      <div style={{ flex: 1, overflow: 'auto', padding: '12px 8px 0' }}>
        {/* Active accounts */}
        <div style={{ marginBottom: 8 }}>
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'space-between',
              padding: '4px 8px',
              marginBottom: 2,
            }}
          >
            <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.06em' }}>
              Active
            </span>
            <button
              style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: 0 }}
              title="Add account"
            >
              <Plus size={13} />
            </button>
          </div>

          {activeAccounts.map((account) => (
            <NavLink
              key={account.id}
              to={`/accounts/${account.id}`}
              style={({ isActive }) => ({
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '5px 8px',
                borderRadius: 5,
                cursor: 'pointer',
                textDecoration: 'none',
                background: isActive ? 'var(--surface2)' : 'transparent',
              })}
            >
              <span style={{ fontSize: 13, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {account.name}
              </span>
              <span
                style={{
                  fontSize: 12,
                  color: isNegativeBalance(account) ? 'var(--commit)' : 'var(--text2)',
                  flexShrink: 0,
                  marginLeft: 4,
                }}
              >
                {formatBalance(account.balance_cents)}
              </span>
            </NavLink>
          ))}

          {activeAccounts.length === 0 && (
            <p style={{ fontSize: 12, color: 'var(--text3)', padding: '4px 8px', margin: 0 }}>
              No active accounts
            </p>
          )}
        </div>

        {/* Passive accounts */}
        {passiveAccounts.length > 0 && (
          <div style={{ marginBottom: 8 }}>
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '4px 8px',
                marginBottom: 2,
              }}
            >
              <span style={{ fontSize: 11, fontWeight: 600, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.06em' }}>
                Passive
              </span>
              <button
                style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: 0 }}
                title="Add account"
              >
                <Plus size={13} />
              </button>
            </div>

            {passiveAccounts.map((account) => (
              <NavLink
                key={account.id}
                to={`/accounts/${account.id}`}
                style={({ isActive }) => ({
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  padding: '5px 8px',
                  borderRadius: 5,
                  cursor: 'pointer',
                  textDecoration: 'none',
                  background: isActive ? 'var(--surface2)' : 'transparent',
                })}
              >
                <span style={{ fontSize: 13, color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {account.name}
                </span>
                <span
                  style={{
                    fontSize: 12,
                    color: isNegativeBalance(account) ? 'var(--commit)' : 'var(--text2)',
                    flexShrink: 0,
                    marginLeft: 4,
                  }}
                >
                  {formatBalance(account.balance_cents)}
                </span>
              </NavLink>
            ))}
          </div>
        )}
      </div>

      {/* Footer */}
      <div
        style={{
          borderTop: '1px solid var(--border)',
          padding: '8px',
        }}
      >
        <NavLink
          to="/settings"
          style={({ isActive }) => navItemStyle(isActive)}
        >
          <Settings size={15} />
          Settings
        </NavLink>

        <NavLink
          to="/glossary"
          style={({ isActive }) => navItemStyle(isActive)}
        >
          <BookOpen size={15} />
          Glossary
        </NavLink>

        <button
          onClick={logout}
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 8,
            padding: '7px 12px',
            borderRadius: 6,
            cursor: 'pointer',
            color: 'var(--text3)',
            background: 'transparent',
            border: 'none',
            fontSize: '13.5px',
            width: '100%',
            marginTop: 2,
          }}
        >
          Sign out
        </button>
      </div>
    </aside>
  )
}
