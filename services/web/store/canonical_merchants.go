package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// CanonicalMerchant represents a row from the canonical_merchants table.
type CanonicalMerchant struct {
	ID        string    `db:"id"`
	EntityID  string    `db:"entity_id"`
	Name      string    `db:"name"`
	Source    string    `db:"source"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// CanonicalMerchantAlias represents a row from the canonical_merchant_aliases table.
type CanonicalMerchantAlias struct {
	EntityID            string    `db:"entity_id"`
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

const canonicalMerchantCols = `id::text, entity_id::text, name, source, created_at, updated_at`

const canonicalMerchantAliasCols = `entity_id::text, normalized_name, canonical_merchant_id::text, source, created_at`

// ListCanonicalMerchants returns a paginated list of canonical merchants for the
// entity with their alias counts, ordered by creation date descending.
func (s *Store) ListCanonicalMerchants(ctx context.Context, entityID string, limit int, cursor string) ([]CanonicalMerchantWithCounts, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, `
			SELECT cm.id::text, cm.entity_id::text, cm.name, cm.source, cm.created_at, cm.updated_at,
			       COUNT(cma.normalized_name)::int AS alias_count
			FROM canonical_merchants cm
			LEFT JOIN canonical_merchant_aliases cma ON cma.canonical_merchant_id = cm.id
			WHERE cm.entity_id = $1
			GROUP BY cm.id, cm.entity_id, cm.name, cm.source, cm.created_at, cm.updated_at
			ORDER BY cm.created_at DESC, cm.id DESC
			LIMIT $2
		`, entityID, limit)
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
		SELECT cm.id::text, cm.entity_id::text, cm.name, cm.source, cm.created_at, cm.updated_at,
		       COUNT(cma.normalized_name)::int AS alias_count
		FROM canonical_merchants cm
		LEFT JOIN canonical_merchant_aliases cma ON cma.canonical_merchant_id = cm.id
		WHERE cm.entity_id = $1
		  AND (cm.created_at, cm.id::text) < ($2::timestamptz, $3)
		GROUP BY cm.id, cm.entity_id, cm.name, cm.source, cm.created_at, cm.updated_at
		ORDER BY cm.created_at DESC, cm.id DESC
		LIMIT $4
	`, entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CanonicalMerchantWithCounts])
}

// GetCanonicalMerchant fetches a single canonical merchant by id scoped to the entity.
func (s *Store) GetCanonicalMerchant(ctx context.Context, entityID, id string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM canonical_merchants WHERE entity_id = $1 AND id = $2
	`, canonicalMerchantCols), entityID, id)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// CreateCanonicalMerchant inserts a new canonical merchant with source='user'.
func (s *Store) CreateCanonicalMerchant(ctx context.Context, entityID, name string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO canonical_merchants (id, entity_id, name, source, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, 'user', NOW(), NOW())
		RETURNING %s
	`, canonicalMerchantCols), entityID, name)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// RenameCanonicalMerchant updates the name of a canonical merchant scoped to the entity.
func (s *Store) RenameCanonicalMerchant(ctx context.Context, entityID, id, name string) (CanonicalMerchant, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE canonical_merchants SET name = $3, updated_at = NOW()
		WHERE entity_id = $1 AND id = $2
		RETURNING %s
	`, canonicalMerchantCols), entityID, id, name)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchant])
}

// DeleteCanonicalMerchant removes a canonical merchant scoped to the entity.
// Aliases are cascade-deleted.
func (s *Store) DeleteCanonicalMerchant(ctx context.Context, entityID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM canonical_merchants WHERE entity_id = $1 AND id = $2
	`, entityID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListCanonicalMerchantAliases returns all aliases for a canonical merchant.
func (s *Store) ListCanonicalMerchantAliases(ctx context.Context, entityID, merchantID string) ([]CanonicalMerchantAlias, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM canonical_merchant_aliases
		WHERE entity_id = $1 AND canonical_merchant_id = $2
		ORDER BY normalized_name ASC
	`, canonicalMerchantAliasCols), entityID, merchantID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[CanonicalMerchantAlias])
}

// AddCanonicalMerchantAlias inserts a new alias for a canonical merchant with source='user'.
func (s *Store) AddCanonicalMerchantAlias(ctx context.Context, entityID, merchantID, normalizedName string) (CanonicalMerchantAlias, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO canonical_merchant_aliases (entity_id, normalized_name, canonical_merchant_id, source, created_at)
		VALUES ($1, $2, $3, 'user', NOW())
		RETURNING %s
	`, canonicalMerchantAliasCols), entityID, normalizedName, merchantID)
	if err != nil {
		return CanonicalMerchantAlias{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[CanonicalMerchantAlias])
}

// DeleteCanonicalMerchantAlias removes a specific alias scoped to the entity.
func (s *Store) DeleteCanonicalMerchantAlias(ctx context.Context, entityID, normalizedName string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM canonical_merchant_aliases WHERE entity_id = $1 AND normalized_name = $2
	`, entityID, normalizedName)
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
func (s *Store) MergeCanonicalMerchants(ctx context.Context, entityID, survivorID, absorbedID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Move all aliases to the survivor.
	_, err = tx.Exec(ctx, `
		UPDATE canonical_merchant_aliases
		SET canonical_merchant_id = $2
		WHERE entity_id = $1 AND canonical_merchant_id = $3
	`, entityID, survivorID, absorbedID)
	if err != nil {
		return err
	}

	// Patch entry conditions JSONB: replace every occurrence of the absorbed UUID
	// string with the survivor UUID string (text-level replacement is safe because
	// UUIDs are globally unique and won't collide with other JSON values).
	_, err = tx.Exec(ctx, `
		UPDATE entries
		SET conditions = REPLACE(conditions::text, $2, $3)::jsonb
		WHERE entity_id = $1
		  AND conditions IS NOT NULL
		  AND conditions::text LIKE '%' || $2 || '%'
	`, entityID, absorbedID, survivorID)
	if err != nil {
		return err
	}

	// Delete the absorbed merchant (aliases already moved, so no FK violation).
	tag, err := tx.Exec(ctx, `
		DELETE FROM canonical_merchants WHERE entity_id = $1 AND id = $2
	`, entityID, absorbedID)
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
func (s *Store) SplitCanonicalMerchant(ctx context.Context, entityID, sourceID string, aliasNames []string, newName string) (CanonicalMerchant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CanonicalMerchant{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Create the new canonical merchant.
	rows, err := tx.Query(ctx, fmt.Sprintf(`
		INSERT INTO canonical_merchants (id, entity_id, name, source, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, 'user', NOW(), NOW())
		RETURNING %s
	`, canonicalMerchantCols), entityID, newName)
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
		SET canonical_merchant_id = $2
		WHERE entity_id = $1
		  AND canonical_merchant_id = $3
		  AND normalized_name = ANY($4)
	`, entityID, newMerchant.ID, sourceID, aliasNames)
	if err != nil {
		return CanonicalMerchant{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return CanonicalMerchant{}, err
	}
	return newMerchant, nil
}
