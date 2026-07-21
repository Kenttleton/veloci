import { EditorView, keymap } from "@codemirror/view"
import { EditorState } from "@codemirror/state"
import { json } from "@codemirror/lang-json"
import { autocompletion, snippet } from "@codemirror/autocomplete"
import { linter, lintGutter } from "@codemirror/lint"
import { history, historyKeymap, defaultKeymap } from "@codemirror/commands"
import { defaultHighlightStyle, syntaxHighlighting } from "@codemirror/language"

// ── Config ─────────────────────────────────────────────────────────────────
// Override the palette trigger character in the browser console:
//   localStorage.setItem('veloci_editor_trigger_char', '/')
const TRIGGER_CHAR = localStorage.getItem("veloci_editor_trigger_char") || "@"

// ── Condition type palette ─────────────────────────────────────────────────
// Each entry has a display label and a CM6 snippet template.
//   ${}       — empty tabstop (cursor placed here after insertion)
//   ${name}   — named tabstop where "name" is pre-selected placeholder text
const CONDITION_TYPES = [
  { label: "Payee contains",       snip: '"payee_contains": "${}"'        },
  { label: "Payee is exact",       snip: '"payee_exact": "${}"'            },
  { label: "Payee starts with",    snip: '"payee_starts_with": "${}"'      },
  { label: "Payee ends with",      snip: '"payee_ends_with": "${}"'        },
  { label: "Payee not contains",   snip: '"payee_not_contains": "${}"'     },
  { label: "Payee regex",          snip: '"payee_regex": "${}"'            },
  { label: "Label matched",        snip: '"label_matched": "${}"'          },
  { label: "Account",              snip: '"account": "${}"'               },
  { label: "Direction",            snip: '"entry_direction": "${expense}"' },
  { label: "Entry type",           snip: '"entry_type": "${standing}"'     },
  { label: "And (both must match)",  snip: '"and": [${}]'                 },
  { label: "Or (either matches)",    snip: '"or": [${}]'                  },
  { label: "Not (must not match)",   snip: '"not": {${}}'                 },
]

// ── Constants ──────────────────────────────────────────────────────────────

// Keys whose values are payee strings — get payee autocomplete.
const PAYEE_KEYS = new Set([
  "payee_contains", "payee_exact", "payee_starts_with",
  "payee_ends_with", "payee_not_contains", "payee_regex",
])

// Human-readable display labels for the plain-language summary.
const KEY_LABELS = {
  payee_contains:     "payee contains",
  payee_exact:        "payee is exactly",
  payee_starts_with:  "payee starts with",
  payee_ends_with:    "payee ends with",
  payee_not_contains: "payee does not contain",
  payee_regex:        "payee matches regex",
  label_matched:      "label matched",
  account:            "account is",
  entry_direction:    "direction is",
  entry_type:         "type is",
}

// Valid condition keys — anything else triggers a linter warning.
const KNOWN_KEYS = new Set([
  ...PAYEE_KEYS, "label_matched", "account",
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

// Maps label name (lowercased) → { id, name } — used for autocomplete and summary links.
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

// Maps account name (lowercased) → { id, name } — used for autocomplete and summary links.
let _accountMap = null

async function fetchAccountMap() {
  if (_accountMap !== null) return _accountMap
  try {
    // SPEC GAP: No institution_id filter on /api/accounts; fetching all and filtering in-memory.
    const r = await fetch("/api/accounts?limit=500", { credentials: "same-origin" })
    const env = r.ok ? await r.json() : { data: [] }
    _accountMap = Object.fromEntries((env.data || []).map(a => [a.name.toLowerCase(), a]))
  } catch { _accountMap = {} }
  return _accountMap
}

// ── Command-palette completer ──────────────────────────────────────────────
// Fires when the cursor follows TRIGGER_CHAR (plus optional filter text).
// Replaces the trigger and any filter text with the chosen JSON snippet.

function commandPaletteCompleter(context) {
  // Escape TRIGGER_CHAR in case it's a regex metacharacter (e.g. '.', '+').
  const escaped = TRIGGER_CHAR.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")
  const before = context.matchBefore(new RegExp(`${escaped}[\\w\\s]*`))
  if (!before) return null

  const typed = before.text.slice(TRIGGER_CHAR.length).toLowerCase().trim()
  const options = CONDITION_TYPES
    .filter(ct => typed === "" || ct.label.toLowerCase().includes(typed))
    .map(ct => ({
      label: ct.label,
      type: "keyword",
      // Show a cleaned-up preview of the snippet as secondary detail text.
      detail: ct.snip.replace(/\$\{[^}]*\}/g, "…").replace(/"/g, ""),
      apply: snippet(ct.snip),
    }))

  if (!options.length) return null
  // filter: false — we handle filtering ourselves; prevent CM6 double-filtering.
  return { from: before.from, options, filter: false }
}

// ── Value completer ────────────────────────────────────────────────────────
// Fires when the cursor is inside a string value for a known condition key.
// Returns a Promise — CM6 autocompletion accepts async completers.

async function valueCompleter(context) {
  // Matches: "key": "typed-so-far  (closing quote not required — may be mid-type)
  const word = context.matchBefore(/"([^"]+)"\s*:\s*"([^"]*)/)
  if (!word) return null

  const keyMatch = word.text.match(/^"([^"]+)"\s*:\s*"([^"]*)$/)
  if (!keyMatch) return null
  const [, key, typed] = keyMatch

  // Position of the character just after the opening quote of the value.
  // Completions replace from here to the cursor (the typed portion only).
  const valueStart = word.from + word.text.lastIndexOf('"') + 1

  // Apply helper: inserts the label text and auto-appends closing quote if absent.
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

  return null
}

// ── Linter ─────────────────────────────────────────────────────────────────
// Validates condition keys and known enum values. Does not attempt to
// validate canonical merchants (removed in conditions refactor).

function conditionsLinter(view) {
  const text = view.state.doc.toString()
  if (!text.trim()) return []

  let parsed
  try { parsed = JSON.parse(text) } catch { return [] }
  // JSON syntax errors are handled by the lang-json linter; nothing to add here.

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
          message: `Unknown condition key "${key}". Type ${TRIGGER_CHAR} to open the condition palette.`,
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

      // Recurse into nested structures.
      if (key === "and" && Array.isArray(val)) val.forEach(checkObj)
      if (key === "or"  && Array.isArray(val)) val.forEach(checkObj)
      if (key === "not" && val && typeof val === "object") checkObj(val)
    }
  }

  checkObj(parsed)
  return diagnostics
}

// ── Plain-language summary ─────────────────────────────────────────────────
// Converts enriched conditions JSON to an HTML string.
//   label_matched values → <a href="/ledger?label=<uuid>"> links
//   account values       → <a href="/accounts/<uuid>"> links
// Async because it needs the label/account maps.

async function summaryHTML(conditions) {
  if (!conditions || typeof conditions !== "object" || Array.isArray(conditions)) {
    return ""
  }

  const labelMap   = await fetchLabelMap()
  const accountMap = await fetchAccountMap()

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
// Re-renders the plain-language summary div on document changes (debounced).
// Returns [] when summaryDiv is null so the caller can spread it safely.

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
// Auto-saves conditions to the API after 1.5 s of inactivity.
// Dispatches `veloci:conditions-saved` on success so the ledger page
// can mark itself dirty and prompt the user to re-run the engine.

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
          // Signal the ledger page that conditions changed → needs reprocessing.
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
  textarea._cmView = "pending"  // guard against double-init on rapid toggles

  const entryId = textarea.dataset.entryId

  // Save-state indicator: a sibling element with class js-conditions-status.
  const indicator = textarea.parentNode
    ? textarea.parentNode.querySelector(".js-conditions-status")
    : null

  // Plain-language summary div: found within the enclosing entry <details>.
  const entryDetails = textarea.closest(".js-entry-details")
  const summaryDiv = entryDetails ? entryDetails.querySelector(".js-conditions-summary") : null

  // Pre-warm the label cache so the first autocomplete invocation feels instant.
  fetchLabelMap()

  // Render the initial summary from the current textarea value.
  if (summaryDiv) {
    try {
      const parsed = JSON.parse(textarea.value)
      summaryHTML(parsed).then(html => {
        summaryDiv.innerHTML = html
          || '<span style="color:var(--text3);font-style:italic">No conditions — this entry will match all transactions.</span>'
      })
    } catch { /* textarea contains invalid JSON — leave summary empty */ }
  }

  const view = new EditorView({
    state: EditorState.create({
      doc: textarea.value,
      extensions: [
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap]),
        json(),
        syntaxHighlighting(defaultHighlightStyle),
        autocompletion({
          override: [commandPaletteCompleter, valueCompleter],
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
// Lazy init: spin up CM6 only when the entry <details> is opened.
// This avoids mounting N editor instances for N collapsed rows on page load.

document.addEventListener("toggle", (e) => {
  const details = e.target
  if (!details.open || !details.classList.contains("js-entry-details")) return
  const ta = details.querySelector(".js-conditions-ta")
  if (ta) initEditor(ta)
}, true)

// Also init any editors that are already open when the module loads
// (e.g. after a page reload with a previously-open entry, or after approve).
document.querySelectorAll("details.js-entry-details[open] .js-conditions-ta")
  .forEach(initEditor)
