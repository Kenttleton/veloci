import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import * as Collapsible from '@radix-ui/react-collapsible'
import { ChevronRight } from 'lucide-react'
import { Modal } from '../shared/Modal'
import {
  useListInstitutions,
  useCreateInstitution,
  useCreateInstitutionAccount,
  useCreateAccount,
  getListAccountsQueryKey,
} from '../../api/generated/velociAPI'
import type {
  CreateInstitutionInputBody,
  CreateInstitutionAccountInputBody,
  CreateAccountInputBody,
} from '../../api/generated/velociAPI.schemas'

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

type InstitutionChoice = 'existing' | 'new' | 'none'

const inputStyle: React.CSSProperties = {
  background: 'var(--surface2)',
  border: '1px solid var(--border)',
  borderRadius: 4,
  padding: '6px 8px',
  color: 'var(--text)',
  fontSize: 13,
  outline: 'none',
  width: '100%',
}

const errorInputStyle: React.CSSProperties = {
  ...inputStyle,
  border: '1px solid var(--commit)',
}

const labelStyle: React.CSSProperties = {
  display: 'block',
  fontSize: 12,
  color: 'var(--text3)',
  marginBottom: 4,
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
}

const fieldWrapStyle: React.CSSProperties = { marginBottom: 14 }

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
  const [institutionChoice, setInstitutionChoice] = useState<InstitutionChoice>('none')
  const [existingInstitutionId, setExistingInstitutionId] = useState('')
  const [newInstitutionName, setNewInstitutionName] = useState('')
  const [advancedOpen, setAdvancedOpen] = useState(false)

  // Advanced institution settings (only relevant when institutionChoice === 'new')
  const [amountSignConvention, setAmountSignConvention] = useState('positive_is_credit')
  const [dedupWindowDays, setDedupWindowDays] = useState('3')
  const [settlementWindowDays, setSettlementWindowDays] = useState('14')
  const [amountTolerancePct, setAmountTolerancePct] = useState('0.5')
  const [dateCol, setDateCol] = useState('date')
  const [amountCol, setAmountCol] = useState('amount')
  const [merchantCol, setMerchantCol] = useState('description')

  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)

  const queryClient = useQueryClient()

  const institutionsQuery = useListInstitutions()
  const institutions = institutionsQuery.data?.data.data ?? []

  const createInstitutionMutation = useCreateInstitution()
  const createInstitutionAccountMutation = useCreateInstitutionAccount()
  const createAccountMutation = useCreateAccount()

  const showInterestRate = accountType === 'credit' || accountType === 'loan' || accountType === 'mortgage'
  const showCreditLimit = accountType === 'credit'

  function resetForm() {
    setName('')
    setAccountType('checking')
    setStatus(defaultStatus)
    setBalance('')
    setInterestRate('')
    setCreditLimit('')
    setInstitutionChoice('none')
    setExistingInstitutionId('')
    setNewInstitutionName('')
    setAdvancedOpen(false)
    setAmountSignConvention('positive_is_credit')
    setDedupWindowDays('3')
    setSettlementWindowDays('14')
    setAmountTolerancePct('0.5')
    setDateCol('date')
    setAmountCol('amount')
    setMerchantCol('description')
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
    if (institutionChoice === 'new' && !newInstitutionName.trim()) {
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
      if (institutionChoice === 'existing') {
        await createInstitutionAccountMutation.mutateAsync({
          id: existingInstitutionId,
          data: accountBody,
        })
      } else if (institutionChoice === 'new') {
        // Create the institution first, using defaults for the CSV-mapping fields
        // (the user can refine these during their first CSV import), then create
        // the account under the newly created institution.
        const newInstitutionBody: CreateInstitutionInputBody = {
          institution_name: newInstitutionName.trim(),
          source_type: 'csv',
          amount_col: amountCol.trim() || 'amount',
          date_col: dateCol.trim() || 'date',
          merchant_col: merchantCol.trim() || 'description',
          balance_col: null,
          debit_credit_col: null,
          imported_id_col: null,
          amount_sign_convention: amountSignConvention,
          dedup_window_days: Number(dedupWindowDays) || 3,
          settlement_window_days: Number(settlementWindowDays) || 14,
          amount_tolerance_pct: Number(amountTolerancePct) || 0.5,
        }
        const createdInstitution = await createInstitutionMutation.mutateAsync({ data: newInstitutionBody })
        const institutionId = createdInstitution.data.data.id
        await createInstitutionAccountMutation.mutateAsync({
          id: institutionId,
          data: accountBody,
        })
      } else {
        // institutionChoice === 'none' — standalone cash/manual account with no institution.
        const noInstitutionBody: CreateAccountInputBody = {
          ...accountBody,
          institution_id: null,
        }
        await createAccountMutation.mutateAsync({ data: noInstitutionBody })
      }

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
            onChange={(e) => setName(e.target.value)}
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
                onChange={() => setInstitutionChoice('existing')}
              />
              Link to existing institution
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--text)', cursor: 'pointer' }}>
              <input
                type="radio"
                name="institution-choice"
                value="new"
                checked={institutionChoice === 'new'}
                onChange={() => setInstitutionChoice('new')}
              />
              Create new institution
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, color: 'var(--text)', cursor: 'pointer' }}>
              <input
                type="radio"
                name="institution-choice"
                value="none"
                checked={institutionChoice === 'none'}
                onChange={() => setInstitutionChoice('none')}
              />
              No institution (cash / manual account)
            </label>
          </div>

          {/* Existing institution select */}
          {institutionChoice === 'existing' && (
            <div style={fieldWrapStyle}>
              <label style={labelStyle} htmlFor="existing-institution">Institution</label>
              <select
                id="existing-institution"
                value={existingInstitutionId}
                onChange={(e) => setExistingInstitutionId(e.target.value)}
                style={{ ...inputStyle, cursor: 'pointer' }}
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
            </div>
          )}

          {/* New institution fields */}
          {institutionChoice === 'new' && (
            <div>
              <div style={fieldWrapStyle}>
                <label style={labelStyle} htmlFor="new-institution-name">Institution name</label>
                <input
                  id="new-institution-name"
                  type="text"
                  value={newInstitutionName}
                  onChange={(e) => setNewInstitutionName(e.target.value)}
                  placeholder="e.g. Chase"
                  style={inputStyle}
                />
              </div>

              <Collapsible.Root open={advancedOpen} onOpenChange={setAdvancedOpen}>
                <Collapsible.Trigger asChild>
                  <button
                    type="button"
                    style={{
                      display: 'flex',
                      alignItems: 'center',
                      gap: 4,
                      background: 'none',
                      border: 'none',
                      cursor: 'pointer',
                      color: 'var(--text2)',
                      fontSize: 12,
                      padding: '4px 0',
                      marginBottom: 8,
                    }}
                  >
                    <ChevronRight
                      size={12}
                      style={{
                        transform: advancedOpen ? 'rotate(90deg)' : 'none',
                        transition: 'transform 0.1s',
                      }}
                    />
                    Advanced settings (optional)
                  </button>
                </Collapsible.Trigger>
                <Collapsible.Content>
                  <div
                    style={{
                      padding: 12,
                      background: 'var(--surface2)',
                      borderRadius: 4,
                      border: '1px solid var(--border)',
                      marginBottom: 8,
                    }}
                  >
                    <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 12 }}>
                      You can refine these when you upload your first CSV.
                    </div>

                    <div style={fieldWrapStyle}>
                      <label style={labelStyle} htmlFor="source-type">Source type</label>
                      <input
                        id="source-type"
                        type="text"
                        value="csv"
                        disabled
                        style={{ ...inputStyle, opacity: 0.6, cursor: 'not-allowed' }}
                      />
                    </div>

                    <div style={fieldWrapStyle}>
                      <label style={labelStyle} htmlFor="amount-sign-convention">Amount sign convention</label>
                      <select
                        id="amount-sign-convention"
                        value={amountSignConvention}
                        onChange={(e) => setAmountSignConvention(e.target.value)}
                        style={{ ...inputStyle, cursor: 'pointer' }}
                      >
                        <option value="positive_is_credit">Positive is credit</option>
                        <option value="positive_is_debit">Positive is debit</option>
                      </select>
                    </div>

                    <div style={{ display: 'flex', gap: 12, marginBottom: 14 }}>
                      <div style={{ flex: 1 }}>
                        <label style={labelStyle} htmlFor="dedup-window-days">Dedup window (days)</label>
                        <input
                          id="dedup-window-days"
                          type="number"
                          value={dedupWindowDays}
                          onChange={(e) => setDedupWindowDays(e.target.value)}
                          style={inputStyle}
                        />
                      </div>
                      <div style={{ flex: 1 }}>
                        <label style={labelStyle} htmlFor="settlement-window-days">Settlement window (days)</label>
                        <input
                          id="settlement-window-days"
                          type="number"
                          value={settlementWindowDays}
                          onChange={(e) => setSettlementWindowDays(e.target.value)}
                          style={inputStyle}
                        />
                      </div>
                    </div>

                    <div style={fieldWrapStyle}>
                      <label style={labelStyle} htmlFor="amount-tolerance-pct">Amount tolerance (%)</label>
                      <input
                        id="amount-tolerance-pct"
                        type="number"
                        step="0.1"
                        value={amountTolerancePct}
                        onChange={(e) => setAmountTolerancePct(e.target.value)}
                        style={inputStyle}
                      />
                    </div>

                    <div style={{ fontSize: 11, color: 'var(--text3)', marginBottom: 8 }}>
                      Column mapping — finalized during first CSV import
                    </div>

                    <div style={{ display: 'flex', gap: 8, marginBottom: 4 }}>
                      <div style={{ flex: 1 }}>
                        <label style={labelStyle} htmlFor="date-col">Date column</label>
                        <input
                          id="date-col"
                          type="text"
                          value={dateCol}
                          onChange={(e) => setDateCol(e.target.value)}
                          style={inputStyle}
                        />
                      </div>
                      <div style={{ flex: 1 }}>
                        <label style={labelStyle} htmlFor="amount-col">Amount column</label>
                        <input
                          id="amount-col"
                          type="text"
                          value={amountCol}
                          onChange={(e) => setAmountCol(e.target.value)}
                          style={inputStyle}
                        />
                      </div>
                      <div style={{ flex: 1 }}>
                        <label style={labelStyle} htmlFor="merchant-col">Merchant column</label>
                        <input
                          id="merchant-col"
                          type="text"
                          value={merchantCol}
                          onChange={(e) => setMerchantCol(e.target.value)}
                          style={inputStyle}
                        />
                      </div>
                    </div>
                  </div>
                </Collapsible.Content>
              </Collapsible.Root>
            </div>
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
            {pending ? 'Creating...' : 'Create account'}
          </button>
        </div>
      </form>
    </Modal>
  )
}
