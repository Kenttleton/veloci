import type { InstitutionView } from '../../api/generated/velociAPI.schemas'

const rowStyle: React.CSSProperties = {
  display: 'flex',
  justifyContent: 'space-between',
  padding: '4px 0',
  fontSize: 12,
}

const labelStyle: React.CSSProperties = { color: 'var(--text3)' }
const valueStyle: React.CSSProperties = { color: 'var(--text)', fontWeight: 500 }

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div style={rowStyle}>
      <span style={labelStyle}>{label}</span>
      <span style={valueStyle}>{value}</span>
    </div>
  )
}

/** Read-only summary of an institution's saved mapping — shown wherever a user links to an existing institution, so they never have to rely on memory for what it contains. */
export function MappingPreview({ institution }: { institution: InstitutionView }) {
  return (
    <div
      style={{
        padding: 12,
        background: 'var(--surface2)',
        borderRadius: 4,
        border: '1px solid var(--border)',
      }}
    >
      <Row label="Date column" value={institution.date_col} />
      <Row label="Amount column" value={institution.amount_col} />
      <Row label="Merchant column" value={institution.merchant_col} />
      {institution.balance_col && <Row label="Balance column" value={institution.balance_col} />}
      {institution.debit_credit_col && <Row label="Debit/credit column" value={institution.debit_credit_col} />}
      {institution.imported_id_col && <Row label="Imported ID column" value={institution.imported_id_col} />}
      <Row
        label="Sign convention"
        value={institution.amount_sign_convention === 'positive_is_credit' ? 'Positive is credit' : 'Positive is debit'}
      />
      <Row label="Dedup window" value={`${institution.dedup_window_days} days`} />
      <Row label="Settlement window" value={`${institution.settlement_window_days} days`} />
      <Row label="Amount tolerance" value={`${institution.amount_tolerance_pct}%`} />
    </div>
  )
}
