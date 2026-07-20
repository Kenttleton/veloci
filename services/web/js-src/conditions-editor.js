import { EditorView, keymap } from "@codemirror/view"
import { EditorState } from "@codemirror/state"
import { json } from "@codemirror/lang-json"
import { autocompletion } from "@codemirror/autocomplete"
import { linter, lintGutter } from "@codemirror/lint"
import { history, historyKeymap, defaultKeymap } from "@codemirror/commands"
import { defaultHighlightStyle, syntaxHighlighting } from "@codemirror/language"

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

// ── Autocomplete ───────────────────────────────────────────────────────────

function conditionsCompleter(getKnown) {
  return (context) => {
    // Match the opening of a string value, e.g.: "merchant": "net<cursor>
    // We look for content after a colon+quote anywhere in the token
    const word = context.matchBefore(/"([^"]*)":\s*"([^"]*)/)
    if (!word) return null

    // Determine which key we're completing the value for
    const keyMatch = word.text.match(/^"([^"]+)"\s*:\s*"([^"]*)$/)
    if (!keyMatch) return null
    const [, key, typed] = keyMatch

    const known = getKnown()
    let options = []

    if (key === "merchant") {
      const matches = known.merchants.filter(m =>
        m.toLowerCase().includes(typed.toLowerCase())
      )
      if (matches.length === 0 && typed.length >= 2) {
        // No match — offer to keep typing with a hint
        options = [{ label: typed, detail: "⚠ new canonical merchant", type: "text" }]
      } else {
        options = matches.map(m => ({ label: m, type: "constant" }))
      }
    } else if (key === "label") {
      options = known.labels
        .filter(l => l.toLowerCase().includes(typed.toLowerCase()))
        .map(l => ({ label: l, type: "keyword" }))
    }

    if (options.length === 0) return null

    // The completion replaces from just after the opening quote of the value
    const valueStart = word.from + word.text.lastIndexOf('"') + 1
    return { from: valueStart, options }
  }
}

// ── Linter ─────────────────────────────────────────────────────────────────

function conditionsLinter(getKnown) {
  return (view) => {
    const text = view.state.doc.toString()
    const diagnostics = []

    let parsed
    try { parsed = JSON.parse(text) } catch { return [] }

    const merchants = getKnown().merchants
    // Find all "merchant": "..." occurrences in the source text
    const re = /"merchant"\s*:\s*"([^"]+)"/g
    let m
    while ((m = re.exec(text)) !== null) {
      const name = m[1]
      if (!merchants.includes(name)) {
        const valueStart = m.index + m[0].lastIndexOf('"' + name + '"')
        const valueEnd = valueStart + name.length + 2
        diagnostics.push({
          from: valueStart,
          to: valueEnd,
          severity: "warning",
          message: `"${name}" not found — will be created as a new canonical merchant on save`,
        })
      }
    }

    return diagnostics
  }
}

// ── Save ───────────────────────────────────────────────────────────────────

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
        indicator.textContent = r.ok ? "saved" : "error"
        if (r.ok) setTimeout(() => { indicator.textContent = "" }, 2000)
      })
    }, 1500)
  })
}

// ── Init ───────────────────────────────────────────────────────────────────

function initEditor(textarea) {
  if (textarea._cmView) return
  textarea._cmView = 'pending'  // prevent double-init before view is ready

  const entryId = textarea.dataset.entryId

  // Save-state indicator: look for a sibling element with js-conditions-status
  const indicator = textarea.parentNode
    ? textarea.parentNode.querySelector(".js-conditions-status")
    : null

  const known = { merchants: [], labels: [] }

  // Fetch autocomplete data once per editor
  fetch("/api/autocomplete?limit=500", { credentials: "same-origin" })
    .then(r => r.ok ? r.json() : null)
    .then(data => {
      if (!data) return
      known.merchants = data.merchants ?? []
      known.labels = data.labels ?? []
    })

  const getKnown = () => known

  const view = new EditorView({
    state: EditorState.create({
      doc: textarea.value,
      extensions: [
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap]),
        json(),
        syntaxHighlighting(defaultHighlightStyle),
        autocompletion({ override: [conditionsCompleter(getKnown)] }),
        lintGutter(),
        linter(conditionsLinter(getKnown), { delay: 600 }),
        EditorView.lineWrapping,
        makeSaveExtension(entryId, indicator),
        velociTheme,
      ],
    }),
    parent: textarea.parentNode,
  })

  textarea._cmView = view  // expose for external readers (e.g. approve handler)

  // Insert the editor right before the textarea and hide the textarea
  textarea.parentNode.insertBefore(view.dom, textarea)
  textarea.style.display = "none"
}

// ── Mount ──────────────────────────────────────────────────────────────────

// Lazy init: only initialize editors when their entry <details> is opened.
// Avoids spinning up CM instances for every collapsed row.
document.addEventListener("toggle", (e) => {
  const details = e.target
  if (!details.open || !details.classList.contains("js-entry-details")) return
  const ta = details.querySelector(".js-conditions-ta")
  if (ta) initEditor(ta)
}, true)

// Also catch any already-open details on page load (e.g. after approve/reject)
document.querySelectorAll("details.js-entry-details[open] .js-conditions-ta")
  .forEach(initEditor)
