import React, { createContext, useContext, useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import axios from 'axios'
import { useLogin as useLoginMutation, useLogout as useLogoutMutation } from '../api/generated/velociAPI'
import { registerAuthInterceptors } from './interceptors'
import { getToken, setToken, clearToken } from './tokens'

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

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [authenticated, setAuthenticated] = useState(() => !!getToken())

  useEffect(() => {
    registerAuthInterceptors(axios)
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
  const logoutMutation = useLogoutMutation()

  function login(email: string, password: string): Promise<void> {
    return loginMutation.mutateAsync({ data: { email, password } }).then((response) => {
      setToken(response.data.token)
      setAuthenticated(true)
    })
  }

  function logout(): void {
    logoutMutation.mutate()
    clearToken()
    setAuthenticated(false)
    navigate('/login')
  }

  return { authenticated, login, logout }
}
