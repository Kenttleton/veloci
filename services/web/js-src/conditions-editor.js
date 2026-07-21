import { EditorView, keymap } from "@codemirror/view"
import { EditorState } from "@codemirror/state"
import { json } from "@codemirror/lang-json"
import { autocompletion, snippet } from "@codemirror/autocomplete"
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
  payee_contains:     "payee contains",
  payee_exact:        "payee is exactly",
  payee_starts_with:  "payee starts with",
  payee_ends_with:    "payee ends with",
  payee_not_contains: "payee does not contain",
  payee_regex:        "payee matches regex",
  label_matched:      "label matched",
  account:            "account is",
  institution:        "institution is",
  entry_direction:    "direction is",
  entry_type:         "type is",
}

const KNOWN_KEYS = new Set([
  ...PAYEE_KEYS, "label_matched", "account", "institution",
  "entry_direction", "entry_type", "recurrence_anchor",
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
// The Lezer JSON parser is error-tolerant, so partial/unclosed strings
// still produce a usable tree — no special incomplete-JSON handling needed.
//
// Returns one of:
//   { type: "key",   depth, parentKey, node }
//   { type: "value", key,              node }
//   null

function getJsonContext(state, pos) {
  const tree = syntaxTree(state)

  function objectDepth(n) {
    let d = 0
    while (n) { if (n.name === "Object") d++; n = n.parent }
    return d
  }

  // Strip surrounding quotes; handles partial strings (no closing quote).
  function strContent(n) {
    const raw = state.sliceDoc(n.from, n.to)
    return raw.startsWith('"') ? raw.slice(1, raw.endsWith('"') ? -1 : undefined) : raw
  }

  // resolveInner(pos, -1) finds the node ending at or before pos — this
  // correctly lands inside a String node while the user is typing.
  let cur = tree.resolveInner(pos, -1)

  while (cur) {
    if (cur.name === "String") {
      const prop = cur.parent
      if (!prop || prop.name !== "Property") break

      const isKey = prop.firstChild?.from === cur.from

      if (isKey) {
        const containerObj = prop.parent // Object that owns this Property
        const depth = objectDepth(containerObj)

        // Walk up to find the nearest enclosing logic-combinator key, if any.
        // Handles arbitrary nesting: not→or, and→not→and, etc.
        let parentKey = null
        let n = containerObj
        while (n) {
          if (n.name === "Array" && n.parent?.name === "Property") {
            const k = n.parent.firstChild
            if (k?.name === "String") { parentKey = strContent(k); break }
          }
          if (n.name === "Object" && n.parent?.name === "Property") {
            const k = n.parent.firstChild
            if (k?.name === "String") { parentKey = strContent(k); break }
          }
          n = n.parent
        }

        return { type: "key", depth, parentKey, node: cur }
      }

      // Value string — find the owning property key.
      const keyNode = prop.firstChild
      const key = keyNode?.name === "String" ? strContent(keyNode) : null
      return { type: "value", key, node: cur }
    }
    cur = cur.parent
  }

  return null
}

// ── Key completer ──────────────────────────────────────────────────────────
// Fires inside a JSON object key string (the part before the colon).
// Ordering logic:
//   depth 1 (root): logic ops (and/or/not) first — root is almost always a combinator
//   depth 2+:       condition leaves first — nested objects are usually conditions
//   Both groups always present; typing filters across both.
//
// and/or/not can appear at any depth (e.g. {"not":{"or":[]}} is a NOR gate).

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
    { label: "payee_contains",     detail: "payee contains text",           apply: snippet('"payee_contains": "${}"') },
    { label: "payee_exact",        detail: "payee is exactly this",         apply: snippet('"payee_exact": "${}"') },
    { label: "payee_starts_with",  detail: "payee starts with",             apply: snippet('"payee_starts_with": "${}"') },
    { label: "payee_ends_with",    detail: "payee ends with",               apply: snippet('"payee_ends_with": "${}"') },
    { label: "payee_not_contains", detail: "payee does not contain",        apply: snippet('"payee_not_contains": "${}"') },
    { label: "payee_regex",        detail: "payee matches regex",           apply: snippet('"payee_regex": "${}"') },
    { label: "label_matched",      detail: "transaction has this label",    apply: snippet('"label_matched": "${}"') },
    { label: "account",            detail: "from this account",             apply: snippet('"account": "${}"') },
    { label: "institution",        detail: "from this institution",         apply: snippet('"institution": "${}"') },
    { label: "entry_direction",    detail: "income or expense",             apply: snippet('"entry_direction": "${expense}"') },
    { label: "entry_type",         detail: "standing, variable, irregular", apply: snippet('"entry_type": "${standing}"') },
    { label: "recurrence_anchor",  detail: "recurrence anchor date",        apply: snippet('"recurrence_anchor": "${}"') },
  ]

  // At the root (depth 1), logic combinators are the most common starting point.
  // Deeper objects are usually leaf conditions, but logic remains available.
  const ordered = ctx.depth <= 1
    ? [...logicOptions, ...conditionOptions]
    : [...conditionOptions, ...logicOptions]

  const options = typed
    ? ordered.filter(o => o.label.toLowerCase().includes(typed))
    : ordered

  if (!options.length) return null

  // validFor keeps the completion list active while the user continues typing
  // inside the opening-quoted key string (no closing quote yet).
  return { from: node.from, options, filter: false, validFor: /^"[^"]*$/ }
}

// ── Value completer ────────────────────────────────────────────────────────
// Fires inside a JSON string value, keyed by the owning property name.
// Async because payee search hits the API; label/account maps are cached.

async function valueCompleter(context) {
  const ctx = getJsonContext(context.state, context.pos)
  if (!ctx || ctx.type !== "value" || !ctx.key) return null

  const node = ctx.node
  const valueStart = node.from + 1 // character after the opening quote
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
      options: ["income", "expense"].map(v => ({ label: v, type: "enum", apply: makeApply(v) })),
    }
  }

  if (key === "entry_type") {
    return {
      from: valueStart,
      options: ["standing", "variable", "irregular"].map(v => ({ label: v, type: "enum", apply: makeApply(v) })),
    }
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
      }

      if (key === "entry_direction" && !["income", "expense"].includes(val)) {
        const pos = findValueRange(key, String(val))
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: 'entry_direction must be "income" or "expense".',
        })
      }

      if (key === "entry_type" && !["standing", "variable", "irregular"].includes(val)) {
        const pos = findValueRange(key, String(val))
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: 'entry_type must be "standing", "variable", or "irregular".',
        })
      }

      if ((key === "and" || key === "or") && !Array.isArray(val)) {
        const pos = findKeyRange(key)
        if (pos) diagnostics.push({
          ...pos,
          severity: "error",
          message: `"${key}" must be an array of condition objects, e.g. [{"payee_contains": "..."}].`,
        })
      }

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

  const labelMap       = await fetchLabelMap()
  const accountMap     = await fetchAccountMap()
  await fetchInstitutionMap() // warm cache; institution summary uses inline span, no map needed

  const esc = s => String(s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")

  const AND_SEP = ` <span style="color:var(--text3);font-weight:600">AND</span> `
  const OR_SEP  = ` <span style="color:var(--text3);font-weight:600">OR</span> `
  const NOT_PFX = `<span style="color:var(--text3);font-weight:600">NOT</span> `

  function renderValue(key, val) {
    if (key === "label_matched") {
      const entry = labelMap[String(val).toLowerCase()]
      if (entry) {
        return `<a href="/ledger?label=${entry.id}" style="color:var(--accent);text-decoration:none;font-weight:500">${esc(val)}</a>`
      }
      return `<span style="color:var(--accent);font-weight:500">${esc(val)}</span>`
    }
    if (key === "account") {
      const entry = accountMap[String(val).toLowerCase()]
      if (entry) {
        return `<a href="/accounts/${entry.id}" style="color:var(--accent);text-decoration:none;font-weight:500">${esc(val)}</a>`
      }
      return `<span style="color:var(--accent);font-weight:500">${esc(val)}</span>`
    }
    if (key === "institution") {
      return `<span style="color:var(--accent);font-weight:500">${esc(val)}</span>`
    }
    return `<strong style="color:var(--text)">${esc(val)}</strong>`
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
        const displayKey = KEY_LABELS[k] || k.replace(/_/g, " ")
        parts.push(`${displayKey} ${renderValue(k, v)}`)
      }
    }
    return parts.filter(Boolean).join(AND_SEP)
  }

  return renderObj(conditions)
}

// ── Summary updater extension ──────────────────────────────────────────────

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
    if (indicator) indicator.textContent = "unsaved"
    timer = setTimeout(() => {
      const text = update.state.doc.toString()
      let parsed
      try { parsed = JSON.parse(text) } catch { return }

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
      })
    }, 1500)
  })
}

// ── Syntax highlighting (dark-safe, WCAG AA on #1a2235) ───────────────────

const velociHighlightStyle = HighlightStyle.define([
  // JSON property keys — accent blue (#7aa3e0 ≈ 5.6:1)
  { tag: tags.propertyName, color: "#7aa3e0", fontWeight: "600" },
  // String values — soft green (#7ecb9a ≈ 6.1:1)
  { tag: tags.string, color: "#7ecb9a" },
  // Numbers — warm amber (#e8c06a ≈ 8.1:1)
  { tag: tags.number, color: "#e8c06a" },
  // Booleans (true / false) — lavender (#c9a7eb ≈ 5.4:1)
  { tag: tags.bool, color: "#c9a7eb" },
  // null keyword — same lavender family
  { tag: tags.null, color: "#c9a7eb" },
  // Punctuation: braces, brackets, colons, commas — muted text2 (#8a9bb8 ≈ 3.6:1)
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
        keymap.of([...defaultKeymap, ...historyKeymap]),
        json(),
        syntaxHighlighting(velociHighlightStyle),
        autocompletion({
          override: [contextKeyCompleter, valueCompleter],
          activateOnTyping: true,
        }),
        lintGutter(),
        linter(conditionsLinter, { delay: 600 }),
        EditorView.lineWrapping,
        makeSaveExtension(entryId, indicator),
        makeSummaryUpdater(summaryDiv),
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
