import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import axios from 'axios'
import Papa from 'papaparse'
import { Modal } from '../shared/Modal'
import { MappingFields } from '../institution/MappingFields'
import { MappingPreview } from '../institution/MappingPreview'
import { useSubmitMapping } from '../institution/useSubmitMapping'
import { mappingValuesFromInstitution, type MappingFormValues } from '../institution/mappingForm'
import { inputStyle, labelStyle, fieldWrapStyle } from '../shared/formStyles'
import {
  useGetAccount,
  useListAccounts,
  useListInstitutions,
  useUpdateAccount,
  getListImportsInfiniteQueryKey,
} from '../../api/generated/velociAPI'

interface UploadImportModalProps {
  accountId: string
  open: boolean
  onClose: () => void
}

type Step = 'pick-file' | 'choose-mode' | 'edit-mapping'

interface ParsedCsv {
  file: File
  headers: string[]
  sampleRows: Record<string, string>[]
}

function parseCsv(file: File): Promise<ParsedCsv> {
  return new Promise((resolve, reject) => {
    Papa.parse<Record<string, string>>(file, {
      header: true,
      preview: 5,
      skipEmptyLines: true,
      complete: (results) => {
        resolve({ file, headers: results.meta.fields ?? [], sampleRows: results.data })
      },
      error: (err: Error) => reject(err),
    })
  })
}

export function UploadImportModal({ accountId, open, onClose }: UploadImportModalProps) {
  const [step, setStep] = useState<Step>('pick-file')
  const [parsed, setParsed] = useState<ParsedCsv | null>(null)
  const [mappingValues, setMappingValues] = useState<MappingFormValues | null>(null)
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  const queryClient = useQueryClient()
  const accountQuery = useGetAccount(accountId, { query: { enabled: open } })
  const institutionsQuery = useListInstitutions({ query: { enabled: open } })
  const accountsQuery = useListAccounts(undefined, { query: { enabled: open } })
  const updateAccountMutation = useUpdateAccount()
  const { submitMapping, pending: mappingPending } = useSubmitMapping()

  const account = accountQuery.data?.data.data
  const currentInstitution = institutionsQuery.data?.data.data?.find((inst) => inst.id === account?.institution_id)
  const sharedAccounts = (accountsQuery.data?.data.data ?? []).filter(
    (a) => a.id !== accountId && a.institution_id === currentInstitution?.id,
  )

  function resetForm() {
    setStep('pick-file')
    setParsed(null)
    setMappingValues(null)
    setError('')
  }

  function handleClose() {
    resetForm()
    onClose()
  }

  async function handleFileSelected(file: File) {
    setError('')
    try {
      const result = await parseCsv(file)
      setParsed(result)
      setStep('choose-mode')
    } catch {
      setError('Could not read that file as CSV.')
    }
  }

  function startEditMapping() {
    setMappingValues(currentInstitution ? mappingValuesFromInstitution(currentInstitution) : null)
    setStep('edit-mapping')
  }

  async function uploadFile(file: File) {
    const formData = new FormData()
    formData.append('account_id', accountId)
    formData.append('csv', file)
    await axios.post('/imports', formData, { headers: { 'Content-Type': 'multipart/form-data' } })
    await queryClient.invalidateQueries({ queryKey: getListImportsInfiniteQueryKey({ account_id: accountId }) })
  }

  async function handleUseExisting() {
    if (!parsed) return
    setPending(true)
    setError('')
    try {
      await uploadFile(parsed.file)
      resetForm()
      onClose()
    } catch {
      setError('Something went wrong uploading the file. Please try again.')
    } finally {
      setPending(false)
    }
  }

  async function handleSubmitMapping() {
    if (!parsed || !mappingValues || !account) return
    if (!mappingValues.institutionName.trim()) {
      setError('Institution name is required.')
      return
    }
    setPending(true)
    setError('')
    try {
      const result = await submitMapping(currentInstitution ?? null, mappingValues)
      if (result.forked) {
        await updateAccountMutation.mutateAsync({
          id: accountId,
          data: {
            name: account.name,
            account_type: account.account_type,
            status: account.status,
            interest_rate: account.interest_rate,
            balance_cents: account.balance_cents,
            credit_limit_cents: account.credit_limit_cents,
            institution_id: result.institutionId,
          },
        })
      }
      await uploadFile(parsed.file)
      resetForm()
      onClose()
    } catch {
      setError('Something went wrong saving the mapping. Please try again.')
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal open={open} onClose={handleClose} title="Upload CSV" maxWidth={560}>
      {step === 'pick-file' && (
        <div>
          <div style={fieldWrapStyle}>
            <label style={labelStyle} htmlFor="csv-file">CSV file</label>
            <input
              id="csv-file"
              type="file"
              accept=".csv,text/csv"
              onChange={(e) => {
                const file = e.target.files?.[0]
                if (file) void handleFileSelected(file)
              }}
              style={inputStyle}
            />
          </div>
          {error && <div style={{ fontSize: 12, color: 'var(--commit)' }}>{error}</div>}
        </div>
      )}

      {step === 'choose-mode' && parsed && (
        <div>
          <div style={{ fontSize: 13, color: 'var(--text2)', marginBottom: 14 }}>
            {parsed.file.name} — {parsed.headers.length} columns detected
          </div>

          {currentInstitution ? (
            <>
              <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>
                Current mapping for {currentInstitution.institution_name}:
              </div>
              <MappingPreview institution={currentInstitution} />
            </>
          ) : (
            <div style={{ fontSize: 12, color: 'var(--text3)', marginBottom: 8 }}>
              This account has no mapping yet — choose "Edit mapping" to set one up.
            </div>
          )}

          {error && <div style={{ fontSize: 12, color: 'var(--commit)', marginTop: 12 }}>{error}</div>}

          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
            <button
              type="button"
              onClick={startEditMapping}
              disabled={pending}
              style={{
                background: 'none',
                border: '1px solid var(--border)',
                borderRadius: 4,
                cursor: pending ? 'default' : 'pointer',
                color: 'var(--text2)',
                fontSize: 13,
                padding: '6px 14px',
              }}
            >
              Edit mapping
            </button>
            {currentInstitution && (
              <button
                type="button"
                onClick={() => void handleUseExisting()}
                disabled={pending}
                style={{
                  background: 'var(--accent)',
                  border: 'none',
                  borderRadius: 4,
                  cursor: pending ? 'default' : 'pointer',
                  color: '#fff',
                  fontSize: 13,
                  fontWeight: 600,
                  padding: '6px 14px',
                  opacity: pending ? 0.6 : 1,
                }}
              >
                {pending ? 'Uploading...' : 'Use existing mapping'}
              </button>
            )}
          </div>
        </div>
      )}

      {step === 'edit-mapping' && parsed && mappingValues && (
        <div>
          {currentInstitution && sharedAccounts.length > 0 && (
            <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 12 }}>
              This mapping is shared with {sharedAccounts.length === 1 ? '1 other account' : `${sharedAccounts.length} other accounts`} at{' '}
              {currentInstitution.institution_name} ({sharedAccounts.map((a) => a.name).join(', ')}). Keeping the same
              name updates it for all of them; changing the name creates a separate mapping just for this account.
            </div>
          )}

          <MappingFields values={mappingValues} onChange={setMappingValues} columnOptions={parsed.headers} defaultAdvancedOpen />

          {parsed.sampleRows.length > 0 && (() => {
            const mappedCols = [
              { label: 'Date',         col: mappingValues.dateCol },
              { label: 'Amount',       col: mappingValues.amountCol },
              { label: 'Merchant',     col: mappingValues.merchantCol },
              ...(mappingValues.balanceCol.trim()     ? [{ label: 'Balance',      col: mappingValues.balanceCol }]     : []),
              ...(mappingValues.debitCreditCol.trim() ? [{ label: 'Debit/Credit', col: mappingValues.debitCreditCol }] : []),
              ...(mappingValues.importedIdCol.trim()  ? [{ label: 'ID',           col: mappingValues.importedIdCol }]  : []),
            ].filter(({ col }) => col.trim() !== '')

            return (
              <div style={{ marginTop: 8 }}>
                <div style={labelStyle}>Preview</div>
                <div style={{ overflowX: 'auto', border: '1px solid var(--border)', borderRadius: 4 }}>
                  <table style={{ borderCollapse: 'collapse', width: '100%', fontSize: 12 }}>
                    <thead>
                      <tr>
                        {mappedCols.map(({ label, col }) => (
                          <th key={label} style={{ textAlign: 'left', padding: '4px 8px', borderBottom: '1px solid var(--border)' }}>
                            <div style={{ color: 'var(--text)', fontWeight: 600 }}>{label}</div>
                            <div style={{ color: 'var(--text3)', fontWeight: 400, fontSize: 10 }}>{col}</div>
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {parsed.sampleRows.map((row, i) => (
                        <tr key={i}>
                          {mappedCols.map(({ label, col }) => (
                            <td key={label} style={{ padding: '4px 8px', color: 'var(--text2)', borderBottom: '1px solid var(--border)' }}>
                              {row[col] ?? <span style={{ color: 'var(--text3)', fontStyle: 'italic' }}>—</span>}
                            </td>
                          ))}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </div>
            )
          })()}

          {error && <div style={{ fontSize: 12, color: 'var(--commit)', marginTop: 12 }}>{error}</div>}

          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 16 }}>
            <button
              type="button"
              onClick={() => setStep('choose-mode')}
              disabled={pending || mappingPending}
              style={{
                background: 'none',
                border: '1px solid var(--border)',
                borderRadius: 4,
                cursor: pending ? 'default' : 'pointer',
                color: 'var(--text2)',
                fontSize: 13,
                padding: '6px 14px',
              }}
            >
              Back
            </button>
            <button
              type="button"
              onClick={() => void handleSubmitMapping()}
              disabled={pending || mappingPending}
              style={{
                background: 'var(--accent)',
                border: 'none',
                borderRadius: 4,
                cursor: pending ? 'default' : 'pointer',
                color: '#fff',
                fontSize: 13,
                fontWeight: 600,
                padding: '6px 14px',
                opacity: pending ? 0.6 : 1,
              }}
            >
              {pending ? 'Saving...' : 'Save mapping & upload'}
            </button>
          </div>
        </div>
      )}
    </Modal>
  )
}
