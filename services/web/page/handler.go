package page

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/authclient"
	"github.com/veloci/veloci/fieldregistry"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/store"
)

const sessionCookie = "veloci_session"

// ShellData is passed to every protected page template.
type ShellData struct {
	User            ShellUser
	ActiveAccounts  []ShellAccount
	PassiveAccounts []ShellAccount
	CurrentPath     string
	HasRunningJobs  bool
}

// ShellUser holds the current user's display info.
type ShellUser struct {
	Name  string
	Email string
}

// ShellAccount is a simplified account for the sidebar.
type ShellAccount struct {
	ID           string
	Name         string
	AccountType  string
	Status       string
	BalanceCents *int64
}

// FormatBalance converts cents to a USD currency string.
func FormatBalance(cents *int64) string {
	if cents == nil {
		return "—"
	}
	negative := *cents < 0
	abs := *cents
	if negative {
		abs = -*cents
	}
	dollars := abs / 100
	rem := abs % 100
	s := commaInt(dollars) + "." + fmt.Sprintf("%02d", rem)
	if negative {
		return "-$" + s
	}
	return "$" + s
}

func commaInt(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	start := len(s) % 3
	if start == 0 {
		start = 3
	}
	b.WriteString(s[:start])
	for i := start; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// Initials extracts 1-2 uppercase initials from a name or falls back to email prefix.
func Initials(name, email string) string {
	if name != "" {
		parts := strings.Fields(name)
		if len(parts) >= 2 {
			return strings.ToUpper(string([]rune(parts[0])[0:1]) + string([]rune(parts[len(parts)-1])[0:1]))
		}
		runes := []rune(name)
		if len(runes) >= 2 {
			return strings.ToUpper(string(runes[:2]))
		}
		return strings.ToUpper(string(runes[:1]))
	}
	runes := []rune(email)
	if len(runes) >= 2 {
		return strings.ToUpper(string(runes[:2]))
	}
	return "?"
}

// IsNegativeBalance returns true for credit/loan/mortgage accounts with a negative balance.
func IsNegativeBalance(a ShellAccount) bool {
	if a.BalanceCents == nil || *a.BalanceCents >= 0 {
		return false
	}
	return a.AccountType == "credit" || a.AccountType == "loan" || a.AccountType == "mortgage"
}

// isActivePath returns true when currentPath matches or is a child of href.
func isActivePath(currentPath, href string) bool {
	if href == "/" {
		return currentPath == "/"
	}
	return currentPath == href || strings.HasPrefix(currentPath, href+"/")
}

// ledgerIconStyle adds a running-job outline when jobs are active.
func ledgerIconStyle(hasRunning bool) string {
	if hasRunning {
		return "opacity:0.55;outline:1px solid var(--accent);border-radius:2px;display:flex"
	}
	return "display:flex"
}

// Server handles server-rendered page requests.
type Server struct {
	store *store.Store
	auth  *authclient.Client
	pool  *pgxpool.Pool
}

// NewServer creates a page Server.
func NewServer(s *store.Store, auth *authclient.Client, pool *pgxpool.Pool) *Server {
	return &Server{store: s, auth: auth, pool: pool}
}

func (s *Server) render(c echo.Context, comp templ.Component) error {
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return comp.Render(c.Request().Context(), c.Response())
}

func (s *Server) buildShellData(r *http.Request) ShellData {
	ctx := r.Context()
	entityID := middleware.EntityID(ctx)
	email := middleware.Email(ctx)

	accounts, _ := s.store.ListAccounts(ctx, entityID, 200, "")

	var active, passive []ShellAccount
	for _, a := range accounts {
		sa := ShellAccount{
			ID:           a.ID,
			Name:         a.Name,
			AccountType:  a.AccountType,
			Status:       a.Status,
			BalanceCents: a.BalanceCents,
		}
		if a.Status == "active" {
			active = append(active, sa)
		} else {
			passive = append(passive, sa)
		}
	}

	return ShellData{
		User:            ShellUser{Email: email},
		ActiveAccounts:  active,
		PassiveAccounts: passive,
		CurrentPath:     r.URL.Path,
		HasRunningJobs:  false, // TODO: Datastar SSE will drive this signal
	}
}

// GetLogin renders the login form.
func (s *Server) GetLogin(c echo.Context) error {
	return s.render(c, Login("", "", c.QueryParam("next")))
}

// PostLogin handles login form submission.
func (s *Server) PostLogin(c echo.Context) error {
	if err := c.Request().ParseForm(); err != nil {
		return s.render(c, Login("Invalid request", "", ""))
	}
	email := c.FormValue("email")
	password := c.FormValue("password")
	next := c.FormValue("next")
	// Only allow relative paths to prevent open-redirect attacks.
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	ctx := c.Request().Context()

	cred, err := s.auth.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
		Email:    email,
		Password: password,
	})
	if err != nil {
		return s.render(c, Login("Invalid email or password", email, next))
	}

	var userID, entityID, entityRole string
	err = s.pool.QueryRow(ctx, `
		SELECT u.id::text, eu.entity_id::text, eu.entity_role
		FROM users u
		JOIN entity_users eu ON eu.user_id = u.id
		WHERE u.email = $1
		LIMIT 1
	`, email).Scan(&userID, &entityID, &entityRole)
	if err != nil {
		return s.render(c, Login("Invalid email or password", email, next))
	}

	claims := make(authclient.MintTokenInputBodyClaims)
	for k, v := range map[string]any{
		"sub":         userID,
		"email":       email,
		"system_role": string(cred.SystemRole),
		"entity_id":   entityID,
		"entity_role": entityRole,
	} {
		b, _ := json.Marshal(v)
		claims[k] = b
	}

	minted, err := s.auth.MintToken(ctx, &authclient.MintTokenInputBody{
		CredentialID: cred.CredentialID,
		Claims:       claims,
	})
	if err != nil {
		return s.render(c, Login("Login failed, please try again", email, next))
	}

	expiry, _ := time.Parse(time.RFC3339, minted.ExpiresAt)
	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Value:    minted.AccessToken,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	c.SetCookie(&http.Cookie{
		Name:     "veloci_refresh",
		Value:    minted.RefreshToken,
		Path:     "/api/session/refresh",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return c.Redirect(http.StatusFound, next)
}

// PostLogout revokes the session token and clears the cookie.
func (s *Server) PostLogout(c echo.Context) error {
	if jti := middleware.JTI(c.Request().Context()); jti != "" {
		s.auth.RevokeToken(c.Request().Context(), authclient.RevokeTokenParams{Jti: jti}) //nolint:errcheck
	}
	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	c.SetCookie(&http.Cookie{
		Name:     "veloci_refresh",
		Value:    "",
		Path:     "/api/session/refresh",
		HttpOnly: true,
		MaxAge:   -1,
	})
	return c.Redirect(http.StatusFound, "/login")
}

// PostSessionRefresh uses the veloci_refresh cookie to issue a new token pair.
// Not behind auth middleware — the access token may be expired when this is called.
func (s *Server) PostSessionRefresh(c echo.Context) error {
	cookie, err := c.Cookie("veloci_refresh")
	if err != nil || cookie.Value == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
	}
	minted, err := s.auth.RefreshToken(c.Request().Context(), &authclient.RefreshTokenInputBody{
		RefreshToken: cookie.Value,
	})
	if err != nil {
		c.SetCookie(&http.Cookie{Name: "veloci_refresh", Value: "", Path: "/api/session/refresh", MaxAge: -1})
		return echo.NewHTTPError(http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
	}
	expiry, _ := time.Parse(time.RFC3339, minted.ExpiresAt)
	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Value:    minted.AccessToken,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	c.SetCookie(&http.Cookie{
		Name:     "veloci_refresh",
		Value:    minted.RefreshToken,
		Path:     "/api/session/refresh",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

// ─── Page handlers ───────────────────────────────────────────────────────────

// BudgetData is passed to the Budget page template.
type BudgetData struct {
	Summary store.SnapshotSummary
	Income  []store.EntryRow
	Commits []store.EntryRow
}

func (s *Server) Budget(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	summary, _ := s.store.GetSnapshotSummary(ctx, entityID)

	entries, _ := s.store.ListEntries(ctx, entityID, store.DateRange{}, "", "live", 500, "")

	var income, commits []store.EntryRow
	for _, e := range entries {
		if e.Direction == "income" {
			income = append(income, e)
		} else {
			commits = append(commits, e)
		}
	}

	return s.render(c, BudgetPage(s.buildShellData(c.Request()), BudgetData{
		Summary: summary,
		Income:  income,
		Commits: commits,
	}))
}

// LedgerData is passed to the Ledger page template.
type LedgerData struct {
	Entries         []store.EntryRow
	Counts          store.EntryCounts
	Filter          string // status filter: all | live | pending | ended
	LabelFilter     string // ?label=<uuid> — filter by label_id
	LabelName       string // display name for active label filter
	DirectionFilter string // ?direction=income|spend
	TypeFilter      string // ?entry_type=standing|variable|irregular
	Sort            string // ?sort=start_date|rate|fitness|label
}

// ledgerFilterURL builds a ledger URL that preserves all current filter params
// and overrides a single param. Pass an empty value to remove that param.
func ledgerFilterURL(d LedgerData, key, value string) string {
	q := url.Values{}
	set := func(k, v, skipVal string) {
		if v != "" && v != skipVal {
			q.Set(k, v)
		}
	}
	set("filter", d.Filter, "all")
	set("label", d.LabelFilter, "")
	set("direction", d.DirectionFilter, "")
	set("entry_type", d.TypeFilter, "")
	set("sort", d.Sort, "label")
	if value == "" {
		q.Del(key)
	} else if (key == "filter" && value == "all") || (key == "sort" && value == "label") {
		q.Del(key)
	} else {
		q.Set(key, value)
	}
	if len(q) == 0 {
		return "/ledger"
	}
	return "/ledger?" + q.Encode()
}

func (s *Server) Ledger(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	filter := c.QueryParam("filter")
	if filter == "" {
		filter = "all"
	}
	labelFilter := c.QueryParam("label")
	dirFilter := c.QueryParam("direction")
	typeFilter := c.QueryParam("entry_type")
	srt := c.QueryParam("sort")
	if srt == "" {
		srt = "label"
	}

	counts, _ := s.store.CountEntriesByStatus(ctx, entityID)

	var entries []store.EntryRow
	if filter == "all" {
		entries, _ = s.store.ListAllEntriesSorted(ctx, entityID)
	} else {
		entries, _ = s.store.ListEntries(ctx, entityID, store.DateRange{}, "", filter, 500, "")
	}

	// Apply additional filters (label, direction, type) in Go after fetch.
	if labelFilter != "" || dirFilter != "" || typeFilter != "" {
		filtered := entries[:0]
		for _, e := range entries {
			if labelFilter != "" && (e.LabelID == nil || *e.LabelID != labelFilter) {
				continue
			}
			if dirFilter != "" && e.Direction != dirFilter {
				continue
			}
			if typeFilter != "" && e.EntryType != typeFilter {
				continue
			}
			filtered = append(filtered, e)
		}
		entries = filtered
	}

	// Apply non-default sort.
	switch srt {
	case "rate":
		sort.SliceStable(entries, func(i, j int) bool {
			ri, rj := entries[i].ActualRatePerDay, entries[j].ActualRatePerDay
			if ri == nil && rj == nil {
				return false
			}
			if ri == nil {
				return false
			}
			if rj == nil {
				return true
			}
			return *ri > *rj
		})
	case "fitness":
		sort.SliceStable(entries, func(i, j int) bool {
			ci, cj := entries[i].Fitness, entries[j].Fitness
			if ci == nil && cj == nil {
				return false
			}
			if ci == nil {
				return false
			}
			if cj == nil {
				return true
			}
			return *ci > *cj
		})
	case "label":
		sort.SliceStable(entries, func(i, j int) bool {
			li, lj := "", ""
			if entries[i].LabelName != nil {
				li = *entries[i].LabelName
			}
			if entries[j].LabelName != nil {
				lj = *entries[j].LabelName
			}
			return li < lj
		})
	}

	for i := range entries {
		entries[i].Conditions = s.store.ConditionsForDisplay(ctx, entityID, entries[i].Conditions)
	}

	// Resolve label name for the filter display badge.
	var labelName string
	if labelFilter != "" {
		if label, err := s.store.GetLabel(ctx, entityID, labelFilter); err == nil {
			labelName = label.Name
		}
	}
	data := LedgerData{
		Entries:         entries,
		Counts:          counts,
		Filter:          filter,
		LabelFilter:     labelFilter,
		LabelName:       labelName,
		DirectionFilter: dirFilter,
		TypeFilter:      typeFilter,
		Sort:            srt,
	}
	return s.render(c, LedgerPage(s.buildShellData(c.Request()), data))
}

func (s *Server) Activity(c echo.Context) error {
	const pageSize = 50
	cursor := c.QueryParam("cursor")
	targetJobID := c.QueryParam("job")
	entityID := middleware.EntityID(c.Request().Context())

	jobs, _ := s.store.ListJobs(c.Request().Context(), entityID, pageSize, cursor)

	var nextCursor string
	if len(jobs) == pageSize {
		last := jobs[len(jobs)-1]
		nextCursor = store.EncodeCursor(last.ID, last.QueuedAt)
	}

	data := ActivityData{
		Jobs:        jobs,
		NextCursor:  nextCursor,
		TargetJobID: targetJobID,
	}
	return s.render(c, ActivityPage(s.buildShellData(c.Request()), data))
}

// fieldRegistryJSON returns the static field registry serialised as a JSON string
// for embedding in page templates.
func fieldRegistryJSON() string {
	b, _ := json.Marshal(fieldregistry.Registry)
	return string(b)
}

// instMappingFields parses an Institution's MappingConfig and returns label/value
// pairs suitable for display in the configuration page.
func instMappingFields(inst store.Institution) []struct{ Label, Value string } {
	var cfg fieldregistry.MappingConfig
	if err := json.Unmarshal(inst.MappingConfig, &cfg); err != nil {
		return nil
	}
	layout := fieldregistry.GetLayout(inst.SourceType, cfg.Layout)
	if layout == nil {
		return nil
	}
	var pairs []struct{ Label, Value string }
	for _, f := range layout.Fields {
		v := cfg.Fields[f.Key]
		if v == "" && !f.Required {
			continue
		}
		pairs = append(pairs, struct{ Label, Value string }{f.Label, v})
	}
	return pairs
}

// AccountData is passed to the Account page template.
type AccountData struct {
	Account      store.Account
	Institution  *store.Institution
	Transactions []store.Transaction
	NextCursor   string
}

func (s *Server) Account(c echo.Context) error {
	id := c.Param("id")
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	account, err := s.store.GetAccount(ctx, entityID, id)
	if err != nil {
		return c.Redirect(http.StatusFound, "/budget")
	}

	var institution *store.Institution
	if account.InstitutionID != nil {
		inst, err := s.store.GetInstitution(ctx, entityID, *account.InstitutionID)
		if err == nil {
			institution = &inst
		}
	}

	txns, _ := s.store.ListTransactions(ctx, entityID, store.DateRange{}, id, "", 200, "")

	var nextCursor string
	if len(txns) == 200 {
		last := txns[len(txns)-1]
		nextCursor = store.EncodeDateCursor(last.ID, last.Date)
	}

	data := AccountData{
		Account:      account,
		Institution:  institution,
		Transactions: txns,
		NextCursor:   nextCursor,
	}
	return s.render(c, AccountPage(s.buildShellData(c.Request()), data))
}

// ─── Page helpers ─────────────────────────────────────────────────────────────

// accountTypeLabel returns a display-friendly account type name.
func accountTypeLabel(t string) string {
	switch t {
	case "checking":
		return "Checking"
	case "savings":
		return "Savings"
	case "credit":
		return "Credit"
	case "loan":
		return "Loan"
	case "mortgage":
		return "Mortgage"
	case "investment":
		return "Investment"
	default:
		return t
	}
}

// isAccountNegativeBalance returns true for credit/loan/mortgage with negative balance.
func isAccountNegativeBalance(a store.Account) bool {
	if a.BalanceCents == nil || *a.BalanceCents >= 0 {
		return false
	}
	return a.AccountType == "credit" || a.AccountType == "loan" || a.AccountType == "mortgage"
}

// directionLabel returns "Income" or "Spend".
func directionLabel(d string) string {
	if d == "income" {
		return "Income"
	}
	return "Spend"
}

// entryTypeLabel returns a readable label for entry type.
func entryTypeLabel(t string) string {
	switch t {
	case "standing":
		return "Standing"
	case "variable":
		return "Variable"
	case "irregular":
		return "Irregular"
	default:
		return t
	}
}

// alertTypeLabel returns a display label for alert type.
func alertTypeLabel(t *string) string {
	if t == nil {
		return ""
	}
	switch *t {
	case "new_recurring":
		return "New"
	case "drift":
		return "Drift"
	case "anomaly":
		return "Anomaly"
	default:
		return *t
	}
}

// fmtRateDay formats a cents/day rate as $/day.
func fmtRateDay(r *float64) string {
	if r == nil {
		return "—"
	}
	v := *r / 100.0
	if v < 0 {
		v = -v
	}
	return fmt.Sprintf("$%.2f/day", v)
}

// fmtRateMo formats a cents/day rate as $/mo (× 30.44).
func fmtRateMo(r *float64) string {
	if r == nil {
		return "—"
	}
	v := *r / 100.0 * 30.44
	if v < 0 {
		v = -v
	}
	return fmt.Sprintf("$%.2f/mo", v)
}

// fmtRateYr formats a cents/day rate as $/yr (× 365).
func fmtRateYr(r *float64) string {
	if r == nil {
		return "—"
	}
	v := *r / 100.0 * 365
	if v < 0 {
		v = -v
	}
	return fmt.Sprintf("$%.2f/yr", v)
}

// fitPct formats a fitness/fit float as a percentage string.
func fitPct(f *float64) string {
	if f == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *f*100)
}

// fitColor returns a CSS color variable for a fitness/fit value.
func fitColor(f *float64) string {
	if f == nil {
		return "var(--text3)"
	}
	if *f >= 0.8 {
		return "var(--income)"
	}
	if *f >= 0.5 {
		return "var(--accent)"
	}
	return "var(--commit)"
}

// entryStatusColor returns a CSS color for an entry status.
func entryStatusColor(status string) string {
	switch status {
	case "live":
		return "var(--income)"
	case "pending":
		return "var(--accent)"
	default:
		return "var(--text3)"
	}
}

// conditionsFormatted returns pretty-printed JSON conditions or an empty object.
func conditionsFormatted(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// entryPutBody is the JSON shape expected by PUT /entries/{id}.
type entryPutBody struct {
	LabelID             *string         `json:"label_id"`
	Direction           string          `json:"direction"`
	EntryType           string          `json:"entry_type"`
	PeriodDays          int             `json:"period_days"`
	VariableMethod      *string         `json:"variable_method"`
	ProjectedRatePerDay *float64        `json:"projected_rate_per_day"`
	Conditions          json.RawMessage `json:"conditions"`
	Priority            int             `json:"priority"`
	Status              string          `json:"status"`
	StartDate           string          `json:"start_date"`
	EndDate             *string         `json:"end_date"`
	ProjectTentatively  bool            `json:"project_tentatively"`
}

// entryDataJSON serializes the entry into the PUT /entries/{id} body format.
// The JS review panel reads this from data-entry to pre-populate and then PUT.
func entryDataJSON(e store.EntryRow) string {
	var endDate *string
	if e.EndDate != nil {
		s := e.EndDate.Format("2006-01-02")
		endDate = &s
	}
	conds := e.Conditions
	if len(conds) == 0 || string(conds) == "null" {
		conds = json.RawMessage(`{}`)
	}
	b, _ := json.Marshal(entryPutBody{
		LabelID:             e.LabelID,
		Direction:           e.Direction,
		EntryType:           e.EntryType,
		PeriodDays:          func() int { if e.PeriodDays != nil { return *e.PeriodDays }; return 0 }(),
		VariableMethod:      e.VariableMethod,
		ProjectedRatePerDay: e.ProjectedRatePerDay,
		Conditions:          conds,
		Priority:            e.Priority,
		Status:              e.Status,
		StartDate:           e.StartDate.Format("2006-01-02"),
		EndDate:             endDate,
		ProjectTentatively:  e.ProjectTentatively,
	})
	return string(b)
}

// formatCents formats an int64 cents value as a USD amount string.
func formatCents(cents int64) string {
	neg := cents < 0
	abs := cents
	if neg {
		abs = -cents
	}
	dollars := abs / 100
	rem := abs % 100
	s := commaInt(dollars) + "." + fmt.Sprintf("%02d", rem)
	if neg {
		return "-$" + s
	}
	return "$" + s
}

// balanceCentsStr returns the balance cents as a string for data attributes,
// or empty string when nil.
func balanceCentsStr(cents *int64) string {
	if cents == nil {
		return ""
	}
	return strconv.FormatInt(*cents, 10)
}

// startingBalanceCentsStr returns starting_balance_cents as a string for data attributes.
func startingBalanceCentsStr(cents int64) string {
	return strconv.FormatInt(cents, 10)
}

// txnAmountStyle returns a CSS color for a transaction amount (debit vs credit).
func txnAmountStyle(cents int64) string {
	if cents >= 0 {
		return "color:var(--income);font-variant-numeric:tabular-nums"
	}
	return "color:var(--commit);font-variant-numeric:tabular-nums"
}

// InstitutionWithAccounts pairs an institution with the accounts that use it.
type InstitutionWithAccounts struct {
	store.Institution
	Accounts []store.Account
}

// ConfigurationData is passed to the Configuration page template.
type ConfigurationData struct {
	Tab          string
	Labels       []store.LabelWithCount
	Institutions []InstitutionWithAccounts
	EntityConfig store.EntityConfig
}

func (s *Server) Configuration(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	tab := c.QueryParam("tab")
	if tab == "" || tab == "merchants" {
		tab = "labels"
	}

	data := ConfigurationData{Tab: tab}
	switch tab {
	case "institutions":
		insts, _ := s.store.ListInstitutions(ctx, entityID)
		for _, inst := range insts {
			accounts, _ := s.store.ListAccountsByInstitution(ctx, entityID, inst.ID, 200, "")
			data.Institutions = append(data.Institutions, InstitutionWithAccounts{
				Institution: inst,
				Accounts:    accounts,
			})
		}
	case "system":
		data.EntityConfig, _ = s.store.GetEntityConfig(ctx, entityID)
	default:
		data.Labels, _ = s.store.ListLabelsWithEntryCount(ctx, entityID)
	}
	return s.render(c, ConfigurationPage(s.buildShellData(c.Request()), data))
}

func (s *Server) Settings(c echo.Context) error {
	return s.render(c, SettingsPage(s.buildShellData(c.Request())))
}

func (s *Server) Glossary(c echo.Context) error {
	return s.render(c, GlossaryPage(s.buildShellData(c.Request())))
}

// ReportsData is passed to the Reports page template.
type ReportsData struct {
	Summary     store.SnapshotSummary
	Projections []store.Projection
	PinchCount  int
}

func (s *Server) Reports(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	summary, _ := s.store.GetSnapshotSummary(ctx, entityID)

	// Fetch up to 180 projections (DESC from store), then reverse to ascending.
	all, _ := s.store.ListProjections(ctx, entityID, 180, "")

	// Keep only entity-level rows (account_id IS NULL = aggregate projection).
	projections := all[:0]
	for _, p := range all {
		if p.AccountID == nil {
			projections = append(projections, p)
		}
	}

	// Reverse to ascending (soonest date first).
	for i, j := 0, len(projections)-1; i < j; i, j = i+1, j-1 {
		projections[i], projections[j] = projections[j], projections[i]
	}

	pinchCount := 0
	for _, p := range projections {
		if p.IsPinchPoint {
			pinchCount++
		}
	}

	return s.render(c, ReportsPage(s.buildShellData(c.Request()), ReportsData{
		Summary:     summary,
		Projections: projections,
		PinchCount:  pinchCount,
	}))
}

// fetchUserName looks up the display name for a user by ID.
func (s *Server) fetchUserName(ctx context.Context, userID string) string {
	var name string
	s.pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&name) //nolint:errcheck
	return name
}
