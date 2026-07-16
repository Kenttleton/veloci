import type { AxiosStatic } from 'axios'
import { getToken, clearToken } from './tokens'

export function registerAuthInterceptors(instance: AxiosStatic): void {
  instance.defaults.baseURL = import.meta.env.VITE_API_URL ?? '/api'

  instance.interceptors.request.use((config) => {
    const token = getToken()
    if (token) config.headers.Authorization = `Bearer ${token}`
    return config
  })

  instance.interceptors.response.use(
    (r) => r,
    (err: unknown) => {
      if (
        err instanceof Object &&
        'response' in err &&
        (err as { response?: { status?: number } }).response?.status === 401
      ) {
        clearToken()
        window.dispatchEvent(new Event('auth:expired'))
      }
      return Promise.reject(err)
    },
  )
}
