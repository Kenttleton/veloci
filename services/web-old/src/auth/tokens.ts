import { clearAllStores } from '../store/registry'

const KEY = 'veloci_token'

export const getToken = (): string | null => localStorage.getItem(KEY)
export const setToken = (t: string): void => { localStorage.setItem(KEY, t) }
export const clearToken = (): void => { localStorage.removeItem(KEY) }

export const clearAppData = async (): Promise<void> => {
  clearToken()
  await clearAllStores()
  for (const key of Object.keys(localStorage)) {
    if (key.startsWith('veloci_')) {
      localStorage.removeItem(key)
    }
  }
}

// Returns Unix epoch seconds from the JWT exp claim, or null if unreadable.
export const getTokenExpiry = (): number | null => {
  const token = getToken()
  if (!token) return null
  try {
    const payload = JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')))
    return typeof payload.exp === 'number' ? payload.exp : null
  } catch {
    return null
  }
}

export const isTokenExpired = (): boolean => {
  const exp = getTokenExpiry()
  return exp === null || Date.now() / 1000 >= exp
}
