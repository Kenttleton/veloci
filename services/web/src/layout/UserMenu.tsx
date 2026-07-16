import { useState, useRef, useEffect } from 'react'
import { useAuth } from '../auth/AuthProvider'
import { useGetMe } from '../api/generated/velociAPI'

function initials(name: string, email: string): string {
  const n = name.trim()
  if (n) {
    const parts = n.split(/\s+/)
    return parts.length >= 2
      ? (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
      : n.slice(0, 2).toUpperCase()
  }
  return email.slice(0, 2).toUpperCase()
}

export function UserMenu() {
  const { logout } = useAuth()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const { data } = useGetMe()
  const user = data?.data.data

  useEffect(() => {
    if (!open) return
    function onDown(e: MouseEvent) {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  const name = user?.name ?? ''
  const email = user?.email ?? ''
  const label = initials(name, email)

  return (
    <div ref={ref} style={{ position: 'fixed', top: 12, right: 16, zIndex: 100 }}>
      <button
        onClick={() => setOpen((v) => !v)}
        title={name || email}
        style={{
          width: 32,
          height: 32,
          borderRadius: '50%',
          background: 'var(--accent)',
          border: 'none',
          cursor: 'pointer',
          color: '#fff',
          fontSize: 12,
          fontWeight: 700,
          letterSpacing: '0.02em',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
        }}
      >
        {label}
      </button>

      {open && (
        <div
          style={{
            position: 'absolute',
            top: 40,
            right: 0,
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 8,
            boxShadow: '0 4px 16px rgba(0,0,0,0.12)',
            minWidth: 200,
            padding: '8px 0',
          }}
        >
          <div style={{ padding: '8px 14px 10px', borderBottom: '1px solid var(--border)' }}>
            {name && (
              <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text)', marginBottom: 2 }}>
                {name}
              </div>
            )}
            <div style={{ fontSize: 12, color: 'var(--text3)' }}>{email}</div>
          </div>
          <button
            onClick={() => { setOpen(false); logout() }}
            style={{
              width: '100%',
              textAlign: 'left',
              padding: '8px 14px',
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              fontSize: 13,
              color: 'var(--text2)',
            }}
          >
            Sign out
          </button>
        </div>
      )}
    </div>
  )
}
