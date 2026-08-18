# CSS Component System Design

**Date:** 2026-08-18
**Scope:** `services/web/static/css/` and all `.templ` files in `services/web/page/`

## Problem

The web service has ~671 inline `style=""` attributes spread across nine template files. Repeated patterns — buttons, form labels, pills, bordered cards, page layout scaffolding — are duplicated verbatim each time they appear. This makes the UI inconsistent (the same button has 3 slightly different `border-radius` values across files), slow to update (changing a button style requires hunting every occurrence), and hard to read (template intent is buried in style noise).

## Goal

Extract repeated UI patterns into named CSS component classes. Templates should express *what* an element is, not *how* it looks. One-off layout divs that don't match a pattern keep their inline styles — no invented classes for single-use styles.

## File Structure

Two CSS files, each with a clear responsibility:

| File | Owns |
|---|---|
| `services/web/static/css/app.css` | Design tokens, shell chrome (sidebar, topbar, nav), global element resets (html/body, inputs, scrollbars, dialogs, login page). Refactored to use CSS nesting for hover/active states. |
| `services/web/static/css/components.css` | All reusable UI component classes (buttons, labels, pills, cards, page layout). New file. |

`layout.templ` loads both files via `<link>` tags. All other templates are unaffected by the load change.

The existing `.btn-primary` class (currently in `app.css`, used only by the login form) moves to `components.css` alongside the new button system. `login.templ` is updated to `class="btn btn--primary"`.

## CSS Architecture

Both files use modern CSS nesting (`&` syntax) for variants and states. All modern browsers support this since 2023.

### `app.css` — Nesting Refactor (existing classes only)

Hover/active states that are currently written as separate selectors are collapsed into their parent block:

```css
/* Before */
.nav-item:hover { background: var(--surface2); color: var(--text); }
.nav-item--active { color: var(--text); background: var(--surface2); font-weight: 500; }

/* After */
.nav-item {
  /* base styles */
  &:hover { background: var(--surface2); color: var(--text); }
  &.nav-item--active { color: var(--text); background: var(--surface2); font-weight: 500; }
}
```

No classes are removed from `app.css` (except `.btn-primary` which moves to `components.css`). No behavior changes — this is a structural refactor of existing CSS.

### `components.css` — New Component Classes

#### Buttons

```css
.btn {
  border-radius: 6px;
  padding: 7px 18px;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
  font-family: inherit;
  border: none;
  line-height: 1;

  &.btn--primary { background: var(--accent); color: #fff; border: none; }
  &.btn--ghost   { background: transparent; border: 1px solid var(--border); color: var(--text2); }
  &.btn--danger  { background: transparent; border: 1px solid var(--commit); color: var(--commit); }
  &.btn--sm      { padding: 4px 12px; font-size: 12px; }

  &:hover   { filter: brightness(1.1); }
  &:disabled { opacity: 0.6; cursor: not-allowed; }
}
```

Usage: `class="btn btn--primary"`, `class="btn btn--ghost btn--sm"`.

The default `.btn` has no background — always pair with a variant. This matches the codebase's existing `background:var(--accent)` dominant pattern.

#### Form Field Label

The uppercase label pattern appears ~25× across templates:

```css
.field-label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: var(--text3);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  margin-bottom: 5px;
}
```

#### Pills

Role badges and entry tags:

```css
.pill {
  display: inline-block;
  padding: 3px 10px;
  border-radius: 16px;
  font-size: 12px;
  font-weight: 600;

  &.pill--accent { background: var(--accent); color: #fff; }
  &.pill--muted  { background: var(--surface2); color: var(--text2); border: 1px solid var(--border); }
  &.pill--sm     { padding: 1px 5px; font-size: 10px; border-radius: 10px; }
}
```

Usage: `class="pill pill--accent"` (Admin role), `class="pill pill--muted"` (Member role), `class="pill pill--sm pill--muted"` (small entry tags).

#### Cards

Bordered rounded containers (security sub-forms, configuration sections):

```css
.card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 16px;

  &.card--sm { padding: 12px; border-radius: 4px; }
}
```

#### Page Layout

The 3-part scaffolding used on every page content area:

```css
.page-layout {
  display: flex;
  flex-direction: column;
  height: 100%;
  overflow: hidden;
}

.page-body {
  flex: 1;
  overflow: auto;
  padding: 24px 20px;

  &.page-body--tight { padding: 16px 20px; }
}

.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 20px;
  border-bottom: 1px solid var(--border);
  flex-shrink: 0;
}
```

Usage in templates replaces the current `style="display:flex;flex-direction:column;height:100%;overflow:hidden"` outer wrapper and variants.

## Template Migration

Nine template files are updated. After any `.templ` edit, `templ generate` regenerates the corresponding `*_templ.go` file — no other build step is needed.

| File | Inline styles | Primary changes |
|---|---|---|
| `account.templ` | 130 | Buttons, labels, pills, page-layout |
| `configuration.templ` | 121 | Buttons, cards, labels, page-layout |
| `ledger.templ` | 122 | Buttons, pills, page-header, page-layout |
| `reports.templ` | 73 | Buttons, labels, page-layout |
| `budget.templ` | 79 | Buttons, labels, page-layout |
| `shell.templ` | 53 | Page-layout (already mostly classed) |
| `pages.templ` | 40 | Buttons, labels, pills, cards |
| `activity.templ` | 27 | Page-layout |
| `glossary.templ` | 26 | Buttons, page-layout |
| `login.templ` | 0 | `.btn-primary` → `class="btn btn--primary"` |

**What stays inline:** One-off `display:grid` column configs, per-page custom widths, unique margins, element-specific positioning. No class is introduced for a style that appears fewer than 3 times.

## What Does Not Change

- Design tokens in `:root {}` — unchanged
- All shell classes (`.sidebar`, `.app-topbar`, `.nav-item`, etc.) — structural refactor only, no visual change
- Global element auto-styles (`input[type="text"]`, `select`, `dialog`) — unchanged
- Go handler and store code — untouched
- API contracts — untouched
- Visual appearance of the application — this is a pure code quality improvement

## Verification

1. Start the dev server; navigate to every page — visual appearance must be identical to before
2. Confirm login form still renders and submits correctly (`.btn--primary` move)
3. Verify button hover states work (brightness filter)
4. Verify disabled button state (opacity + not-allowed cursor) where applicable
5. Check role pills on `/settings` render correctly (Admin = accent, Member = muted)
6. Check `templ generate` produces clean output with no errors across all modified files
7. `go build ./...` passes cleanly
