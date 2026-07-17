import { useState, useRef, useEffect } from 'react'
import { Plus, Check, X, Pencil, Trash2, ChevronDown, ChevronRight } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { useListInstitutions, useDeleteInstitution, getListInstitutionsQueryKey } from '../api/generated/velociAPI'
import type { InstitutionView, LabelView } from '../api/generated/velociAPI.schemas'
import { MappingFields } from '../components/institution/MappingFields'
import { MappingPreview } from '../components/institution/MappingPreview'
import { useSubmitMapping } from '../components/institution/useSubmitMapping'
import { mappingValuesFromInstitution, DEFAULT_MAPPING_VALUES, type MappingFormValues } from '../components/institution/mappingForm'
import { getToken } from '../auth/tokens'

type Tab = 'labels' | 'institutions'

// ── Labels (lifted from SettingsPage) ────────────────────────────────────────

type Label = LabelView & { entry_count?: number }

async function apiFetch<T>(path: string, options?: RequestInit): Promise<T> {
  const token = getToken()
  const base = (import.meta.env.VITE_API_URL as string | undefined) ?? '/api'
  const res = await fetch(`${base}${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...options?.headers,
    },
  })
  return res.json() as Promise<T>
}

async function fetchLabels(): Promise<Label[]> {
  const result = await apiFetch<{ data: Label[] }>('/labels')
  return result.data ?? []
}
async function createLabel(name: string): Promise<Label> {
  const result = await apiFetch<{ data: Label }>('/labels', { method: 'POST', body: JSON.stringify({ name }) })
  return result.data
}
async function updateLabel(id: string, name: string): Promise<Label> {
  const result = await apiFetch<{ data: Label }>(`/labels/${id}`, { method: 'PUT', body: JSON.stringify({ name }) })
  return result.data
}

function LabelsTab() {
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
    fetchLabels()
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

  function cancelEdit() { setEditingId(null); setEditValue(''); setDuplicateError('') }

  async function saveEdit(id: string) {
    const trimmed = editValue.trim()
    if (!trimmed) { cancelEdit(); return }
    if (labels.some((l) => l.name === trimmed && l.id !== id)) { setDuplicateError('A label with this name already exists.'); return }
    try {
      const updated = await updateLabel(id, trimmed)
      setLabels((prev) => prev.map((l) => (l.id === id ? updated : l)))
      cancelEdit()
    } catch { /* handled */ }
  }

  function startAddNew() {
    setAddingNew(true); setNewLabelName(''); setDuplicateError('')
    setTimeout(() => newInputRef.current?.focus(), 10)
  }

  async function saveNewLabel() {
    const trimmed = newLabelName.trim()
    if (!trimmed) { setAddingNew(false); return }
    if (labels.some((l) => l.name === trimmed)) { setDuplicateError('A label with this name already exists.'); return }
    try {
      const label = await createLabel(trimmed)
      setLabels((prev) => [label, ...prev])
      setAddingNew(false); setNewLabelName(''); setDuplicateError('')
    } catch { /* handled */ }
  }

  const inputStyle = (error: boolean): React.CSSProperties => ({
    background: 'var(--surface2)',
    border: `1px solid ${error ? 'var(--commit)' : 'var(--accent)'}`,
    borderRadius: 4,
    padding: '4px 8px',
    color: 'var(--text)',
    fontSize: 13,
    outline: 'none',
    width: '100%',
  })

  return (
    <section style={{ maxWidth: 560 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
        <div>
          <h2 style={{ margin: 0, fontSize: 15, fontWeight: 700, color: 'var(--text)' }}>Labels</h2>
          <p style={{ margin: '4px 0 0', fontSize: 12, color: 'var(--text3)' }}>Used to tag and group budget entries</p>
        </div>
        <button
          onClick={startAddNew}
          style={{ display: 'flex', alignItems: 'center', gap: 4, background: 'none', border: 'none', cursor: 'pointer', color: 'var(--accent)', fontSize: 13, padding: '4px 0' }}
        >
          <Plus size={13} />
          New label
        </button>
      </div>

      {loading ? (
        [0, 1, 2].map((i) => (
          <div key={i} style={{ height: 40, background: 'var(--surface)', borderRadius: 4, marginBottom: 4, opacity: 0.4 }} />
        ))
      ) : (
        <div style={{ border: '1px solid var(--border)', borderRadius: 4, overflow: 'hidden' }}>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px 80px', gap: 12, padding: '8px 12px', background: 'var(--surface)', borderBottom: '1px solid var(--border)' }}>
            {['Name', 'Used by', 'Actions'].map((h) => (
              <span key={h} style={{ fontSize: 11, color: 'var(--text3)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{h}</span>
            ))}
          </div>

          {addingNew && (
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px 80px', gap: 12, padding: '8px 12px', borderBottom: '1px solid var(--border)', background: 'var(--bg)', alignItems: 'center' }}>
              <div>
                <input ref={newInputRef} type="text" value={newLabelName}
                  onChange={(e) => { setNewLabelName(e.target.value); setDuplicateError('') }}
                  onKeyDown={(e) => { if (e.key === 'Enter') void saveNewLabel(); if (e.key === 'Escape') { setAddingNew(false); setNewLabelName('') } }}
                  placeholder="Label name" style={inputStyle(!!duplicateError && !editingId)} />
                {duplicateError && !editingId && <div style={{ fontSize: 11, color: 'var(--commit)', marginTop: 2 }}>{duplicateError}</div>}
              </div>
              <span style={{ fontSize: 12, color: 'var(--text3)' }}>—</span>
              <div style={{ display: 'flex', gap: 6 }}>
                <button onClick={() => void saveNewLabel()} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--income)', padding: 0 }}><Check size={14} /></button>
                <button onClick={() => { setAddingNew(false); setNewLabelName('') }} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: 0 }}><X size={14} /></button>
              </div>
            </div>
          )}

          {labels.map((label, idx) => {
            const isEditing = editingId === label.id
            return (
              <div key={label.id} style={{ display: 'grid', gridTemplateColumns: '1fr 100px 80px', gap: 12, padding: '9px 12px', borderBottom: idx < labels.length - 1 ? '1px solid var(--border)' : 'none', background: isEditing ? 'var(--surface2)' : 'var(--surface)', alignItems: 'center' }}>
                <div>
                  {isEditing ? (
                    <>
                      <input ref={editInputRef} type="text" value={editValue}
                        onChange={(e) => { setEditValue(e.target.value); setDuplicateError('') }}
                        onKeyDown={(e) => { if (e.key === 'Enter') void saveEdit(label.id); if (e.key === 'Escape') cancelEdit() }}
                        style={inputStyle(!!duplicateError)} />
                      {duplicateError && <div style={{ fontSize: 11, color: 'var(--commit)', marginTop: 2 }}>{duplicateError}</div>}
                    </>
                  ) : (
                    <span style={{ fontSize: 13, color: 'var(--text)', cursor: 'pointer' }} onClick={() => startEdit(label)}>
                      {label.name || <span style={{ color: 'var(--text3)' }}>(empty name)</span>}
                    </span>
                  )}
                </div>
                <span style={{ fontSize: 12, color: 'var(--text2)' }}>{label.entry_count != null ? `${label.entry_count} entries` : '—'}</span>
                <div style={{ display: 'flex', gap: 6 }}>
                  {isEditing ? (
                    <>
                      <button onClick={() => void saveEdit(label.id)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--income)', padding: 0 }}><Check size={14} /></button>
                      <button onClick={cancelEdit} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: 0 }}><X size={14} /></button>
                    </>
                  ) : (
                    <button onClick={() => startEdit(label)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text2)', fontSize: 12, padding: 0 }}>Rename</button>
                  )}
                </div>
              </div>
            )
          })}

          {labels.length === 0 && !addingNew && (
            <div style={{ padding: '16px 12px', color: 'var(--text3)', fontSize: 13 }}>No labels yet. Create one above.</div>
          )}
        </div>
      )}
    </section>
  )
}

// ── Institution Mappings ──────────────────────────────────────────────────────

type EditState = { mode: 'create' } | { mode: 'edit'; institution: InstitutionView }

function InstitutionsTab() {
  const queryClient = useQueryClient()
  const { data, isLoading } = useListInstitutions()
  const institutions = data?.data.data ?? []
  const deleteMutation = useDeleteInstitution()
  const { submitMapping, pending: savePending } = useSubmitMapping()

  const [editState, setEditState] = useState<EditState | null>(null)
  const [formValues, setFormValues] = useState<MappingFormValues>(DEFAULT_MAPPING_VALUES)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const [deleteConfirmId, setDeleteConfirmId] = useState<string | null>(null)
  const [error, setError] = useState('')

  function openCreate() {
    setFormValues(DEFAULT_MAPPING_VALUES)
    setEditState({ mode: 'create' })
    setError('')
  }

  function openEdit(inst: InstitutionView) {
    setFormValues(mappingValuesFromInstitution(inst))
    setEditState({ mode: 'edit', institution: inst })
    setError('')
  }

  function closeForm() { setEditState(null); setError('') }

  async function handleSave() {
    if (!editState) return
    if (!formValues.institutionName.trim()) { setError('Institution name is required.'); return }
    try {
      const starting = editState.mode === 'edit' ? editState.institution : null
      await submitMapping(starting, formValues)
      await queryClient.invalidateQueries({ queryKey: getListInstitutionsQueryKey() })
      closeForm()
    } catch {
      setError('Something went wrong saving the mapping.')
    }
  }

  async function handleDelete(id: string) {
    try {
      await deleteMutation.mutateAsync({ id })
      await queryClient.invalidateQueries({ queryKey: getListInstitutionsQueryKey() })
      setDeleteConfirmId(null)
    } catch {
      setDeleteConfirmId(null)
    }
  }

  if (editState) {
    return (
      <section style={{ maxWidth: 600 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 20 }}>
          <button onClick={closeForm} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--accent)', fontSize: 13, padding: 0 }}>
            ← Back
          </button>
          <h2 style={{ margin: 0, fontSize: 15, fontWeight: 700, color: 'var(--text)' }}>
            {editState.mode === 'create' ? 'New Institution Mapping' : `Edit: ${editState.institution.institution_name}`}
          </h2>
        </div>

        <MappingFields values={formValues} onChange={setFormValues} columnOptions={[]} defaultAdvancedOpen />

        {error && <div style={{ fontSize: 12, color: 'var(--commit)', marginTop: 12 }}>{error}</div>}

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 20 }}>
          <button onClick={closeForm} disabled={savePending} style={{ background: 'none', border: '1px solid var(--border)', borderRadius: 4, cursor: 'pointer', color: 'var(--text2)', fontSize: 13, padding: '6px 14px' }}>
            Cancel
          </button>
          <button onClick={() => void handleSave()} disabled={savePending} style={{ background: 'var(--accent)', border: 'none', borderRadius: 4, cursor: savePending ? 'default' : 'pointer', color: '#fff', fontSize: 13, fontWeight: 600, padding: '6px 14px', opacity: savePending ? 0.6 : 1 }}>
            {savePending ? 'Saving…' : 'Save mapping'}
          </button>
        </div>
      </section>
    )
  }

  return (
    <section style={{ maxWidth: 640 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
        <div>
          <h2 style={{ margin: 0, fontSize: 15, fontWeight: 700, color: 'var(--text)' }}>Institution Mappings</h2>
          <p style={{ margin: '4px 0 0', fontSize: 12, color: 'var(--text3)' }}>CSV column mappings for each financial institution</p>
        </div>
        <button
          onClick={openCreate}
          style={{ display: 'flex', alignItems: 'center', gap: 4, background: 'none', border: 'none', cursor: 'pointer', color: 'var(--accent)', fontSize: 13, padding: '4px 0' }}
        >
          <Plus size={13} />
          New mapping
        </button>
      </div>

      {isLoading ? (
        [0, 1].map((i) => <div key={i} style={{ height: 48, background: 'var(--surface)', borderRadius: 4, marginBottom: 4, opacity: 0.4 }} />)
      ) : institutions.length === 0 ? (
        <div style={{ padding: '24px 0', color: 'var(--text3)', fontSize: 13 }}>No institution mappings yet. Create one to enable CSV imports.</div>
      ) : (
        <div style={{ border: '1px solid var(--border)', borderRadius: 4, overflow: 'hidden' }}>
          {institutions.map((inst, idx) => {
            const isExpanded = expandedId === inst.id
            const isLast = idx === institutions.length - 1
            return (
              <div key={inst.id} style={{ borderBottom: isLast ? 'none' : '1px solid var(--border)' }}>
                {/* Row */}
                <div
                  style={{ display: 'flex', alignItems: 'center', padding: '10px 12px', gap: 8, background: isExpanded ? 'var(--surface2)' : 'var(--surface)', cursor: 'pointer' }}
                  onClick={() => setExpandedId(isExpanded ? null : inst.id)}
                >
                  <span style={{ color: 'var(--text3)', display: 'flex', alignItems: 'center' }}>
                    {isExpanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                  </span>
                  <span style={{ flex: 1, fontSize: 13, fontWeight: 500, color: 'var(--text)' }}>{inst.institution_name}</span>
                  <span style={{ fontSize: 11, color: 'var(--text3)', background: 'var(--surface2)', padding: '2px 6px', borderRadius: 4 }}>{inst.source_type}</span>
                  <div style={{ display: 'flex', gap: 6 }} onClick={(e) => e.stopPropagation()}>
                    <button
                      onClick={() => openEdit(inst)}
                      title="Edit"
                      style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text2)', padding: '2px 4px' }}
                    >
                      <Pencil size={13} />
                    </button>
                    {deleteConfirmId === inst.id ? (
                      <div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                        <span style={{ fontSize: 11, color: 'var(--commit)' }}>Delete?</span>
                        <button onClick={() => void handleDelete(inst.id)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--commit)', padding: '2px 4px', fontSize: 11 }}>Yes</button>
                        <button onClick={() => setDeleteConfirmId(null)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: '2px 4px', fontSize: 11 }}>No</button>
                      </div>
                    ) : (
                      <button
                        onClick={() => setDeleteConfirmId(inst.id)}
                        title="Delete"
                        style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text3)', padding: '2px 4px' }}
                      >
                        <Trash2 size={13} />
                      </button>
                    )}
                  </div>
                </div>

                {/* Expanded preview */}
                {isExpanded && (
                  <div style={{ borderTop: '1px solid var(--border)', background: 'var(--bg)', padding: '12px 16px' }}>
                    <MappingPreview institution={inst} />
                  </div>
                )}
              </div>
            )
          })}
        </div>
      )}
    </section>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export function ConfigurationPage() {
  const [tab, setTab] = useState<Tab>('labels')

  const tabBtn = (t: Tab): React.CSSProperties => ({
    padding: '6px 14px',
    borderRadius: 6,
    fontSize: 13,
    fontWeight: tab === t ? 600 : 400,
    cursor: 'pointer',
    border: 'none',
    background: tab === t ? 'var(--surface2)' : 'transparent',
    color: tab === t ? 'var(--text)' : 'var(--text2)',
  })

  return (
    <div style={{ display: 'flex', flexDirection: 'column', height: '100%', overflow: 'hidden' }}>
      {/* Topbar */}
      <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
        <h1 style={{ margin: '0 0 10px', fontSize: 18, fontWeight: 700, color: 'var(--text)', letterSpacing: '-0.02em' }}>
          Configuration
        </h1>
        <div style={{ display: 'flex', gap: 4 }}>
          <button style={tabBtn('labels')} onClick={() => setTab('labels')}>Labels</button>
          <button style={tabBtn('institutions')} onClick={() => setTab('institutions')}>Institution Mappings</button>
        </div>
      </div>

      <div style={{ flex: 1, overflow: 'auto', padding: '24px 20px' }}>
        {tab === 'labels' && <LabelsTab />}
        {tab === 'institutions' && <InstitutionsTab />}
      </div>
    </div>
  )
}
