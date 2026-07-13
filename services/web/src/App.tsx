import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { AuthProvider, useAuth } from './auth/AuthProvider'
import { LoginPage } from './auth/LoginPage'
import { RateFormatProvider } from './contexts/RateFormatContext'
import { JobsProvider } from './contexts/JobsContext'
import { AppShell } from './layout/AppShell'
import { BudgetPage } from './pages/BudgetPage'
import { AccountPage } from './pages/AccountPage'
import { ReviewPage } from './pages/ReviewPage'
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
        <BrowserRouter>
          <Routes>
            <Route path="/" element={<AppShell />}>
              <Route index element={<Navigate to="/budget" replace />} />
              <Route path="budget" element={<BudgetPage />} />
              <Route path="reports" element={<ReportsPage />} />
              <Route path="review" element={<ReviewPage />} />
              <Route path="activity" element={<ActivityPage />} />
              <Route path="accounts/:id" element={<AccountPage />} />
              <Route path="settings" element={<SettingsPage />} />
              <Route path="glossary" element={<GlossaryPage />} />
            </Route>
          </Routes>
        </BrowserRouter>
      </JobsProvider>
    </RateFormatProvider>
  )
}

export default function App() {
  return (
    <AuthProvider>
      <AuthenticatedApp />
    </AuthProvider>
  )
}
