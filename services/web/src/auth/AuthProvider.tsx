import React, { createContext, useContext, useState, useCallback } from 'react'
import { login as apiLogin, logout as apiLogout, isAuthenticated } from '../api/client'

interface AuthContextValue {
  authenticated: boolean
  login: (email: string, password: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [authenticated, setAuthenticated] = useState(isAuthenticated)

  const login = useCallback(async (email: string, password: string) => {
    await apiLogin(email, password)
    setAuthenticated(true)
  }, [])

  const logout = useCallback(() => {
    apiLogout()
    setAuthenticated(false)
  }, [])

  return (
    <AuthContext.Provider value={{ authenticated, login, logout }}>
      {children}
    </AuthContext.Provider>
  )
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
