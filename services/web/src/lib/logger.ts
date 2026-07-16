import pino from 'pino'

const logger = pino({
  level: import.meta.env.DEV ? 'debug' : 'info',
  browser: {
    asObject: true,
    transmit: {
      level: 'warn',
      send: (_level: pino.Level, _logEvent: pino.LogEvent): void => {
        // configure aggregator transport here when ready
      },
    },
  },
})

export default logger
