import { Outlet, useLocation } from 'react-router-dom'
import { Sidebar } from './Sidebar'
import { useJobStream } from '../hooks/useJobStream'
import { ErrorBoundary } from '../components/shared/ErrorBoundary'

export function AppShell() {
  // Establish SSE connection for the lifetime of the authenticated session
  useJobStream()
  const location = useLocation()

  return (
    <div
      style={{
        display: 'flex',
        height: '100vh',
        overflow: 'hidden',
        background: 'var(--bg)',
      }}
    >
      <Sidebar />
      <main
        style={{
          flex: 1,
          overflow: 'auto',
          display: 'flex',
          flexDirection: 'column',
          minWidth: 0,
        }}
      >
        <ErrorBoundary resetKey={location.key}>
          <Outlet />
        </ErrorBoundary>
      </main>
    </div>
  )
}
