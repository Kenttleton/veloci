import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { useAuth } from './auth/AuthProvider'
import { LoginPage } from './auth/LoginPage'
import { RateFormatProvider } from './contexts/RateFormatContext'
import { JobsProvider } from './contexts/JobsContext'
import { AppShell } from './layout/AppShell'
import { BudgetPage } from './pages/BudgetPage'
import { AccountPage } from './pages/AccountPage'
import { LedgerPage } from './pages/LedgerPage'
import { ActivityPage } from './pages/ActivityPage'
import { SettingsPage } from './pages/SettingsPage'
import { GlossaryPage } from './pages/GlossaryPage'

function ReportsPage() {
  return (
    <div style={{ padding: 32, color: 'var(--text2)' }}>
      Reports — coming soon
    </div>
  )
}

function AuthenticatedApp() {
  const { authenticated } = useAuth()

  if (!authenticated) {
    return <LoginPage />
  }

  return (
    <RateFormatProvider>
      <JobsProvider>
        <Routes>
          <Route path="/" element={<AppShell />}>
            <Route index element={<Navigate to="/budget" replace />} />
<Route path="budget" element={<BudgetPage />} />
            <Route path="reports" element={<ReportsPage />} />
            <Route path="ledger" element={<LedgerPage />} />
            <Route path="activity" element={<ActivityPage />} />
            <Route path="accounts/:id" element={<AccountPage />} />
            <Route path="settings" element={<SettingsPage />} />
            <Route path="glossary" element={<GlossaryPage />} />
          </Route>
        </Routes>
      </JobsProvider>
    </RateFormatProvider>
  )
}

export default function App() {
  return (
    <BrowserRouter useTransitions={false}>
      <AuthenticatedApp />
    </BrowserRouter>
  )
}
