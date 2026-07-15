import type { InstitutionView } from '../../api/generated/velociAPI.schemas'

export interface MappingFormValues {
  institutionName: string
  amountSignConvention: string
  dedupWindowDays: string
  settlementWindowDays: string
  amountTolerancePct: string
  dateCol: string
  amountCol: string
  merchantCol: string
  balanceCol: string
  debitCreditCol: string
  importedIdCol: string
}

export const DEFAULT_MAPPING_VALUES: MappingFormValues = {
  institutionName: '',
  amountSignConvention: 'positive_is_credit',
  dedupWindowDays: '3',
  settlementWindowDays: '14',
  amountTolerancePct: '0.5',
  dateCol: 'date',
  amountCol: 'amount',
  merchantCol: 'description',
  balanceCol: '',
  debitCreditCol: '',
  importedIdCol: '',
}

/** Builds form values pre-filled from an existing institution's saved mapping. */
export function mappingValuesFromInstitution(institution: InstitutionView): MappingFormValues {
  return {
    institutionName: institution.institution_name,
    amountSignConvention: institution.amount_sign_convention,
    dedupWindowDays: String(institution.dedup_window_days),
    settlementWindowDays: String(institution.settlement_window_days),
    amountTolerancePct: String(institution.amount_tolerance_pct),
    dateCol: institution.date_col,
    amountCol: institution.amount_col,
    merchantCol: institution.merchant_col,
    balanceCol: institution.balance_col ?? '',
    debitCreditCol: institution.debit_credit_col ?? '',
    importedIdCol: institution.imported_id_col ?? '',
  }
}

/** Converts form state into the API request body shape shared by create/update institution. */
export function mappingValuesToRequestBody(values: MappingFormValues) {
  return {
    institution_name: values.institutionName.trim(),
    source_type: 'csv',
    amount_col: values.amountCol.trim() || 'amount',
    date_col: values.dateCol.trim() || 'date',
    merchant_col: values.merchantCol.trim() || 'description',
    balance_col: values.balanceCol.trim() || null,
    debit_credit_col: values.debitCreditCol.trim() || null,
    imported_id_col: values.importedIdCol.trim() || null,
    amount_sign_convention: values.amountSignConvention,
    dedup_window_days: Number(values.dedupWindowDays) || 3,
    settlement_window_days: Number(values.settlementWindowDays) || 14,
    amount_tolerance_pct: Number(values.amountTolerancePct) || 0.5,
  }
}
