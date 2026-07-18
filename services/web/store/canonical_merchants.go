package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CanonicalMerchant represents a row from the canonical_merchants table.
// Canonical merchants are global (no entity_id) — shared reference data.
type CanonicalMerchant struct {
	ID        string    `db:"id"`
	Name      string    `db:"name"`
	Source    string    `db:"source"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// CanonicalMerchantAlias represents a row from the canonical_merchant_aliases table.
type CanonicalMerchantAlias struct {
	NormalizedName      string    `db:"normalized_name"`
	CanonicalMerchantID string    `db:"canonical_merchant_id"`
	Source              string    `db:"source"`
	CreatedAt           time.Time `db:"created_at"`
}

// CanonicalMerchantWithCounts extends CanonicalMerchant with aggregate counts.
type CanonicalMerchantWithCounts struct {
	CanonicalMerchant
	AliasCount int `db:"alias_count"`
}

const canonicalMerchantCols = `id::text, name, source, created_at, updated_at`

const canonicalMerchantAliasCols = `normalized_name, canonical_merchant_id::text, source, created_at`

// ListCanonicalMerchants returns a paginated list of all canonical merchants
// with their alias counts, ordered by creation date descending.
func (s *Store) ListCanonicalMerchants(ctx context.Context, limit int, cursor string) ([]CanonicalMerchantWithCounts, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, `
			SELECT cm.id::text, cm.name, cm.source, cm.created_at, cm.updated_at,
			       COUNT(cma.normalized_name)::int AS alias_count
			FROM canonical_merchants cm
			LEFT JOIN canonical_merchant_aliases cma ON cma.canonical_merchant_id = cm.id
			GROUP BY cm.id, cm.name, cm.source, cm.created_at, cm.updated_at
			ORDER BY cm.created_at DESC, cm.id DESC
			LIMIT $1
		`, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[CanonicalMerchantWithCounts])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT cm.id::text, cm.name, cm.source, cm.created_at, cm.updated_at,
		       COUNT(cma.normalized_name)::int AS alias_count
		FROM canonical_merchants cm
		LEFT JOIN canonical_merchant_aliases cma ON cma.canonical_merchant_id = cm.id
		WHERE (cm.created_at, cm.id::text) < ($1::timestamptz, $2)
		GROUP BY cm.id, cm.name, cm.source, cm.created_at, cm.updated_at
		ORDER BY cm.created_at DESC, cm.id DESC
		LIMIT $3
	`, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CanonicalMerchantWithCounts])
}

// GetCanonicalMerchant fetches a single canonical merchant by id.
func (s *Store) GetCanonicalMerchant(ctx context.Context, id string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM canonical_merchants WHERE id = $1
	`, canonicalMerchantCols), id)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// CreateCanonicalMerchant inserts a new canonical merchant with source='user'.
func (s *Store) CreateCanonicalMerchant(ctx context.Context, name string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO canonical_merchants (id, name, source, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'user', NOW(), NOW())
		RETURNING %s
	`, canonicalMerchantCols), name)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// RenameCanonicalMerchant updates the name of a canonical merchant.
func (s *Store) RenameCanonicalMerchant(ctx context.Context, id, name string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE canonical_merchants SET name = $2, updated_at = NOW() WHERE id = $1
		RETURNING %s
	`, canonicalMerchantCols), id, name)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// DeleteCanonicalMerchant removes a canonical merchant. Aliases are cascade-deleted.
func (s *Store) DeleteCanonicalMerchant(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM canonical_merchants WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListCanonicalMerchantAliases returns all aliases for a canonical merchant.
func (s *Store) ListCanonicalMerchantAliases(ctx context.Context, merchantID string) ([]CanonicalMerchantAlias, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM canonical_merchant_aliases
		WHERE canonical_merchant_id = $1
		ORDER BY normalized_name ASC
	`, canonicalMerchantAliasCols), merchantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CanonicalMerchantAlias])
}

// AddCanonicalMerchantAlias inserts a new alias for a canonical merchant with source='user'.
func (s *Store) AddCanonicalMerchantAlias(ctx context.Context, merchantID, normalizedName string) (CanonicalMerchantAlias, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO canonical_merchant_aliases (normalized_name, canonical_merchant_id, source, created_at)
		VALUES ($1, $2, 'user', NOW())
		RETURNING %s
	`, canonicalMerchantAliasCols), normalizedName, merchantID)
	if err != nil {
		return CanonicalMerchantAlias{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchantAlias])
}

// DeleteCanonicalMerchantAlias removes a specific alias.
func (s *Store) DeleteCanonicalMerchantAlias(ctx context.Context, normalizedName string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM canonical_merchant_aliases WHERE normalized_name = $1
	`, normalizedName)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// MergeCanonicalMerchants merges absorbedID into survivorID within a transaction:
// moves all aliases to the survivor, patches entry conditions JSONB to replace
// the absorbed UUID with the survivor UUID, then deletes the absorbed merchant.
func (s *Store) MergeCanonicalMerchants(ctx context.Context, survivorID, absorbedID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Move all aliases to the survivor.
	_, err = tx.Exec(ctx, `
		UPDATE canonical_merchant_aliases
		SET canonical_merchant_id = $1
		WHERE canonical_merchant_id = $2
	`, survivorID, absorbedID)
	if err != nil {
		return err
	}

	// Patch entry conditions JSONB: replace every occurrence of the absorbed UUID
	// string with the survivor UUID string (text-level replacement is safe because
	// UUIDs are globally unique and won't collide with other JSON values).
	_, err = tx.Exec(ctx, `
		UPDATE entries
		SET conditions = REPLACE(conditions::text, $1, $2)::jsonb
		WHERE conditions IS NOT NULL
		  AND conditions::text LIKE '%' || $1 || '%'
	`, absorbedID, survivorID)
	if err != nil {
		return err
	}

	// Delete the absorbed merchant (aliases already moved, so no FK violation).
	tag, err := tx.Exec(ctx, `DELETE FROM canonical_merchants WHERE id = $1`, absorbedID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}

	return tx.Commit(ctx)
}

// SplitCanonicalMerchant creates a new canonical merchant and moves the specified
// aliases to it. Returns the newly created merchant.
func (s *Store) SplitCanonicalMerchant(ctx context.Context, sourceID string, aliasNames []string, newName string) (CanonicalMerchant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Create the new canonical merchant.
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		INSERT INTO canonical_merchants (id, name, source, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'user', NOW(), NOW())
		RETURNING %s
	`, canonicalMerchantCols), newName)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	newMerchant, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
	if err != nil {
		return CanonicalMerchant{}, err
	}

	// Move selected aliases to the new merchant.
	_, err = tx.Exec(ctx, `
		UPDATE canonical_merchant_aliases
		SET canonical_merchant_id = $1
		WHERE canonical_merchant_id = $2
		  AND normalized_name = ANY($3)
	`, newMerchant.ID, sourceID, aliasNames)
	if err != nil {
		return CanonicalMerchant{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return CanonicalMerchant{}, err
	}
	return newMerchant, nil
}
