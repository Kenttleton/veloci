import { AuthProvider, useAuth } from './auth/AuthProvider'
import { LoginPage } from './auth/LoginPage'

function Inner() {
  const { authenticated, logout } = useAuth()
  if (!authenticated) return <LoginPage />
  return (
    <main>
      <p>Authenticated — financial views coming soon.</p>
      <button onClick={logout}>Sign out</button>
    </main>
  )
}

export default function App() {
  return (
    <AuthProvider>
      <Inner />
    </AuthProvider>
  )
}
