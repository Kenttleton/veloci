import { useState, useEffect, useRef } from 'react'
import { Plus, Check, X } from 'lucide-react'
import { getLabels, createLabel, updateLabel } from '../api/resources'
import type { Label } from '../api/resources'

export function SettingsPage() {
  const [labels, setLabels] = useState<Label[]>([])
  const [loading, setLoading] = useState(true)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editValue, setEditValue] = useState('')
  const [addingNew, setAddingNew] = useState(false)
  const [newLabelName, setNewLabelName] = useState('')
  const [duplicateError, setDuplicateError] = useState('')
  const newInputRef = useRef<HTMLInputElement>(null)
  const editInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    getLabels()
      .then(setLabels)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [])

  function startEdit(label: Label) {
    setEditingId(label.id)
    setEditValue(label.name)
    setDuplicateError('')
    setTimeout(() => editInputRef.current?.focus(), 10)
  }

  function cancelEdit() {
    setEditingId(null)
    setEditValue('')
    setDuplicateError('')
  }

  async function saveEdit(id: string) {
    const trimmed = editValue.trim()
    if (!trimmed) { cancelEdit(); return }
    if (labels.some((l) => l.name === trimmed && l.id !== id)) {
      setDuplicateError('A label with this name already exists.')
      return
    }
    try {
      const updated = await updateLabel(id, trimmed)
      setLabels((prev) => prev.map((l) => (l.id === id ? updated : l)))
      setEditingId(null)
      setEditValue('')
      setDuplicateError('')
    } catch {
      //
    }
  }

  function startAddNew() {
    setAddingNew(true)
    setNewLabelName('')
    setDuplicateError('')
    setTimeout(() => newInputRef.current?.focus(), 10)
  }

  async function saveNewLabel() {
    const trimmed = newLabelName.trim()
    if (!trimmed) { setAddingNew(false); return }
    if (labels.some((l) => l.name === trimmed)) {
      setDuplicateError('A label with this name already exists.')
      return
    }
    try {
      const label = await createLabel(trimmed)
      setLabels((prev) => [label, ...prev])
      setAddingNew(false)
      setNewLabelName('')
      setDuplicateError('')
    } catch {
      //
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      {/* Topbar */}
      <div
        style={{
          padding: '14px 20px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        <h1 style={{ margin: 0, fontSize: 18, fontWeight: 700, color: 'var(--text)', letterSpacing: '-0.02em' }}>
          Settings
        </h1>
      </div>

      <div style={{ flex: 1, overflow: 'auto', padding: '20px' }}>
        {/* Labels section */}
        <section style={{ maxWidth: 560 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
            <h2 style={{ margin: 0, fontSize: 15, fontWeight: 700, color: 'var(--text)' }}>Labels</h2>
            <button
              onClick={startAddNew}
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: 4,
                background: 'none',
                border: 'none',
                cursor: 'pointer',
                color: 'var(--accent)',
                fontSize: 13,
                padding: '4px 0',
              }}
            >
              <Plus size={13} />
              New label
            </button>
          </div>

          {loading ? (
            <div>
              {[0, 1, 2].map((i) => (
                <div
                  key={i}
                  style={{
                    height: 40,
                    background: 'var(--surface)',
                    borderRadius: 4,
                    marginBottom: 4,
                    opacity: 0.4,
                  }}
                />
              ))}
            </div>
          ) : (
            <div
              style={{
                border: '1px solid var(--border)',
                borderRadius: 4,
                overflow: 'hidden',
              }}
            >
              {/* Table header */}
              <div
                style={{
                  display: 'grid',
                  gridTemplateColumns: '1fr 100px 80px',
                  gap: 12,
                  padding: '8px 12px',
                  background: 'var(--surface)',
                  borderBottom: '1px solid var(--border)',
                }}
              >
                <span style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Name</span>
                <span style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Used by</span>
                <span style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>Actions</span>
              </div>

              {/* New label inline row */}
              {addingNew && (
                <div
                  style={{
                    display: 'grid',
                    gridTemplateColumns: '1fr 100px 80px',
                    gap: 12,
                    padding: '8px 12px',
                    borderBottom: '1px solid var(--border)',
                    background: 'var(--bg)',
                    alignItems: 'center',
                  }}
                >
                  <div>
                    <input
                      ref={newInputRef}
                      type="text"
                      value={newLabelName}
                      onChange={(e) => { setNewLabelName(e.target.value); setDuplicateError('') }}
                      onKeyDown={(e) => {
                        if (e.key === 'Enter') void saveNewLabel()
                        if (e.key === 'Escape') { setAddingNew(false); setNewLabelName('') }
                      }}
                      placeholder="Label name"
                      style={{
                        background: 'var(--surface2)',
                        border: `1px solid ${duplicateError && !editingId ? 'var(--commit)' : 'var(--accent)'}`,
                        borderRadius: 4,
                        padding: '4px 8px',
                        color: 'var(--text)',
                        fontSize: 13,
                        outline: 'none',
                        width: '100%',
                      }}
                    />
                    {duplicateError && !editingId && (
                      <div style={{ fontSize: 11, color: 'var(--commit)', marginTop: 2 }}>
                        {duplicateError}
                      </div>
                    )}
                  </div>
                  <span style={{ fontSize: 12, color: 'var(--text3)' }}>—</span>
                  <div style={{ display: 'flex', gap: 6 }}>
                    <button
                      onClick={() => void saveNewLabel()}
                      style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--margin-pos)', padding: 0 }}
                    >
                      <Check size={14} />
                    </button>
                    <button
                      onClick={() => { setAddingNew(false); setNewLabelName('') }}
                      style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: 0 }}
                    >
                      <X size={14} />
                    </button>
                  </div>
                </div>
              )}

              {/* Label rows */}
              {labels.map((label, idx) => {
                const isEditing = editingId === label.id
                return (
                  <div
                    key={label.id}
                    style={{
                      display: 'grid',
                      gridTemplateColumns: '1fr 100px 80px',
                      gap: 12,
                      padding: '9px 12px',
                      borderBottom: idx < labels.length - 1 ? '1px solid var(--border)' : 'none',
                      background: isEditing ? 'var(--surface2)' : 'var(--surface)',
                      alignItems: 'center',
                    }}
                  >
                    <div>
                      {isEditing ? (
                        <>
                          <input
                            ref={editInputRef}
                            type="text"
                            value={editValue}
                            onChange={(e) => { setEditValue(e.target.value); setDuplicateError('') }}
                            onKeyDown={(e) => {
                              if (e.key === 'Enter') void saveEdit(label.id)
                              if (e.key === 'Escape') cancelEdit()
                            }}
                            style={{
                              background: 'var(--bg)',
                              border: `1px solid ${duplicateError ? 'var(--commit)' : 'var(--accent)'}`,
                              borderRadius: 4,
                              padding: '3px 7px',
                              color: 'var(--text)',
                              fontSize: 13,
                              outline: 'none',
                              width: '100%',
                            }}
                          />
                          {duplicateError && (
                            <div style={{ fontSize: 11, color: 'var(--commit)', marginTop: 2 }}>
                              {duplicateError}
                            </div>
                          )}
                        </>
                      ) : (
                        <span
                          style={{ fontSize: 13, color: 'var(--text)', cursor: 'pointer' }}
                          onClick={() => startEdit(label)}
                        >
                          {label.name || <span style={{ color: 'var(--text3)' }}>(empty name)</span>}
                        </span>
                      )}
                    </div>
                    <span style={{ fontSize: 12, color: 'var(--text2)' }}>
                      {label.entry_count != null ? `${label.entry_count} entries` : '—'}
                    </span>
                    <div style={{ display: 'flex', gap: 6 }}>
                      {isEditing ? (
                        <>
                          <button
                            onClick={() => void saveEdit(label.id)}
                            style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--margin-pos)', padding: 0 }}
                          >
                            <Check size={14} />
                          </button>
                          <button
                            onClick={cancelEdit}
                            style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: 0 }}
                          >
                            <X size={14} />
                          </button>
                        </>
                      ) : (
                        <button
                          onClick={() => startEdit(label)}
                          style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text2)', fontSize: 12, padding: 0 }}
                        >
                          Rename
                        </button>
                      )}
                    </div>
                  </div>
                )
              })}

              {labels.length === 0 && !addingNew && (
                <div style={{ padding: '16px 12px', color: 'var(--text3)', fontSize: 13 }}>
                  No labels yet. Create one above.
                </div>
              )}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
