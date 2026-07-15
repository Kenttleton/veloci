import React from 'react'

interface State {
  error: Error | null
}

export class ErrorBoundary extends React.Component<
  { children: React.ReactNode },
  State
> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  render() {
    if (this.state.error) {
      return (
        <div
          style={{
            padding: 32,
            display: 'flex',
            flexDirection: 'column',
            gap: 12,
            alignItems: 'flex-start',
          }}
        >
          <p style={{ margin: 0, color: 'var(--text2)', fontSize: 14 }}>
            Something went wrong on this page.
          </p>
          <button
            onClick={() => this.setState({ error: null })}
            style={{
              background: 'var(--surface2)',
              border: '1px solid var(--border)',
              borderRadius: 4,
              padding: '5px 12px',
              cursor: 'pointer',
              color: 'var(--text)',
              fontSize: 13,
            }}
          >
            Try again
          </button>
        </div>
      )
    }
    return this.props.children
  }
}
