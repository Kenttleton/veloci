import { useState, useRef } from 'react'
import {
  useReactTable,
  getCoreRowModel,
  getSortedRowModel,
  getFilteredRowModel,
  getExpandedRowModel,
  flexRender,
  createColumnHelper,
  type SortingState,
  type ColumnFiltersState,
  type ExpandedState,
} from '@tanstack/react-table'
import { useVirtualizer } from '@tanstack/react-virtual'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { useListEntriesInfinite, useListTransactionsInfinite } from '../../api/cursorQuery'
import type { EntryView, TransactionView } from '../../api/generated/velociAPI.schemas'
import { LabelPill } from '../shared/LabelPill'

const columnHelper = createColumnHelper<EntryView>()

function formatRate(ratePerDay: number): string {
  const monthly = Math.abs(ratePerDay) * 30
  return '$' + (monthly / 100).toFixed(0) + '/mo'
}

const columns = [
  columnHelper.display({
    id: 'expander',
    size: 28,
    cell: ({ row }) => (
      <button
        onClick={(e) => {
          e.stopPropagation()
          row.getToggleExpandedHandler()()
        }}
        style={{
          background: 'none',
          border: 'none',
          cursor: 'pointer',
          padding: 0,
          color: 'var(--text3)',
          display: 'flex',
          alignItems: 'center',
        }}
      >
        {row.getIsExpanded() ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
      </button>
    ),
  }),
  columnHelper.accessor('name', {
    header: 'Entry',
    cell: (info) => (
      <span style={{ fontWeight: 600, fontSize: 13 }}>{info.getValue()}</span>
    ),
  }),
  columnHelper.accessor('entry_type', {
    header: 'Type',
    size: 90,
    cell: (info) => info.getValue(),
  }),
  columnHelper.accessor('direction', {
    header: 'Dir',
    size: 70,
    cell: (info) => (
      <span
        style={{
          color:
            info.getValue() === 'income' ? 'var(--income)' : 'var(--commit)',
        }}
      >
        {info.getValue()}
      </span>
    ),
  }),
  columnHelper.accessor('actual_rate', {
    header: 'Rate/day',
    size: 90,
    cell: (info) => formatRate(info.getValue()),
  }),
  columnHelper.accessor('label_name', {
    header: 'Label',
    size: 100,
    cell: (info) => {
      const val = info.getValue()
      return val ? <LabelPill name={val} /> : null
    },
  }),
  columnHelper.accessor('status', {
    header: 'Status',
    size: 80,
    cell: (info) => info.getValue(),
  }),
]

function TransactionSubTable({ entryId: _entryId }: { entryId: string }) {
  // entryId param kept for when the API supports filtering by entry_id
  const { data, fetchNextPage, hasNextPage, isFetching } = useListTransactionsInfinite({
    limit: 25,
  })

  const txRows = data?.pages.flatMap((p) => p.data.data ?? []) ?? []

  if (!data && isFetching) {
    return (
      <div style={{ padding: '8px 28px', color: 'var(--text3)', fontSize: 12 }}>
        Loading transactions...
      </div>
    )
  }

  if (txRows.length === 0 && !isFetching) {
    return (
      <div style={{ padding: '8px 28px', color: 'var(--text3)', fontSize: 12 }}>
        No matched transactions.
      </div>
    )
  }

  return (
    <div
      style={{
        background: 'var(--bg)',
        borderTop: '1px solid var(--border)',
        borderBottom: '1px solid var(--border)',
        maxHeight: 122,
        overflowY: 'auto',
      }}
    >
      {/* Sub-table header */}
      <div
        style={{
          display: 'flex',
          gap: 8,
          padding: '4px 28px',
          borderBottom: '1px solid var(--border)',
        }}
      >
        {(['Date', 'Merchant', 'Amount'] as const).map((h) => (
          <div
            key={h}
            style={{
              flex: h === 'Amount' ? '0 0 90px' : h === 'Date' ? '0 0 80px' : 1,
              fontSize: 10,
              color: 'var(--text3)',
              textTransform: 'uppercase',
              letterSpacing: '0.04em',
            }}
          >
            {h}
          </div>
        ))}
      </div>

      {txRows.map((tx: TransactionView) => (
        <div
          key={tx.id}
          style={{
            display: 'flex',
            gap: 8,
            padding: '5px 28px',
            borderBottom: '1px solid var(--border)',
            alignItems: 'center',
          }}
        >
          <div style={{ flex: '0 0 80px', fontSize: 12, color: 'var(--text2)' }}>
            {new Date(tx.date).toLocaleDateString('en-US', {
              month: 'short',
              day: 'numeric',
            })}
          </div>
          <div
            style={{
              flex: 1,
              fontSize: 12,
              color: 'var(--text)',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
              whiteSpace: 'nowrap',
            }}
          >
            {tx.merchant_normalized}
          </div>
          <div
            style={{
              flex: '0 0 90px',
              fontSize: 12,
              color: 'var(--text)',
              textAlign: 'right',
            }}
          >
            {(Math.abs(tx.amount_cents) / 100).toLocaleString('en-US', {
              style: 'currency',
              currency: 'USD',
            })}
          </div>
        </div>
      ))}

      {hasNextPage && (
        <button
          onClick={() => void fetchNextPage()}
          disabled={isFetching}
          style={{
            background: 'none',
            border: 'none',
            cursor: 'pointer',
            padding: '6px 28px',
            color: 'var(--accent)',
            fontSize: 12,
          }}
        >
          {isFetching ? 'Loading...' : 'Load more'}
        </button>
      )}
    </div>
  )
}

interface EntriesTableProps {
  accountId?: string
}

// TODO(task-8+): pass accountId to useListEntriesInfinite when API supports filtering
export function EntriesTable({ accountId: _accountId }: EntriesTableProps) {
  const parentRef = useRef<HTMLDivElement>(null)
  const [sorting, setSorting] = useState<SortingState>([])
  const [columnFilters, setColumnFilters] = useState<ColumnFiltersState>([])
  const [expanded, setExpanded] = useState<ExpandedState>({})

  const { data, fetchNextPage, hasNextPage, isFetching } = useListEntriesInfinite({ limit: 50 })

  const rows = data?.pages.flatMap((p) => p.data.data ?? []) ?? []

  const table = useReactTable({
    data: rows,
    columns,
    state: { sorting, columnFilters, expanded },
    onSortingChange: setSorting,
    onColumnFiltersChange: setColumnFilters,
    onExpandedChange: setExpanded,
    getCoreRowModel: getCoreRowModel(),
    getSortedRowModel: getSortedRowModel(),
    getFilteredRowModel: getFilteredRowModel(),
    getExpandedRowModel: getExpandedRowModel(),
    getRowCanExpand: () => true,
  })

  const tableRows = table.getRowModel().rows

  const virtualizer = useVirtualizer({
    count: tableRows.length,
    getScrollElement: () => parentRef.current,
    estimateSize: (i) => (tableRows[i]?.getIsExpanded() ? 160 : 38),
    overscan: 5,
    onChange: (instance) => {
      const lastItem = instance.getVirtualItems().at(-1)
      if (
        lastItem &&
        lastItem.index >= tableRows.length - 10 &&
        hasNextPage &&
        !isFetching
      ) {
        void fetchNextPage()
      }
    },
  })

  if (!data && isFetching) {
    return <div style={{ padding: 20, color: 'var(--text3)' }}>Loading...</div>
  }

  if (rows.length === 0 && !isFetching) {
    return (
      <div style={{ padding: 32, textAlign: 'center', color: 'var(--text3)' }}>
        No entries found.
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
        <select
          onChange={(e) =>
            table.getColumn('direction')?.setFilterValue(e.target.value || undefined)
          }
          style={{
            background: 'var(--surface2)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text2)',
            fontSize: 12,
          }}
        >
          <option value="">All directions</option>
          <option value="income">Income</option>
          <option value="expense">Expense</option>
        </select>
        <select
          onChange={(e) =>
            table.getColumn('entry_type')?.setFilterValue(e.target.value || undefined)
          }
          style={{
            background: 'var(--surface2)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text2)',
            fontSize: 12,
          }}
        >
          <option value="">All types</option>
          <option value="standing">Standing</option>
          <option value="variable">Variable</option>
          <option value="irregular">Irregular</option>
        </select>
        <select
          onChange={(e) =>
            table.getColumn('status')?.setFilterValue(e.target.value || undefined)
          }
          style={{
            background: 'var(--surface2)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            color: 'var(--text2)',
            fontSize: 12,
          }}
        >
          <option value="">All statuses</option>
          <option value="active">Active</option>
          <option value="inactive">Inactive</option>
          <option value="pending_review">Pending review</option>
        </select>
      </div>

      {/* Column headers */}
      <div style={{ borderBottom: '1px solid var(--border)', background: 'var(--bg)' }}>
        {table.getHeaderGroups().map((headerGroup) => (
          <div
            key={headerGroup.id}
            style={{
              display: 'flex',
              padding: '5px 16px',
              gap: 8,
              alignItems: 'center',
            }}
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

      {/* Virtualised rows */}
      <div ref={parentRef} style={{ height: 480, overflowY: 'auto' }}>
        <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
          {virtualizer.getVirtualItems().map((virtualRow) => {
            const row = tableRows[virtualRow.index]
            const isExpanded = row.getIsExpanded()
            return (
              <div
                key={row.id}
                style={{
                  position: 'absolute',
                  top: virtualRow.start,
                  left: 0,
                  right: 0,
                }}
              >
                {/* Entry row */}
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    padding: '0 16px',
                    height: 38,
                    gap: 8,
                    borderBottom: isExpanded ? 'none' : '1px solid var(--border)',
                    background: isExpanded ? 'var(--surface2)' : 'var(--surface)',
                    cursor: 'pointer',
                  }}
                  onClick={row.getToggleExpandedHandler()}
                >
                  {row.getVisibleCells().map((cell) => (
                    <div
                      key={cell.id}
                      style={{
                        flex: cell.column.getSize()
                          ? `0 0 ${cell.column.getSize()}px`
                          : 1,
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

                {/* Expanded transaction sub-table */}
                {isExpanded && <TransactionSubTable entryId={row.original.id} />}
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
