import { EditorView, keymap } from "@codemirror/view"
import { EditorState } from "@codemirror/state"
import { json } from "@codemirror/lang-json"
import { autocompletion, snippet, closeBrackets, closeBracketsKeymap, startCompletion } from "@codemirror/autocomplete"
import { syntaxTree, HighlightStyle, syntaxHighlighting } from "@codemirror/language"
import { tags } from "@lezer/highlight"
import { linter, lintGutter } from "@codemirror/lint"
import { history, historyKeymap, defaultKeymap } from "@codemirror/commands"

// ── Constants ──────────────────────────────────────────────────────────────

const PAYEE_KEYS = new Set([
  "payee_contains", "payee_exact", "payee_starts_with",
  "payee_ends_with", "payee_not_contains", "payee_regex",
])

const KEY_LABELS = {
  and:                    "All of these",
  or:                     "Any of these",
  not:                    "None of these",
  payee_contains:         "Description contains",
  payee_exact:            "Description is exactly",
  payee_starts_with:      "Description starts with",
  payee_ends_with:        "Description ends with",
  payee_not_contains:     "Description does not contain",
  payee_regex:            "Description matches pattern",
  payee_one_of:           "Description is one of",
  amount_range:           "Amount is between",
  date_day_of_month:      "Day of month is",
  date_range:             "Date is between",
  account:                "From account",
  institution:            "From institution",
  label_matched:          "Tagged as",
  entry_direction:        "Direction is",
  entry_type:             "Type is",
  entry_period:           "Period is between",
  entry_projected_rate:   "Projected rate is between",
  entry_fitness:          "Fitness is",
  entry_recurrence_anchor: "Anchor is",
}

const KNOWN_KEYS = new Set([
  ...PAYEE_KEYS,
  "payee_one_of",
  "amount_range",
  "date_day_of_month",
  "date_range",
  "label_matched",
  "account",
  "institution",
  "entry_direction",
  "entry_type",
  "entry_period",
  "entry_fitness",
  "entry_projected_rate",
  "entry_recurrence_anchor",
  "and", "or", "not",
])

// ── Module-level caches (one fetch per page load) ──────────────────────────

async function searchMerchants(payee) {
  if (!payee) return []
  try {
    const r = await fetch("/api/transactions/merchant", {
      method: "QUERY",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ payee }),
    })
    return r.ok ? ((await r.json()).data || []) : []
  } catch { return [] }
}

let _labelMap = null

async function fetchLabelMap() {
  if (_labelMap !== null) return _labelMap
  try {
    const r = await fetch("/api/labels?limit=500", { credentials: "same-origin" })
    const env = r.ok ? await r.json() : { data: [] }
    _labelMap = Object.fromEntries((env.data || []).map(l => [l.name.toLowerCase(), l]))
  } catch { _labelMap = {} }
  return _labelMap
}

let _accountMap = null

async function fetchAccountMap() {
  if (_accountMap !== null) return _accountMap
  try {
    const r = await fetch("/api/accounts?limit=500", { credentials: "same-origin" })
    const env = r.ok ? await r.json() : { data: [] }
    _accountMap = Object.fromEntries((env.data || []).map(a => [a.name.toLowerCase(), a]))
  } catch { _accountMap = {} }
  return _accountMap
}

let _institutionMap = null

async function fetchInstitutionMap() {
  if (_institutionMap !== null) return _institutionMap
  try {
    const r = await fetch("/api/institutions", { credentials: "same-origin" })
    const env = r.ok ? await r.json() : { data: [] }
    _institutionMap = Object.fromEntries(
      (env.data || []).map(i => [i.institution_name.toLowerCase(), i])
    )
  } catch { _institutionMap = {} }
  return _institutionMap
}

// ── Context detection ──────────────────────────────────────────────────────
// Walks the CM6 JSON syntax tree to determine where the cursor sits.
//
// Returns one of:
//   { type: "key",   depth, parentKey, node }
//   { type: "value", key,              node }
//   null
//
// Lezer's error-tolerant JSON parser may not wrap a String in a Property node
// when there is no colon yet (incomplete property, user still typing the key).
// We handle that case by checking for String-directly-inside-Object and
// treating it as a key context.

function getJsonContext(state, pos) {
  const tree = syntaxTree(state)

  function objectDepth(n) {
    let d = 0
    while (n) { if (n.name === "Object") d++; n = n.parent }
    return d
  }

  function strContent(n) {
    const raw = state.sliceDoc(n.from, n.to)
    return raw.startsWith('"') ? raw.slice(1, raw.endsWith('"') ? -1 : undefined) : raw
  }

  function parentKey(containerObj) {
    let n = containerObj
    while (n) {
      if (n.name === "Array" && n.parent?.name === "Property") {
        const k = n.parent.firstChild
        if (k?.name === "String") return strContent(k)
        break
      }
      if (n.name === "Object" && n.parent?.name === "Property") {
        const k = n.parent.firstChild
        if (k?.name === "String") return strContent(k)
      }
      n = n.parent
    }
    return null
  }

  let cur = tree.resolveInner(pos, 0)

  while (cur) {
    if (cur.name === "String") {
      const parent = cur.parent
      if (!parent) break

      // ── Key inside a complete Property node ───────────────────────────
      if (parent.name === "Property") {
        const isKey = parent.firstChild?.from === cur.from
        if (isKey) {
          const containerObj = parent.parent
          const depth = objectDepth(containerObj)
          return { type: "key", depth, parentKey: parentKey(containerObj), node: cur }
        }
        // Value string — find the owning property key.
        const keyNode = parent.firstChild
        const key = keyNode?.name === "String" ? strContent(keyNode) : null
        return { type: "value", key, node: cur }
      }

      // ── String directly inside Object — incomplete property (no colon yet) ──
      // Lezer omits the Property wrapper in error recovery when only a key
      // string exists with no colon or value. Treat it as a key context.
      if (parent.name === "Object") {
        const depth = objectDepth(parent)
        return { type: "key", depth, parentKey: parentKey(parent), node: cur }
      }

      break
    }
    cur = cur.parent
  }

  return null
}

// ── Key completer ──────────────────────────────────────────────────────────

function contextKeyCompleter(context) {
  const ctx = getJsonContext(context.state, context.pos)
  if (!ctx || ctx.type !== "key") return null

  const node = ctx.node
  const typed = context.state.sliceDoc(node.from + 1, context.pos).toLowerCase()

  const logicOptions = [
    {
      label: "and",
      detail: "all conditions must match",
      type: "keyword",
      apply: snippet('"and": [\n  {${}}\n]'),
    },
    {
      label: "or",
      detail: "any condition must match",
      type: "keyword",
      apply: snippet('"or": [\n  {${}}\n]'),
    },
    {
      label: "not",
      detail: "must not match",
      type: "keyword",
      apply: snippet('"not": {${}}'),
    },
  ]

  const conditionOptions = [
    // Payee (transaction-target)
    { label: "payee_contains",     detail: "description contains text",            apply: snippet('"payee_contains": "${}"') },
    { label: "payee_exact",        detail: "description is exactly this",          apply: snippet('"payee_exact": "${}"') },
    { label: "payee_starts_with",  detail: "description starts with",              apply: snippet('"payee_starts_with": "${}"') },
    { label: "payee_ends_with",    detail: "description ends with",                apply: snippet('"payee_ends_with": "${}"') },
    { label: "payee_not_contains", detail: "description does not contain",         apply: snippet('"payee_not_contains": "${}"') },
    { label: "payee_regex",        detail: "description matches regex",            apply: snippet('"payee_regex": "${}"') },
    { label: "payee_one_of",       detail: "description is one of several values", apply: snippet('"payee_one_of": ["${}"]') },
    // Amount / date (transaction-target)
    { label: "amount_range",       detail: "amount between values (dollars)",      apply: snippet('"amount_range": {"min": ${-50}, "max": ${-10}}') },
    { label: "date_day_of_month",  detail: "day of month (1–28)",                  apply: snippet('"date_day_of_month": {"day": ${15}}') },
    { label: "date_range",         detail: "date is within a range",               apply: snippet('"date_range": {"start": "${2026-01-01}", "end": "${2026-12-31}"}') },
    // Account / institution (transaction-target)
    { label: "account",            detail: "from a specific account",              apply: snippet('"account": "${}"') },
    { label: "institution",        detail: "from a specific institution",          apply: snippet('"institution": "${}"') },
    // Entry-target leaves
    { label: "label_matched",      detail: "entry has this label",                 apply: snippet('"label_matched": "${}"') },
    { label: "entry_direction",    detail: "income or spend",                      apply: snippet('"entry_direction": "${spend}"') },
    { label: "entry_type",         detail: "standing, variable, or irregular",     apply: snippet('"entry_type": "${standing}"') },
    { label: "entry_period",       detail: "recurrence period in days",            apply: snippet('"entry_period": {"min_days": ${25}, "max_days": ${35}}') },
    { label: "entry_fitness",      detail: "fitness score gates",                  apply: snippet('"entry_fitness": {"overall": {"min": ${0.8}}}') },
    { label: "entry_projected_rate", detail: "projected rate (percent)",           apply: snippet('"entry_projected_rate": {"min": ${1.5}}') },
    { label: "entry_recurrence_anchor", detail: "anchor: dom:N, dow:N, interval:N", apply: snippet('"entry_recurrence_anchor": "${dom:15}"') },
  ]

  const ordered = ctx.depth <= 1
    ? [...logicOptions, ...conditionOptions]
    : [...conditionOptions, ...logicOptions]

  const options = typed
    ? ordered.filter(o => o.label.toLowerCase().includes(typed))
    : ordered

  if (!options.length) return null

  return { from: node.from, to: node.to, options, filter: false, validFor: /^"[^"]*/ }
}

// ── Value completer ────────────────────────────────────────────────────────

async function valueCompleter(context) {
  const ctx = getJsonContext(context.state, context.pos)
  if (!ctx || ctx.type !== "value" || !ctx.key) return null

  const node = ctx.node
  const valueStart = node.from + 1
  const typed = context.state.sliceDoc(valueStart, context.pos)
  const { key } = ctx

  function makeApply(label) {
    return (view, _c, from, to) => {
      const after = view.state.sliceDoc(to, to + 1)
      view.dispatch({
        changes: {
          from, to: after === '"' ? to + 1 : to,
          insert: label + (after === '"' ? "" : '"'),
        },
      })
    }
  }

  if (PAYEE_KEYS.has(key)) {
    const payees = await searchMerchants(typed)
    const options = payees.map(p => ({ label: p, type: "constant", apply: makeApply(p) }))
    return options.length ? { from: valueStart, options } : null
  }

  if (key === "entry_direction") {
    return {
      from: valueStart,
      options: ["income", "spend"].map(v => ({ label: v, type: "enum", apply: makeApply(v) })),
    }
  }

  if (key === "entry_type") {
    return {
      from: valueStart,
      options: ["standing", "variable", "irregular"].map(v => ({ label: v, type: "enum", apply: makeApply(v) })),
    }
  }

  if (key === "entry_recurrence_anchor") {
    const anchors = [
      { label: "dom:1",      detail: "1st of month" },
      { label: "dom:15",     detail: "15th of month" },
      { label: "dom:-1",     detail: "last day of month" },
      { label: "dom:-7",     detail: "7 days before month end" },
      { label: "dom:1,15",   detail: "1st and 15th (semi-monthly)" },
      { label: "dow:0",      detail: "every Monday" },
      { label: "dow:4",      detail: "every Friday" },
      { label: "interval:7",  detail: "every 7 days" },
      { label: "interval:14", detail: "every 14 days" },
      { label: "interval:30", detail: "every 30 days" },
    ]
    const options = anchors
      .filter(a => !typed || a.label.includes(typed))
      .map(a => ({ label: a.label, detail: a.detail, type: "enum", apply: makeApply(a.label) }))
    return options.length ? { from: valueStart, options } : null
  }

  if (key === "label_matched") {
    const lmap = await fetchLabelMap()
    const options = Object.values(lmap)
      .filter(l => l.name.toLowerCase().includes(typed.toLowerCase()))
      .slice(0, 20)
      .map(l => ({ label: l.name, type: "keyword", apply: makeApply(l.name) }))
    return options.length ? { from: valueStart, options } : null
  }

  if (key === "account") {
    const amap = await fetchAccountMap()
    const options = Object.values(amap)
      .filter(a => a.name.toLowerCase().includes(typed.toLowerCase()))
      .slice(0, 20)
      .map(a => ({ label: a.name, type: "keyword", apply: makeApply(a.name) }))
    return options.length ? { from: valueStart, options } : null
  }

  if (key === "institution") {
    const imap = await fetchInstitutionMap()
    const options = Object.values(imap)
      .filter(i => i.institution_name.toLowerCase().includes(typed.toLowerCase()))
      .slice(0, 20)
      .map(i => ({ label: i.institution_name, type: "keyword", apply: makeApply(i.institution_name) }))
    return options.length ? { from: valueStart, options } : null
  }

  return null
}

// ── Linter ─────────────────────────────────────────────────────────────────

function conditionsLinter(view) {
  const text = view.state.doc.toString()
  if (!text.trim()) return []

  let parsed
  try { parsed = JSON.parse(text) } catch { return [] }

  const diagnostics = []

  function findKeyRange(key) {
    const re = new RegExp(`"${key.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}"\\s*:`)
    const m = re.exec(text)
    return m ? { from: m.index, to: m.index + m[0].length } : null
  }

  function findValueRange(key, val) {
    const ek = key.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
    const re = new RegExp(`"${ek}"\\s*:\\s*"([^"]*)"`)
    const m = re.exec(text)
    if (!m) return null
    const start = m.index + m[0].indexOf(m[1])
    return { from: start, to: start + m[1].length }
  }

  function label(key) {
    return KEY_LABELS[key] || `"${key}"`
  }

  function checkObj(obj) {
    if (!obj || typeof obj !== "object" || Array.isArray(obj)) return
    for (const [key, val] of Object.entries(obj)) {
      if (!KNOWN_KEYS.has(key)) {
        const pos = findKeyRange(key)
        if (pos) diagnostics.push({
          ...pos,
          severity: "warning",
          message: `Unknown condition key "${key}". Type " inside an object to see suggestions.`,
        })
        continue
      }

      if (key === "entry_direction" && !["income", "spend"].includes(val)) {
        const pos = findValueRange(key, String(val))
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: `${label(key)} must be "income" or "spend".`,
        })
      }

      if (key === "entry_type" && !["standing", "variable", "irregular"].includes(val)) {
        const pos = findValueRange(key, String(val))
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: `${label(key)} must be "standing", "variable", or "irregular".`,
        })
      }

      if ((key === "and" || key === "or") && !Array.isArray(val)) {
        const pos = findKeyRange(key)
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: `${label(key)} must be an array of condition objects.`,
        })
      }

      if (PAYEE_KEYS.has(key) && typeof val === "string" && val === "") {
        const pos = findKeyRange(key)
        if (pos) diagnostics.push({
          ...pos,
          severity: "warning",
          message: `${label(key)} is empty — it will match every transaction.`,
        })
      }

      if (key === "payee_one_of" && (!Array.isArray(val) || val.length === 0)) {
        const pos = findKeyRange(key)
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: `${label(key)} must be a non-empty array of strings.`,
        })
      }

      if (key === "amount_range") {
        if (!val || typeof val !== "object" || Array.isArray(val) ||
            (val.min === undefined && val.max === undefined)) {
          const pos = findKeyRange(key)
          if (pos) diagnostics.push({
            ...pos,
            severity: "error",
            message: `${label(key)} must be an object with at least one of "min" or "max" (dollar values).`,
          })
        }
      }

      if (key === "date_day_of_month") {
        if (!val || typeof val !== "object" || Array.isArray(val) || val.day === undefined) {
          const pos = findKeyRange(key)
          if (pos) diagnostics.push({
            ...pos,
            severity: "error",
            message: `${label(key)} requires a "day" field (1–28).`,
          })
        }
      }

      if (key === "date_range") {
        if (!val || typeof val !== "object" || Array.isArray(val) ||
            val.start === undefined || val.end === undefined) {
          const pos = findKeyRange(key)
          if (pos) diagnostics.push({
            ...pos,
            severity: "error",
            message: `${label(key)} requires both "start" and "end" date strings (YYYY-MM-DD).`,
          })
        }
      }

      if (key === "entry_period") {
        if (!val || typeof val !== "object" || Array.isArray(val) ||
            (val.min_days === undefined && val.max_days === undefined)) {
          const pos = findKeyRange(key)
          if (pos) diagnostics.push({
            ...pos,
            severity: "error",
            message: `${label(key)} must be an object with at least one of "min_days" or "max_days".`,
          })
        }
      }

      if (key === "entry_fitness" && (!val || typeof val !== "object" || Array.isArray(val))) {
        const pos = findKeyRange(key)
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: `${label(key)} must be an object with score gates (e.g. {"overall": {"min": 0.8}}).`,
        })
      }

      if (key === "entry_projected_rate") {
        if (!val || typeof val !== "object" || Array.isArray(val) ||
            (val.min === undefined && val.max === undefined)) {
          const pos = findKeyRange(key)
          if (pos) diagnostics.push({
            ...pos,
            severity: "error",
            message: `${label(key)} must be an object with at least one of "min" or "max".`,
          })
        }
      }

      if (key === "entry_recurrence_anchor" && typeof val !== "string") {
        const pos = findKeyRange(key)
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: `${label(key)} must be a string (e.g. "dom:15", "dow:0", "interval:14").`,
        })
      }

      // Recurse into logical containers.
      if (key === "and" && Array.isArray(val)) val.forEach(checkObj)
      if (key === "or"  && Array.isArray(val)) val.forEach(checkObj)
      if (key === "not" && val && typeof val === "object") checkObj(val)
    }
  }

  checkObj(parsed)
  return diagnostics
}

// ── Plain-language summary ─────────────────────────────────────────────────

async function summaryHTML(conditions) {
  if (!conditions || typeof conditions !== "object" || Array.isArray(conditions)) {
    return ""
  }

  const labelMap   = await fetchLabelMap()
  const accountMap = await fetchAccountMap()
  await fetchInstitutionMap()

  const esc = s => String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")

  const AND_SEP = ` <span style="color:var(--text3);font-weight:600">AND</span> `
  const OR_SEP  = ` <span style="color:var(--text3);font-weight:600">OR</span> `
  const NOT_PFX = `<span style="color:var(--text3);font-weight:600">NOT</span> `

  function strong(s) { return `<strong style="color:var(--text)">${esc(s)}</strong>` }
  function accent(s) { return `<span style="color:var(--accent);font-weight:500">${esc(s)}</span>` }
  function link(href, text) {
    return `<a href="${href}" style="color:var(--accent);text-decoration:none;font-weight:500">${esc(text)}</a>`
  }

  function renderLeaf(key, val) {
    const lbl = KEY_LABELS[key] || key.replace(/_/g, " ")

    if (key === "label_matched") {
      const entry = labelMap[String(val).toLowerCase()]
      return `${lbl} ${entry ? link(`/ledger?label=${entry.id}`, val) : accent(val)}`
    }
    if (key === "account") {
      const entry = accountMap[String(val).toLowerCase()]
      return `${lbl} ${entry ? link(`/accounts/${entry.id}`, val) : accent(val)}`
    }
    if (key === "institution") {
      return `${lbl} ${accent(val)}`
    }
    if (PAYEE_KEYS.has(key)) {
      return `${lbl} ${strong(val)}`
    }
    if (key === "payee_one_of" && Array.isArray(val)) {
      return `${lbl} ${strong(val.join(", "))}`
    }
    if (key === "entry_direction" || key === "entry_type") {
      return `${lbl} ${strong(val)}`
    }
    if (key === "entry_recurrence_anchor") {
      return `${lbl} ${strong(val)}`
    }
    if (key === "amount_range" && val && typeof val === "object") {
      const parts = []
      if (val.min !== undefined) parts.push(`min $${val.min}`)
      if (val.max !== undefined) parts.push(`max $${val.max}`)
      return `${lbl} ${strong(parts.join(", "))}`
    }
    if (key === "date_day_of_month" && val && typeof val === "object") {
      let s = `day ${val.day}`
      if (val.tolerance_days) s += ` ±${val.tolerance_days}d`
      return `${lbl} ${strong(s)}`
    }
    if (key === "date_range" && val && typeof val === "object") {
      return `${lbl} ${strong(`${val.start ?? "?"} – ${val.end ?? "?"}`)}`
    }
    if (key === "entry_period" && val && typeof val === "object") {
      const parts = []
      if (val.min_days !== undefined) parts.push(`≥${val.min_days}d`)
      if (val.max_days !== undefined) parts.push(`≤${val.max_days}d`)
      return `${lbl} ${strong(parts.join(" "))}`
    }
    if (key === "entry_projected_rate" && val && typeof val === "object") {
      const parts = []
      if (val.min !== undefined) parts.push(`≥${val.min}%`)
      if (val.max !== undefined) parts.push(`≤${val.max}%`)
      return `${lbl} ${strong(parts.join(" "))}`
    }
    if (key === "entry_fitness" && val && typeof val === "object") {
      const gates = Object.entries(val).map(([k, v]) => {
        const bounds = []
        if (v?.min !== undefined) bounds.push(`≥${v.min}`)
        if (v?.max !== undefined) bounds.push(`≤${v.max}`)
        return `${k} ${bounds.join(" ")}`
      })
      return `${lbl} ${strong(gates.join(", "))}`
    }
    // Fallback for unknown / complex values.
    return `${lbl} ${strong(typeof val === "object" ? JSON.stringify(val) : val)}`
  }

  function renderObj(obj) {
    if (!obj || typeof obj !== "object" || Array.isArray(obj)) return ""
    const parts = []
    for (const [k, v] of Object.entries(obj)) {
      if (k === "and" && Array.isArray(v)) {
        const inner = v.map(renderObj).filter(Boolean).join(AND_SEP)
        if (inner) parts.push(`( ${inner} )`)
      } else if (k === "or" && Array.isArray(v)) {
        const inner = v.map(renderObj).filter(Boolean).join(OR_SEP)
        if (inner) parts.push(`( ${inner} )`)
      } else if (k === "not" && v && typeof v === "object") {
        const inner = renderObj(v)
        if (inner) parts.push(`${NOT_PFX}( ${inner} )`)
      } else {
        parts.push(renderLeaf(k, v))
      }
    }
    return parts.filter(Boolean).join(AND_SEP)
  }

  return renderObj(conditions)
}

// ── Summary updater ────────────────────────────────────────────────────────

function makeSummaryUpdater(summaryDiv) {
  if (!summaryDiv) return []
  const EMPTY_MSG = '<span style="color:var(--text3);font-style:italic">No conditions — this entry will match all transactions.</span>'
  let timer = null
  return EditorView.updateListener.of((update) => {
    if (!update.docChanged) return
    clearTimeout(timer)
    timer = setTimeout(async () => {
      const text = update.state.doc.toString()
      let parsed
      try { parsed = JSON.parse(text) } catch { return }
      const html = await summaryHTML(parsed)
      summaryDiv.innerHTML = html || EMPTY_MSG
    }, 300)
  })
}

// ── Save extension ─────────────────────────────────────────────────────────

function makeSaveExtension(entryId, indicator) {
  let timer = null
  return EditorView.updateListener.of((update) => {
    if (!update.docChanged) return
    clearTimeout(timer)

    const text = update.state.doc.toString()

    if (!text.trim()) {
      if (indicator) indicator.textContent = ""
      return
    }

    let parsed
    try {
      parsed = JSON.parse(text)
    } catch {
      if (indicator) indicator.textContent = "invalid JSON"
      return
    }

    if (indicator) indicator.textContent = "unsaved"
    timer = setTimeout(() => {
      if (indicator) indicator.textContent = "saving…"
      fetch(`/api/entries/${entryId}/conditions`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({ conditions: parsed }),
      }).then(r => {
        if (!indicator) return
        if (r.ok) {
          indicator.textContent = "saved"
          setTimeout(() => { indicator.textContent = "" }, 2000)
          document.dispatchEvent(new CustomEvent("veloci:conditions-saved", {
            detail: { entryId },
            bubbles: true,
          }))
        } else {
          indicator.textContent = "error"
        }
      }).catch(() => {
        if (indicator) indicator.textContent = "error"
      })
    }, 1500)
  })
}

// ── Autocomplete-after-close-quote trigger ────────────────────────────────
// When closeBrackets() inserts the matching `"`, it creates a synthetic
// transaction that may not re-fire activateOnTyping.  We detect the cursor
// landing between "" and call startCompletion() manually.

function makeAutocompleteTrigger() {
  return EditorView.updateListener.of((update) => {
    if (!update.docChanged) return
    // Only act on user input transactions.
    const isInput = update.transactions.some(
      tr => tr.isUserEvent("input") || tr.isUserEvent("delete")
    )
    if (!isInput) return
    const state = update.state
    const pos = state.selection.main.head
    if (state.sliceDoc(pos - 1, pos) === '"' && state.sliceDoc(pos, pos + 1) === '"') {
      startCompletion(update.view)
    }
  })
}

// ── Syntax highlighting ────────────────────────────────────────────────────

const velociHighlightStyle = HighlightStyle.define([
  { tag: tags.propertyName, color: "#7aa3e0", fontWeight: "600" },
  { tag: tags.string, color: "#7ecb9a" },
  { tag: tags.number, color: "#e8c06a" },
  { tag: tags.bool, color: "#c9a7eb" },
  { tag: tags.null, color: "#c9a7eb" },
  { tag: tags.punctuation, color: "#8a9bb8" },
  { tag: tags.bracket, color: "#8a9bb8" },
])

// ── Theme ──────────────────────────────────────────────────────────────────

const velociTheme = EditorView.theme({
  "&": {
    fontSize: "13px",
    fontFamily: "var(--font-mono, 'JetBrains Mono', 'Fira Code', monospace)",
    border: "1px solid var(--border)",
    borderRadius: "4px",
    background: "var(--bg-secondary, var(--bg))",
    minHeight: "80px",
  },
  "&.cm-focused": { outline: "2px solid var(--accent)", outlineOffset: "-1px" },
  ".cm-content": { padding: "8px", caretColor: "var(--text)" },
  ".cm-line": { lineHeight: "1.6" },
  ".cm-gutters": {
    background: "var(--bg-secondary, var(--bg))",
    borderRight: "1px solid var(--border)",
    color: "var(--text-muted)",
    minWidth: "20px",
  },
  ".cm-lintRange-warning": { backgroundImage: "none", borderBottom: "2px solid var(--commit)" },
  ".cm-lintRange-error":   { backgroundImage: "none", borderBottom: "2px solid var(--neg)" },
  // Lint tooltip contrast fix — CM6 defaults to light gray; match app surface.
  ".cm-tooltip.cm-tooltip-lint": {
    background: "var(--bg-secondary, var(--bg))",
    border: "1px solid var(--border)",
    color: "var(--text)",
    borderRadius: "4px",
    boxShadow: "0 4px 12px rgba(0,0,0,0.15)",
  },
  ".cm-tooltip.cm-tooltip-autocomplete": {
    background: "var(--bg)",
    border: "1px solid var(--border)",
    borderRadius: "4px",
    boxShadow: "0 4px 12px rgba(0,0,0,0.15)",
  },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li": { padding: "4px 8px" },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]": {
    background: "var(--accent)",
    color: "var(--bg)",
  },
  ".cm-diagnostic-warning": {
    borderLeft: "3px solid var(--commit)",
    padding: "2px 4px",
    marginLeft: "2px",
  },
  ".cm-diagnostic-error": {
    borderLeft: "3px solid var(--neg)",
    padding: "2px 4px",
    marginLeft: "2px",
  },
})

// ── Structure completer ────────────────────────────────────────────────────

function structureCompleter(context) {
  const andOr = context.matchBefore(/"(and|or)"\s*:\s*/)
  if (andOr) {
    return {
      from: context.pos,
      options: [{
        label: "[ … ]",
        detail: "array of condition objects",
        type: "keyword",
        apply: snippet("[\n  {${}}\n]"),
      }],
    }
  }

  const not = context.matchBefore(/"not"\s*:\s*/)
  if (not) {
    return {
      from: context.pos,
      options: [{
        label: "{ … }",
        detail: "single condition object",
        type: "keyword",
        apply: snippet("{${}}"),
      }],
    }
  }

  return null
}

// ── Init ───────────────────────────────────────────────────────────────────

function initEditor(textarea) {
  if (textarea._cmView) return
  textarea._cmView = "pending"

  const entryId = textarea.dataset.entryId
  const indicator = textarea.parentNode
    ? textarea.parentNode.querySelector(".js-conditions-status")
    : null
  const entryDetails = textarea.closest(".js-entry-details")
  const summaryDiv = entryDetails ? entryDetails.querySelector(".js-conditions-summary") : null

  fetchLabelMap()

  if (summaryDiv) {
    try {
      const parsed = JSON.parse(textarea.value)
      summaryHTML(parsed).then(html => {
        summaryDiv.innerHTML = html
          || '<span style="color:var(--text3);font-style:italic">No conditions — this entry will match all transactions.</span>'
      })
    } catch { /* invalid JSON — leave summary empty */ }
  }

  const view = new EditorView({
    state: EditorState.create({
      doc: textarea.value,
      extensions: [
        history(),
        closeBrackets(),
        keymap.of([...closeBracketsKeymap, ...defaultKeymap, ...historyKeymap]),
        json(),
        syntaxHighlighting(velociHighlightStyle),
        autocompletion({
          override: [contextKeyCompleter, valueCompleter, structureCompleter],
          activateOnTyping: true,
        }),
        lintGutter(),
        linter(conditionsLinter, { delay: 600 }),
        EditorView.lineWrapping,
        makeSaveExtension(entryId, indicator),
        makeSummaryUpdater(summaryDiv),
        makeAutocompleteTrigger(),
        velociTheme,
      ],
    }),
    parent: textarea.parentNode,
  })

  textarea._cmView = view
  textarea.parentNode.insertBefore(view.dom, textarea)
  textarea.style.display = "none"
}

// ── Mount ──────────────────────────────────────────────────────────────────

document.addEventListener("toggle", (e) => {
  const details = e.target
  if (!details.open || !details.classList.contains("js-entry-details")) return
  const ta = details.querySelector(".js-conditions-ta")
  if (ta) initEditor(ta)
}, true)

document.querySelectorAll("details.js-entry-details[open] .js-conditions-ta")
  .forEach(initEditor)
