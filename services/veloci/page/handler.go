package page

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/veloci/veloci/authclient"
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

func (s *Server) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
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
func (s *Server) GetLogin(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, Login("", ""))
}

// PostLogin handles login form submission.
func (s *Server) PostLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.render(w, r, Login("Invalid request", ""))
		return
	}
	email := r.FormValue("email")
	password := r.FormValue("password")
	ctx := r.Context()

	cred, err := s.auth.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
		Email:    email,
		Password: password,
	})
	if err != nil {
		s.render(w, r, Login("Invalid email or password", email))
		return
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
		s.render(w, r, Login("Invalid email or password", email))
		return
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
		s.render(w, r, Login("Login failed, please try again", email))
		return
	}

	expiry, _ := time.Parse(time.RFC3339, minted.ExpiresAt)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    minted.AccessToken,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/", http.StatusFound)
}

// PostLogout revokes the session token and clears the cookie.
func (s *Server) PostLogout(w http.ResponseWriter, r *http.Request) {
	if jti := middleware.JTI(r.Context()); jti != "" {
		s.auth.RevokeToken(r.Context(), authclient.RevokeTokenParams{Jti: jti}) //nolint:errcheck
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ─── Page handlers ───────────────────────────────────────────────────────────

func (s *Server) Budget(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, BudgetPage(s.buildShellData(r)))
}

// LedgerData is passed to the Ledger page template.
type LedgerData struct {
	Entries []store.EntryRow
	Counts  store.EntryCounts
	Filter  string
}

func (s *Server) Ledger(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	entityID := middleware.EntityID(ctx)

	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}

	counts, _ := s.store.CountEntriesByStatus(ctx, entityID)

	var entries []store.EntryRow
	if filter == "all" {
		entries, _ = s.store.ListAllEntriesSorted(ctx, entityID)
	} else {
		entries, _ = s.store.ListEntries(ctx, entityID, store.DateRange{}, "", filter, 500, "")
	}

	data := LedgerData{
		Entries: entries,
		Counts:  counts,
		Filter:  filter,
	}
	s.render(w, r, LedgerPage(s.buildShellData(r), data))
}

func (s *Server) Activity(w http.ResponseWriter, r *http.Request) {
	const pageSize = 50
	cursor := r.URL.Query().Get("cursor")
	targetJobID := r.URL.Query().Get("job")
	entityID := middleware.EntityID(r.Context())

	jobs, _ := s.store.ListJobs(r.Context(), entityID, pageSize, cursor)

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
	s.render(w, r, ActivityPage(s.buildShellData(r), data))
}

// AccountData is passed to the Account page template.
type AccountData struct {
	Account      store.Account
	Institution  *store.Institution
	Transactions []store.Transaction
	NextCursor   string
}

func (s *Server) Account(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	entityID := middleware.EntityID(ctx)

	account, err := s.store.GetAccount(ctx, entityID, id)
	if err != nil {
		http.Error(w, "account not found", http.StatusNotFound)
		return
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
	s.render(w, r, AccountPage(s.buildShellData(r), data))
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

// directionLabel returns "Debit" or "Credit".
func directionLabel(d string) string {
	if d == "credit" {
		return "Credit"
	}
	return "Debit"
}

// entryTypeLabel returns a readable label for entry type.
func entryTypeLabel(t string) string {
	switch t {
	case "fixed":
		return "Fixed"
	case "variable":
		return "Variable"
	case "one_time":
		return "One-time"
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

// ratePerMo returns a formatted monthly rate estimate from projected_rate_per_day.
func ratePerMo(e store.EntryRow) string {
	if e.ProjectedRatePerDay == nil {
		return "—"
	}
	monthly := *e.ProjectedRatePerDay * 30.44
	if monthly < 0 {
		monthly = -monthly
	}
	return "$" + fmt.Sprintf("%.0f", monthly) + "/mo"
}

// confPct formats a confidence float as a percentage string.
func confPct(f *float64) string {
	if f == nil {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", *f*100)
}

// confColor returns a CSS color variable for a confidence value.
func confColor(f *float64) string {
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
	case "active":
		return "var(--income)"
	case "pending_review":
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
		PeriodDays:          e.PeriodDays,
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

// txnAmountStyle returns a CSS color for a transaction amount (debit vs credit).
func txnAmountStyle(cents int64) string {
	if cents >= 0 {
		return "color:var(--income);font-variant-numeric:tabular-nums"
	}
	return "color:var(--commit);font-variant-numeric:tabular-nums"
}

func (s *Server) Configuration(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, ConfigurationPage(s.buildShellData(r)))
}

func (s *Server) Settings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, SettingsPage(s.buildShellData(r)))
}

func (s *Server) Glossary(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, GlossaryPage(s.buildShellData(r)))
}

func (s *Server) Reports(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, ReportsPage(s.buildShellData(r)))
}

// fetchUserName looks up the display name for a user by ID.
func (s *Server) fetchUserName(ctx context.Context, userID string) string {
	var name string
	s.pool.QueryRow(ctx, `SELECT name FROM users WHERE id = $1`, userID).Scan(&name) //nolint:errcheck
	return name
}
