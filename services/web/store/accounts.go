package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Account represents a row from the accounts table.
type Account struct {
	ID                   string    `db:"id"`
	EntityID             string    `db:"entity_id"`
	InstitutionID        *string   `db:"institution_id"`
	Name                 string    `db:"name"`
	AccountType          string    `db:"account_type"`
	Status               string    `db:"status"`
	InterestRate         *float64  `db:"interest_rate"`
	StartingBalanceCents int64     `db:"starting_balance_cents"`
	BalanceCents         *int64    `db:"balance_cents"`
	CreditLimitCents     *int64    `db:"credit_limit_cents"`
	CreatedAt            time.Time `db:"created_at"`
}

const accountCols = `
	id::text, entity_id::text, institution_id::text,
	name, account_type, status,
	interest_rate, starting_balance_cents,
	(starting_balance_cents + COALESCE((SELECT SUM(t.amount_cents) FROM transactions t WHERE t.account_id = accounts.id), 0)) AS balance_cents,
	credit_limit_cents, created_at
`

// GetAccount fetches a single account by id for an entity.
func (s *Store) GetAccount(ctx context.Context, entityID, id string) (Account, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM accounts
		WHERE entity_id = $1 AND id = $2
	`, accountCols), entityID, id)
	if err != nil {
		return Account{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Account])
}

// ListAccounts returns paginated accounts for an entity across all institutions.
func (s *Store) ListAccounts(ctx context.Context, entityID string, limit int, cursor string) ([]Account, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM accounts
			WHERE entity_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		`, accountCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Account])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM accounts
		WHERE entity_id = $1
		  AND (created_at, id::text) < ($2::timestamptz, $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, accountCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Account])
}

// ListAccountsByInstitution returns paginated accounts for an institution within an entity.
func (s *Store) ListAccountsByInstitution(ctx context.Context, entityID, institutionID string, limit int, cursor string) ([]Account, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM accounts
			WHERE entity_id = $1 AND institution_id = $2
			ORDER BY created_at DESC, id DESC
			LIMIT $3
		`, accountCols), entityID, institutionID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Account])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM accounts
		WHERE entity_id = $1 AND institution_id = $2
		  AND (created_at, id::text) < ($3::timestamptz, $4)
		ORDER BY created_at DESC, id DESC
		LIMIT $5
	`, accountCols), entityID, institutionID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Account])
}

// CreateAccount inserts a new account row.
func (s *Store) CreateAccount(ctx context.Context, entityID string, in Account) (Account, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO accounts (
			id, entity_id, institution_id, name, account_type, status,
			interest_rate, starting_balance_cents, credit_limit_cents, created_at
		) VALUES (
			gen_random_uuid(), $1, $2::uuid, $3, $4, $5,
			$6, $7, $8, NOW()
		)
		RETURNING %s
	`, accountCols),
		entityID, in.InstitutionID, in.Name, in.AccountType, in.Status,
		in.InterestRate, in.StartingBalanceCents, in.CreditLimitCents,
	)
	if err != nil {
		return Account{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Account])
}

// UpdateAccount updates mutable fields on an account row. A nil InstitutionID
// leaves the existing institution_id untouched — pass a non-nil value to relink.
func (s *Store) UpdateAccount(ctx context.Context, entityID, id string, in Account) (Account, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE accounts SET
			name = $3,
			account_type = $4,
			status = $5,
			interest_rate = $6,
			starting_balance_cents = $7,
			credit_limit_cents = $8,
			institution_id = COALESCE($9::uuid, institution_id)
		WHERE entity_id = $1 AND id = $2
		RETURNING %s
	`, accountCols),
		entityID, id,
		in.Name, in.AccountType, in.Status,
		in.InterestRate, in.StartingBalanceCents, in.CreditLimitCents,
		in.InstitutionID,
	)
	if err != nil {
		return Account{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Account])
}

// DeleteAccount removes an account row.
func (s *Store) DeleteAccount(ctx context.Context, entityID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM accounts WHERE entity_id = $1 AND id = $2
	`, entityID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
