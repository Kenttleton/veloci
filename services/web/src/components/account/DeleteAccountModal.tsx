import { useState, useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { Modal } from '../shared/Modal'
import { inputStyle, labelStyle, fieldWrapStyle } from '../shared/formStyles'
import { useDeleteAccount } from '../../api/generated/velociAPI'
import type { AccountView } from '../../api/generated/velociAPI.schemas'

interface DeleteAccountModalProps {
  account: AccountView | null
  open: boolean
  onClose: () => void
}

export function DeleteAccountModal({ account, open, onClose }: DeleteAccountModalProps) {
  const [confirmValue, setConfirmValue] = useState('')
  const [error, setError] = useState('')
  const navigate = useNavigate()
  const { mutateAsync, isPending } = useDeleteAccount()

  useEffect(() => {
    if (!open) {
      setConfirmValue('')
      setError('')
    }
  }, [open])

  const canDelete = account !== null && confirmValue === account.name

  async function handleDelete() {
    if (!account || !canDelete) return
    setError('')
    try {
      await mutateAsync({ id: account.id })
      void navigate('/budget')
    } catch {
      setError('Failed to delete account. Please try again.')
    }
  }

  function handleClose() {
    setConfirmValue('')
    setError('')
    onClose()
  }

  return (
    <Modal open={open} onClose={handleClose} title="Delete Account">
      <div style={fieldWrapStyle}>
        <p style={{ margin: '0 0 14px', fontSize: 13, color: 'var(--text)', lineHeight: 1.5 }}>
          This will permanently delete <strong>{account?.name}</strong> and all its data. This cannot be undone.
        </p>
        <label style={labelStyle} htmlFor="delete-confirm">
          Type account name to confirm
        </label>
        <input
          id="delete-confirm"
          type="text"
          value={confirmValue}
          onChange={(e) => setConfirmValue(e.target.value)}
          placeholder="Type account name to confirm"
          disabled={isPending}
          style={inputStyle}
        />
      </div>

      {error && (
        <div style={{ fontSize: 12, color: 'var(--commit)', marginBottom: 12 }}>
          {error}
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 8 }}>
        <button
          type="button"
          onClick={handleClose}
          disabled={isPending}
          style={{
            background: 'none',
            border: '1px solid var(--border)',
            borderRadius: 4,
            cursor: isPending ? 'default' : 'pointer',
            color: 'var(--text2)',
            fontSize: 13,
            padding: '6px 14px',
          }}
        >
          Cancel
        </button>
        <button
          type="button"
          onClick={() => void handleDelete()}
          disabled={!canDelete || isPending}
          style={{
            background: 'var(--commit)',
            border: 'none',
            borderRadius: 4,
            cursor: !canDelete || isPending ? 'default' : 'pointer',
            color: '#fff',
            fontSize: 13,
            fontWeight: 600,
            padding: '6px 14px',
            opacity: !canDelete || isPending ? 0.5 : 1,
          }}
        >
          {isPending ? 'Deleting...' : 'Delete'}
        </button>
      </div>
    </Modal>
  )
}
