import { BrowserRouter, Routes, Route, Navigate, useLocation } from 'react-router-dom'
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
  return <div style={{ padding: 32, color: 'var(--text2)' }}>Reports — coming soon</div>
}

function RequireAuth({ children }: { children: React.ReactNode }) {
  const { authenticated } = useAuth()
  const location = useLocation()
  if (!authenticated) {
    return <Navigate to="/login" state={{ from: location }} replace />
  }
  return <>{children}</>
}

export default function App() {
  return (
    <BrowserRouter useTransitions={false}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route
          path="/*"
          element={
            <RequireAuth>
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
            </RequireAuth>
          }
        />
      </Routes>
    </BrowserRouter>
  )
}
