import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Modal } from '../shared/Modal'
import { MappingFields } from '../institution/MappingFields'
import { MappingPreview } from '../institution/MappingPreview'
import { useSubmitMapping } from '../institution/useSubmitMapping'
import { DEFAULT_MAPPING_VALUES, type MappingFormValues } from '../institution/mappingForm'
import { inputStyle, errorInputStyle, labelStyle, fieldWrapStyle } from '../shared/formStyles'
import {
  useListInstitutions,
  useCreateInstitutionAccount,
  getListAccountsQueryKey,
} from '../../api/generated/velociAPI'
import type { CreateInstitutionAccountInputBody } from '../../api/generated/velociAPI.schemas'

interface AddAccountModalProps {
  open: boolean
  onClose: () => void
  defaultStatus: 'active' | 'passive'
}

type AccountType = 'checking' | 'savings' | 'credit' | 'loan' | 'mortgage' | 'investment'

const ACCOUNT_TYPE_LABELS: Record<AccountType, string> = {
  checking: 'Checking',
  savings: 'Savings',
  credit: 'Credit',
  loan: 'Loan',
  mortgage: 'Mortgage',
  investment: 'Investment',
}

type InstitutionChoice = 'existing' | 'new' | 'skip'

/** Renders a dollar-amount text input; parent stores the raw string and converts on submit. */
function DollarInput({
  value,
  onChange,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  return (
    <div style={{ position: 'relative' }}>
      <span
        style={{
          position: 'absolute',
          left: 8,
          top: '50%',
          transform: 'translateY(-50%)',
          color: 'var(--text3)',
          fontSize: 13,
          pointerEvents: 'none',
        }}
      >
        $
      </span>
      <input
        type="text"
        inputMode="decimal"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder ?? '0.00'}
        style={{ ...inputStyle, paddingLeft: 18 }}
      />
    </div>
  )
}

/** Parses a dollar-amount string (e.g. "1,234.56") into integer cents, or null if empty/invalid. */
function dollarsToCents(value: string): number | null {
  const trimmed = value.trim().replace(/,/g, '')
  if (!trimmed) return null
  const parsed = Number(trimmed)
  if (Number.isNaN(parsed)) return null
  return Math.round(parsed * 100)
}

export function AddAccountModal({ open, onClose, defaultStatus }: AddAccountModalProps) {
  // --- Account fields ---
  const [name, setName] = useState('')
  const [accountType, setAccountType] = useState<AccountType>('checking')
  const [status, setStatus] = useState<'active' | 'passive'>(defaultStatus)
  const [balance, setBalance] = useState('')
  const [interestRate, setInterestRate] = useState('')
  const [creditLimit, setCreditLimit] = useState('')

  // --- Institution section ---
  const [institutionChoice, setInstitutionChoice] = useState<InstitutionChoice>('skip')
  const [existingInstitutionId, setExistingInstitutionId] = useState('')
  const [mappingValues, setMappingValues] = useState<MappingFormValues>(DEFAULT_MAPPING_VALUES)

  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  const queryClient = useQueryClient()

  const institutionsQuery = useListInstitutions()
  const institutions = institutionsQuery.data?.data.data ?? []
  const selectedExistingInstitution = institutions.find((inst) => inst.id === existingInstitutionId)

  const createInstitutionAccountMutation = useCreateInstitutionAccount()
  const { submitMapping, pending: mappingPending } = useSubmitMapping()

  const showInterestRate = accountType === 'credit' || accountType === 'loan' || accountType === 'mortgage'
  const showCreditLimit = accountType === 'credit'

  function selectInstitutionChoice(choice: InstitutionChoice) {
    setInstitutionChoice(choice)
    if (choice === 'new') {
      setMappingValues(DEFAULT_MAPPING_VALUES)
    } else if (choice === 'skip') {
      setMappingValues((prev) => ({ ...DEFAULT_MAPPING_VALUES, institutionName: name.trim() || prev.institutionName }))
    }
  }

  function handleNameChange(value: string) {
    setName(value)
    if (institutionChoice === 'skip') {
      setMappingValues((prev) => ({ ...prev, institutionName: value.trim() }))
    }
  }

  function resetForm() {
    setName('')
    setAccountType('checking')
    setStatus(defaultStatus)
    setBalance('')
    setInterestRate('')
    setCreditLimit('')
    setInstitutionChoice('skip')
    setExistingInstitutionId('')
    setMappingValues(DEFAULT_MAPPING_VALUES)
    setError('')
  }

  function handleClose() {
    resetForm()
    onClose()
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError('')

    const trimmedName = name.trim()
    if (!trimmedName) {
      setError('Account name is required.')
      return
    }
    if (institutionChoice === 'existing' && !existingInstitutionId) {
      setError('Select an institution, or choose a different institution option.')
      return
    }
    if (institutionChoice !== 'existing' && !mappingValues.institutionName.trim()) {
      setError('Institution name is required.')
      return
    }

    const accountBody: CreateInstitutionAccountInputBody = {
      name: trimmedName,
      account_type: accountType,
      status,
      balance_cents: dollarsToCents(balance),
      interest_rate: interestRate.trim() ? Number(interestRate) : null,
      credit_limit_cents: showCreditLimit ? dollarsToCents(creditLimit) : null,
    }

    setPending(true)
    try {
      let institutionId = existingInstitutionId
      if (institutionChoice !== 'existing') {
        // For 'skip', reuse an existing institution by name rather than creating a duplicate.
        const nameMatch = institutions.find(
          (inst) => inst.institution_name === mappingValues.institutionName.trim(),
        )
        if (nameMatch) {
          institutionId = nameMatch.id
        } else {
          const result = await submitMapping(null, mappingValues)
          institutionId = result.institutionId
        }
      }

      await createInstitutionAccountMutation.mutateAsync({ id: institutionId, data: accountBody })
      await queryClient.invalidateQueries({ queryKey: getListAccountsQueryKey() })

      resetForm()
      onClose()
    } catch {
      setError('Something went wrong creating the account. Please try again.')
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal open={open} onClose={handleClose} title="Add account" maxWidth={480}>
      <form onSubmit={(e) => void handleSubmit(e)}>
        {/* Name */}
        <div style={fieldWrapStyle}>
          <label style={labelStyle} htmlFor="account-name">Name</label>
          <input
            id="account-name"
            type="text"
            value={name}
            onChange={(e) => handleNameChange(e.target.value)}
            placeholder="e.g. Chase Checking"
            style={!name.trim() && error ? errorInputStyle : inputStyle}
          />
        </div>

        {/* Account type + status */}
        <div style={{ display: 'flex', gap: 12, marginBottom: 14 }}>
          <div style={{ flex: 1 }}>
            <label style={labelStyle} htmlFor="account-type">Account type</label>
            <select
              id="account-type"
              value={accountType}
              onChange={(e) => setAccountType(e.target.value as AccountType)}
              style={{ ...inputStyle, cursor: 'pointer' }}
            >
              {(Object.keys(ACCOUNT_TYPE_LABELS) as AccountType[]).map((t) => (
                <option key={t} value={t}>{ACCOUNT_TYPE_LABELS[t]}</option>
              ))}
            </select>
          </div>
          <div style={{ flex: 1 }}>
            <label style={labelStyle} htmlFor="account-status">Status</label>
            <select
              id="account-status"
              value={status}
              onChange={(e) => setStatus(e.target.value as 'active' | 'passive')}
              style={{ ...inputStyle, cursor: 'pointer' }}
            >
              <option value="active">Active</option>
              <option value="passive">Passive</option>
            </select>
          </div>
        </div>

        {/* Balance */}
        <div style={fieldWrapStyle}>
          <label style={labelStyle} htmlFor="account-balance">Balance (optional)</label>
          <DollarInput value={balance} onChange={setBalance} />
        </div>

        {/* Interest rate — credit/loan/mortgage only */}
        {showInterestRate && (
          <div style={fieldWrapStyle}>
            <label style={labelStyle} htmlFor="account-interest-rate">Interest rate % (optional)</label>
            <input
              id="account-interest-rate"
              type="text"
              inputMode="decimal"
              value={interestRate}
              onChange={(e) => setInterestRate(e.target.value)}
              placeholder="e.g. 19.99"
              style={inputStyle}
            />
          </div>
        )}

        {/* Credit limit — credit only */}
        {showCreditLimit && (
          <div style={fieldWrapStyle}>
            <label style={labelStyle} htmlFor="account-credit-limit">Credit limit (optional)</label>
            <DollarInput value={creditLimit} onChange={setCreditLimit} />
          </div>
        )}

        {/* Institution section */}
        <div style={{ marginTop: 20, marginBottom: 14, borderTop: '1px solid var(--border)', paddingTop: 14 }}>
          <div style={labelStyle}>Institution</div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 10 }}>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--text)', cursor: 'pointer' }}>
              <input
                type="radio"
                name="institution-choice"
                value="existing"
                checked={institutionChoice === 'existing'}
                onChange={() => selectInstitutionChoice('existing')}
              />
              Link to existing institution
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--text)', cursor: 'pointer' }}>
              <input
                type="radio"
                name="institution-choice"
                value="new"
                checked={institutionChoice === 'new'}
                onChange={() => selectInstitutionChoice('new')}
              />
              Create new institution
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--text)', cursor: 'pointer' }}>
              <input
                type="radio"
                name="institution-choice"
                value="skip"
                checked={institutionChoice === 'skip'}
                onChange={() => selectInstitutionChoice('skip')}
              />
              Skip for now
            </label>
          </div>

          {/* Existing institution select + read-only mapping preview */}
          {institutionChoice === 'existing' && (
            <div style={fieldWrapStyle}>
              <label style={labelStyle} htmlFor="existing-institution">Institution</label>
              <select
                id="existing-institution"
                value={existingInstitutionId}
                onChange={(e) => setExistingInstitutionId(e.target.value)}
                style={{ ...inputStyle, cursor: 'pointer', marginBottom: 8 }}
                disabled={institutionsQuery.isLoading}
              >
                <option value="">
                  {institutionsQuery.isLoading ? 'Loading institutions...' : 'Select an institution'}
                </option>
                {institutions.map((inst) => (
                  <option key={inst.id} value={inst.id}>{inst.institution_name}</option>
                ))}
              </select>
              {!institutionsQuery.isLoading && institutions.length === 0 && (
                <div style={{ fontSize: 12, color: 'var(--text3)', marginTop: 4 }}>
                  No institutions yet — choose "Create new institution" instead.
                </div>
              )}
              {selectedExistingInstitution && <MappingPreview institution={selectedExistingInstitution} />}
            </div>
          )}

          {/* New / skip institution mapping editor */}
          {institutionChoice !== 'existing' && (
            <MappingFields
              values={mappingValues}
              onChange={setMappingValues}
              nameEditable={institutionChoice === 'new'}
            />
          )}
        </div>

        {/* Error message */}
        {error && (
          <div style={{ fontSize: 12, color: 'var(--commit)', marginBottom: 12 }}>
            {error}
          </div>
        )}

        {/* Actions */}
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 8 }}>
          <button
            type="button"
            onClick={handleClose}
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
            Cancel
          </button>
          <button
            type="submit"
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
            {pending ? 'Creating...' : 'Create account'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
