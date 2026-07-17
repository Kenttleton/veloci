import React from 'react'
import * as TooltipPrimitive from '@radix-ui/react-tooltip'

interface TermTooltipProps {
  term: string
  definition: string
  children?: React.ReactNode
}

export function TermTooltip({ term, definition, children }: TermTooltipProps) {
  return (
    <TooltipPrimitive.Provider delayDuration={300}>
      <TooltipPrimitive.Root>
        <TooltipPrimitive.Trigger asChild>
          <span
            style={{
              borderBottom: '1px dashed var(--text3)',
              cursor: 'help',
              display: 'inline',
            }}
          >
            {children ?? term}
          </span>
        </TooltipPrimitive.Trigger>
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content
            sideOffset={4}
            style={{
              background: 'var(--surface2)',
              border: '1px solid var(--border)',
              borderRadius: 4,
              padding: '6px 10px',
              fontSize: '12px',
              color: 'var(--text2)',
              maxWidth: 280,
              lineHeight: 1.4,
              zIndex: 9999,
              boxShadow: '0 4px 12px rgba(0,0,0,0.4)',
            }}
          >
            {definition}
            <TooltipPrimitive.Arrow
              style={{ fill: 'var(--border)' }}
            />
          </TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      </TooltipPrimitive.Root>
    </TooltipPrimitive.Provider>
  )
}
