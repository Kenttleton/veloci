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

func (s *Server) Ledger(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, LedgerPage(s.buildShellData(r)))
}

func (s *Server) Activity(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, ActivityPage(s.buildShellData(r)))
}

func (s *Server) Account(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.render(w, r, AccountPage(s.buildShellData(r), id))
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
