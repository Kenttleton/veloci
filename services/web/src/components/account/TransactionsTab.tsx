import { useRef, useState, useEffect } from 'react'
import { format, parseISO } from 'date-fns'
import {
  useReactTable,
  getCoreRowModel,
  getSortedRowModel,
  getFilteredRowModel,
  flexRender,
  createColumnHelper,
} from '@tanstack/react-table'
import type { SortingState, ColumnFiltersState } from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { useListTransactionsInfinite } from '../../api/cursorQuery'
import type { TransactionView } from '../../api/generated/velociAPI.schemas'

const columnHelper = createColumnHelper<TransactionView>()

function formatAmount(cents: number): string {
  return (Math.abs(cents) / 100).toLocaleString('en-US', { style: 'currency', currency: 'USD' })
}


const columns = [
  columnHelper.accessor('date', {
    header: 'Date',
    size: 90,
    cell: (info) => format(parseISO(info.getValue()), 'MMM d'),
  }),
  columnHelper.accessor('merchant_normalized', {
    header: 'Merchant',
    cell: (info) => info.getValue(),
  }),
  columnHelper.accessor('imported_payee', {
    header: 'Raw Payee',
    cell: (info) => info.getValue(),
  }),
  columnHelper.accessor('amount_cents', {
    header: 'Amount',
    size: 100,
    cell: (info) => formatAmount(info.getValue()),
  }),
  columnHelper.accessor('settlement_status', {
    header: 'Status',
    size: 80,
    cell: (info) => info.getValue(),
  }),
]

interface TransactionsTabProps {
  accountId: string
}

export function TransactionsTab({ accountId: _accountId }: TransactionsTabProps) {
  const parentRef = useRef<HTMLDivElement>(null)
  const [sorting, setSorting] = useState<SortingState>([])
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])

  const { data, fetchNextPage, hasNextPage, isFetching } = useListTransactionsInfinite({
    limit: 50,
  })

  // Pages are AxiosResponse<EnvelopeListTransactionView>
  const rows = data?.pages.flatMap((p) => p.data.data ?? []) ?? []

  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, columnFilters },
    onSortingChange: setSorting,
    onColumnFiltersChange: setColumnFilters,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
  })

  const tableRows = table.getRowModel().rows

  const virtualizer = useVirtualizer({
    count: tableRows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: () => 36,
    overscan: 10,
  })

  const lastVirtualIndex = virtualizer.getVirtualItems().at(-1)?.index
  useEffect(() => {
    if (lastVirtualIndex !== undefined && lastVirtualIndex >= tableRows.length - 20 && hasNextPage && !isFetching) {
      void fetchNextPage()
    }
  }, [lastVirtualIndex, tableRows.length, hasNextPage, isFetching, fetchNextPage])

  if (!data && isFetching) {
    return <div style={{ padding: 20, color: 'var(--text3)' }}>Loading...</div>
  }

  if (rows.length === 0 && !isFetching) {
    return (
      <div style={{ padding: 32, textAlign: 'center', color: 'var(--text3)' }}>
        No transactions for this account.
      </div>
    )
  }

  return (
    <div>
      {/* Filter bar */}
      <div
        style={{
          display: 'flex',
          gap: 8,
          padding: '8px 16px',
          borderBottom: '1px solid var(--border)',
        }}
      >
        <input
          type="text"
          placeholder="Filter merchant..."
          onChange={(e) => table.getColumn('merchant_normalized')?.setFilterValue(e.target.value)}
          style={{
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text)',
            fontSize: 12,
            outline: 'none',
            width: 200,
          }}
        />
      </div>

      {/* Column headers */}
      <div style={{ borderBottom: '1px solid var(--border)', background: 'var(--bg)' }}>
        {table.getHeaderGroups().map((headerGroup) => (
          <div
            key={headerGroup.id}
            style={{ display: 'flex', padding: '5px 16px', gap: 8 }}
          >
            {headerGroup.headers.map((header) => (
              <div
                key={header.id}
                style={{
                  flex: header.column.getSize() ? `0 0 ${header.column.getSize()}px` : 1,
                  fontSize: 11,
                  color: 'var(--text3)',
                  textTransform: 'uppercase',
                  letterSpacing: '0.04em',
                  cursor: header.column.getCanSort() ? 'pointer' : 'default',
                  userSelect: 'none',
                }}
                onClick={header.column.getToggleSortingHandler()}
              >
                {flexRender(header.column.columnDef.header, header.getContext())}
                {header.column.getIsSorted() === 'asc'
                  ? ' ↑'
                  : header.column.getIsSorted() === 'desc'
                    ? ' ↓'
                    : ''}
              </div>
            ))}
          </div>
        ))}
      </div>

      {/* Virtual rows */}
      <div ref={parentRef} style={{ height: 480, overflowY: 'auto' }}>
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const row = tableRows[virtualRow.index]
            return (
              <div
                key={row.id}
                style={{
                  position: 'absolute',
                  top: virtualRow.start,
                  left: 0,
                  right: 0,
                  height: virtualRow.size,
                  display: 'flex',
                  alignItems: 'center',
                  padding: '0 16px',
                  gap: 8,
                  borderBottom: '1px solid var(--border)',
                  background: 'var(--surface)',
                }}
              >
                {row.getVisibleCells().map((cell) => (
                  <div
                    key={cell.id}
                    style={{
                      flex: cell.column.getSize() ? `0 0 ${cell.column.getSize()}px` : 1,
                      fontSize: 13,
                      color: 'var(--text)',
                      overflow: 'hidden',
                      textOverflow: 'ellipsis',
                      whiteSpace: 'nowrap',
                    }}
                  >
                    {flexRender(cell.column.columnDef.cell, cell.getContext())}
                  </div>
                ))}
              </div>
            )
          })}
        </div>
      </div>

      {isFetching && (
        <div style={{ padding: '8px 16px', color: 'var(--text3)', fontSize: 12 }}>
          Loading more...
        </div>
      )}
    </div>
  )
}
