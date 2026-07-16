import { BrowserRouter, Routes, Route, Navigate, useParams } from 'react-router-dom'
import { useAuth } from './auth/AuthProvider'
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

function AccountPageWrapper() {
  const { id } = useParams<{ id: string }>()
  return <AccountPage key={id} />
}

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
            <Route path="review" element={<ReviewPage />} />
            <Route path="activity" element={<ActivityPage />} />
            <Route path="accounts/:id" element={<AccountPageWrapper />} />
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
