import { useNavigate } from 'react-router-dom'
import { useLogin as useLoginMutation, useLogout as useLogoutMutation } from '../api/generated/velociAPI'
import { setToken, clearToken, getToken } from './tokens'
import { useAuthContext } from './AuthProvider'

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
    const token = getToken()
    if (token) {
      logoutMutation.mutate({ data: { jti: token } })
    }
    clearToken()
    setAuthenticated(false)
    navigate('/login')
  }

  return { authenticated, login, logout }
}
