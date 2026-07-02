import axios from 'axios'

const BASE = import.meta.env.VITE_API_URL ?? '/api'

const api = axios.create({ baseURL: BASE })

api.interceptors.request.use((config) => {
  const token = localStorage.getItem('token')
  if (token) config.headers.Authorization = `Bearer ${token}`
  return config
})

api.interceptors.response.use(
  (res) => res,
  (err: unknown) => {
    if (
      axios.isAxiosError(err) &&
      err.response?.status === 401
    ) {
      localStorage.removeItem('token')
      window.location.href = '/login'
    }
    return Promise.reject(err)
  },
)

export async function login(email: string, password: string): Promise<void> {
  const { data } = await api.post<{ token: string }>('/auth/login', { email, password })
  localStorage.setItem('token', data.token)
}

export function logout(): void {
  localStorage.removeItem('token')
}

export function isAuthenticated(): boolean {
  return !!localStorage.getItem('token')
}

export async function apiFetch<T>(path: string, config?: Parameters<typeof api.get>[1]): Promise<T> {
  const { data } = await api.get<T>(path, config)
  return data
}
