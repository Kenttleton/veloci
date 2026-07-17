import logger from '../lib/logger'

const log = logger.child({ domain: 'store:registry' })

type ClearFn = () => void | Promise<void>

const stores = new Map<string, ClearFn>()

export function registerStore(domain: string, clear: ClearFn): void {
  stores.set(domain, clear)
}

export async function clearAllStores(): Promise<void> {
  const entries = Array.from(stores.entries())
  const results = await Promise.allSettled(entries.map(([, clear]) => Promise.resolve(clear())))
  results.forEach((result, i) => {
    if (result.status === 'rejected') {
      log.error({ store: entries[i][0], err: result.reason }, 'store clear failed')
    }
  })
}
