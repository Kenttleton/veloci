import React from 'react'
import log from '../../lib/logger'

interface Props {
  children: React.ReactNode
  resetKey?: string
}

interface State {
  error: Error | null
}

export class ErrorBoundary extends React.Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: React.ErrorInfo) {
    log.error({ err: error, componentStack: info.componentStack }, 'render error caught by boundary')
  }

  componentDidUpdate(prevProps: Props) {
    if (this.state.error && prevProps.resetKey !== this.props.resetKey) {
      this.setState({ error: null })
    }
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
            Render error: {this.state.error.message}
          </p>
          <pre style={{ margin: 0, color: 'var(--text3)', fontSize: 11, whiteSpace: 'pre-wrap', maxWidth: 600 }}>
            {this.state.error.stack}
          </pre>
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
