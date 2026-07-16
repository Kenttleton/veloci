import React, { createContext, useContext, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import axios from 'axios'
import { useLogin as useLoginMutation } from '../api/generated/velociAPI'
import { registerAuthInterceptors } from './interceptors'
import { getToken, setToken, clearToken, clearAppData, isTokenExpired } from './tokens'

interface AuthContextValue {
  authenticated: boolean
  setAuthenticated: (v: boolean) => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      retry: 1,
    },
  },
})

// Must run before QueryClientProvider mounts so the first queries use the right baseURL.
registerAuthInterceptors(axios)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [authenticated, setAuthenticated] = useState(() => {
    if (!getToken() || isTokenExpired()) {
      clearToken()
      return false
    }
    return true
  })

  useEffect(() => {
    if (!authenticated) return

    const expire = () => { void clearAppData(); setAuthenticated(false) }

    if (isTokenExpired()) { expire(); return }

    const id = setInterval(() => { if (isTokenExpired()) expire() }, 30_000)
    return () => clearInterval(id)
  }, [authenticated])

  // 401 safety net — catches clock skew or server-side token revocation
  useEffect(() => {
    const handle = () => { void clearAppData(); setAuthenticated(false) }
    window.addEventListener('auth:expired', handle)
    return () => window.removeEventListener('auth:expired', handle)
  }, [])

  return (
    <QueryClientProvider client={queryClient}>
      <AuthContext.Provider value={{ authenticated, setAuthenticated }}>
        {children}
      </AuthContext.Provider>
    </QueryClientProvider>
  )
}

export function useAuthContext(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuthContext must be used within AuthProvider')
  return ctx
}

export function useAuth() {
  const { authenticated, setAuthenticated } = useAuthContext()
  const navigate = useNavigate()
  const loginMutation = useLoginMutation()

  function login(email: string, password: string): Promise<void> {
    return loginMutation.mutateAsync({ data: { email, password } }).then((response) => {
      setToken(response.data.token)
      setAuthenticated(true)
    })
  }

  function logout(): void {
    void clearAppData()
    setAuthenticated(false)
    navigate('/login')
  }

  return { authenticated, login, logout }
}
