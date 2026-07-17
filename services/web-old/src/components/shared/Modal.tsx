import React from 'react'
import * as Dialog from '@radix-ui/react-dialog'
import { X } from 'lucide-react'

interface ModalProps {
  open: boolean
  onClose: () => void
  title: string
  children: React.ReactNode
  /** Panel max-width in px. Defaults to 480. */
  maxWidth?: number
}

export function Modal({ open, onClose, title, children, maxWidth = 480 }: ModalProps) {
  return (
    <Dialog.Root
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
    >
      <Dialog.Portal>
        <Dialog.Overlay
          style={{
            position: 'fixed',
            inset: 0,
            background: 'rgba(10, 14, 24, 0.6)',
            display: 'flex',
            alignItems: 'flex-start',
            justifyContent: 'center',
            padding: '48px 16px',
            overflowY: 'auto',
            zIndex: 1000,
          }}
        >
          <Dialog.Content
            onOpenAutoFocus={(e) => {
              // Let the first focusable field inside take focus instead of the panel itself
              const panel = e.currentTarget as HTMLElement
              const first = panel.querySelector<HTMLElement>(
                'input, select, textarea, button:not([aria-label="Close"])'
              )
              if (first) {
                e.preventDefault()
                first.focus()
              }
            }}
            style={{
              background: 'var(--surface)',
              border: '1px solid var(--border)',
              borderRadius: 6,
              width: '100%',
              maxWidth,
              boxShadow: '0 12px 32px rgba(0, 0, 0, 0.45)',
              outline: 'none',
            }}
          >
            {/* Header */}
            <div
              style={{
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'space-between',
                padding: '14px 16px',
                borderBottom: '1px solid var(--border)',
              }}
            >
              <Dialog.Title
                style={{ margin: 0, fontSize: 15, fontWeight: 700, color: 'var(--text)' }}
              >
                {title}
              </Dialog.Title>
              <Dialog.Description asChild>
                <span style={{ display: 'none' }} />
              </Dialog.Description>
              <Dialog.Close asChild>
                <button
                  aria-label="Close"
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    background: 'none',
                    border: 'none',
                    cursor: 'pointer',
                    color: 'var(--text3)',
                    padding: 4,
                    borderRadius: 4,
                  }}
                >
                  <X size={16} />
                </button>
              </Dialog.Close>
            </div>

            {/* Body */}
            <div style={{ padding: 16, maxHeight: '75vh', overflowY: 'auto' }}>{children}</div>
          </Dialog.Content>
        </Dialog.Overlay>
      </Dialog.Portal>
    </Dialog.Root>
  )
}
