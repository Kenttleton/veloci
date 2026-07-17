import { defineConfig } from 'orval'

export default defineConfig({
  veloci: {
    input: { target: '../api/api/openapi.json' },
    output: {
      mode: 'split',
      target: 'src/api/generated',
      client: 'react-query',
      override: {
        query: {
          useQuery: true,
          useInfinite: true,
          useInfiniteQueryParam: 'cursor',
          signal: true,
        },
      },
    },
  },
})
