import { useState } from 'react'
import * as Collapsible from '@radix-ui/react-collapsible'
import { ChevronRight } from 'lucide-react'
import { inputStyle, labelStyle, fieldWrapStyle } from '../shared/formStyles'
import type { MappingFormValues } from './mappingForm'

interface MappingFieldsProps {
  values: MappingFormValues
  onChange: (values: MappingFormValues) => void
  /** Real CSV header names to populate column-field dropdowns. Omit to render free-text inputs (e.g. no CSV available yet). */
  columnOptions?: string[]
  /** Whether the institution name field is editable. Defaults to true. */
  nameEditable?: boolean
  /** Whether the advanced settings section starts expanded. Defaults to false. */
  defaultAdvancedOpen?: boolean
}

function set<K extends keyof MappingFormValues>(
  values: MappingFormValues,
  onChange: (values: MappingFormValues) => void,
  key: K,
) {
  return (value: MappingFormValues[K]) => onChange({ ...values, [key]: value })
}

function ColumnField({
  id,
  label,
  value,
  onChange,
  options,
  required,
}: {
  id: string
  label: string
  value: string
  onChange: (v: string) => void
  options?: string[]
  required?: boolean
}) {
  return (
    <div style={fieldWrapStyle}>
      <label style={labelStyle} htmlFor={id}>{label}</label>
      {options ? (
        <select
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          style={{ ...inputStyle, cursor: 'pointer' }}
        >
          <option value="">{required ? 'Select a column' : 'None'}</option>
          {options.map((col) => (
            <option key={col} value={col}>{col}</option>
          ))}
        </select>
      ) : (
        <input
          id={id}
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          style={inputStyle}
        />
      )}
    </div>
  )
}

export function MappingFields({
  values,
  onChange,
  columnOptions,
  nameEditable = true,
  defaultAdvancedOpen = false,
}: MappingFieldsProps) {
  const [advancedOpen, setAdvancedOpen] = useState(defaultAdvancedOpen)

  return (
    <div>
      <div style={fieldWrapStyle}>
        <label style={labelStyle} htmlFor="institution-name">Institution name</label>
        <input
          id="institution-name"
          type="text"
          value={values.institutionName}
          onChange={(e) => set(values, onChange, 'institutionName')(e.target.value)}
          placeholder="e.g. Chase"
          disabled={!nameEditable}
          style={{ ...inputStyle, opacity: nameEditable ? 1 : 0.6, cursor: nameEditable ? 'text' : 'not-allowed' }}
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
              style={{ transform: advancedOpen ? 'rotate(90deg)' : 'none', transition: 'transform 0.1s' }}
            />
            {columnOptions ? 'Column mapping' : 'Advanced settings (optional)'}
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
            {!columnOptions && (
              <div style={{ fontSize: 12, color: 'var(--text2)', marginBottom: 12 }}>
                You can refine these when you upload your first CSV.
              </div>
            )}

            <div style={{ display: 'flex', gap: 8, marginBottom: 4 }}>
              <div style={{ flex: 1 }}>
                <ColumnField
                  id="date-col"
                  label="Date column"
                  value={values.dateCol}
                  onChange={set(values, onChange, 'dateCol')}
                  options={columnOptions}
                  required
                />
              </div>
              <div style={{ flex: 1 }}>
                <ColumnField
                  id="amount-col"
                  label="Amount column"
                  value={values.amountCol}
                  onChange={set(values, onChange, 'amountCol')}
                  options={columnOptions}
                  required
                />
              </div>
              <div style={{ flex: 1 }}>
                <ColumnField
                  id="merchant-col"
                  label="Merchant column"
                  value={values.merchantCol}
                  onChange={set(values, onChange, 'merchantCol')}
                  options={columnOptions}
                  required
                />
              </div>
            </div>

            <div style={{ display: 'flex', gap: 8, marginBottom: 4 }}>
              <div style={{ flex: 1 }}>
                <ColumnField
                  id="balance-col"
                  label="Balance column (optional)"
                  value={values.balanceCol}
                  onChange={set(values, onChange, 'balanceCol')}
                  options={columnOptions}
                />
              </div>
              <div style={{ flex: 1 }}>
                <ColumnField
                  id="debit-credit-col"
                  label="Debit/credit column (optional)"
                  value={values.debitCreditCol}
                  onChange={set(values, onChange, 'debitCreditCol')}
                  options={columnOptions}
                />
              </div>
              <div style={{ flex: 1 }}>
                <ColumnField
                  id="imported-id-col"
                  label="Imported ID column (optional)"
                  value={values.importedIdCol}
                  onChange={set(values, onChange, 'importedIdCol')}
                  options={columnOptions}
                />
              </div>
            </div>

            <div style={fieldWrapStyle}>
              <label style={labelStyle} htmlFor="amount-sign-convention">Amount sign convention</label>
              <select
                id="amount-sign-convention"
                value={values.amountSignConvention}
                onChange={(e) => set(values, onChange, 'amountSignConvention')(e.target.value)}
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
                  value={values.dedupWindowDays}
                  onChange={(e) => set(values, onChange, 'dedupWindowDays')(e.target.value)}
                  style={inputStyle}
                />
              </div>
              <div style={{ flex: 1 }}>
                <label style={labelStyle} htmlFor="settlement-window-days">Settlement window (days)</label>
                <input
                  id="settlement-window-days"
                  type="number"
                  value={values.settlementWindowDays}
                  onChange={(e) => set(values, onChange, 'settlementWindowDays')(e.target.value)}
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
                value={values.amountTolerancePct}
                onChange={(e) => set(values, onChange, 'amountTolerancePct')(e.target.value)}
                style={inputStyle}
              />
            </div>
          </div>
        </Collapsible.Content>
      </Collapsible.Root>
    </div>
  )
}
