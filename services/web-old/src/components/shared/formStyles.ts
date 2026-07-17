import type { CSSProperties } from 'react'

export const inputStyle: CSSProperties = {
  background: 'var(--surface2)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  padding: '6px 8px',
  color: 'var(--text)',
  fontSize: 13,
  outline: 'none',
  width: '100%',
}

export const errorInputStyle: CSSProperties = {
  ...inputStyle,
  border: '1px solid var(--commit)',
}

export const labelStyle: CSSProperties = {
  display: 'block',
  fontSize: 12,
  color: 'var(--text3)',
  marginBottom: 4,
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
}

export const fieldWrapStyle: CSSProperties = { marginBottom: 14 }
