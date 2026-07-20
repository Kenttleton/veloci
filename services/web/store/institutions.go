package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Institution represents a row from the institution_mappings table.
type Institution struct {
	ID                   string          `db:"id"`
	EntityID             string          `db:"entity_id"`
	InstitutionName      string          `db:"institution_name"`
	SourceType           string          `db:"source_type"`
	SettlementWindowDays int             `db:"settlement_window_days"`
	DedupWindowDays      int             `db:"dedup_window_days"`
	AmountTolerancePct   float64         `db:"amount_tolerance_pct"`
	MappingConfig        json.RawMessage `db:"mapping_config"`
	CreatedAt            time.Time       `db:"created_at"`
}

const institutionCols = `
	id::text, entity_id::text, institution_name, source_type,
	settlement_window_days, dedup_window_days, amount_tolerance_pct,
	mapping_config, created_at
`

// GetInstitution fetches a single institution by id for an entity.
func (s *Store) GetInstitution(ctx context.Context, entityID, id string) (Institution, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM institution_mappings
		WHERE entity_id = $1 AND id = $2
	`, institutionCols), entityID, id)
	if err != nil {
		return Institution{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Institution])
}

// ListInstitutions returns every institution for an entity, unpaginated.
func (s *Store) ListInstitutions(ctx context.Context, entityID string) ([]Institution, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM institution_mappings
		WHERE entity_id = $1
		ORDER BY institution_name
	`, institutionCols), entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Institution])
}

// CreateInstitution inserts a new institution_mappings row.
func (s *Store) CreateInstitution(ctx context.Context, entityID string, in Institution) (Institution, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO institution_mappings (
			id, entity_id, institution_name, source_type,
			settlement_window_days, dedup_window_days, amount_tolerance_pct,
			mapping_config, created_at
		) VALUES (
			gen_random_uuid(), $1, $2, $3,
			$4, $5, $6,
			$7, NOW()
		)
		RETURNING %s
	`, institutionCols),
		entityID, in.InstitutionName, in.SourceType,
		in.SettlementWindowDays, in.DedupWindowDays, in.AmountTolerancePct,
		in.MappingConfig,
	)
	if err != nil {
		return Institution{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Institution])
}

// UpdateInstitution updates mutable fields on an institution_mappings row.
func (s *Store) UpdateInstitution(ctx context.Context, entityID, id string, in Institution) (Institution, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE institution_mappings SET
			institution_name       = $3,
			source_type            = $4,
			settlement_window_days = $5,
			dedup_window_days      = $6,
			amount_tolerance_pct   = $7,
			mapping_config         = $8
		WHERE entity_id = $1 AND id = $2
		RETURNING %s
	`, institutionCols),
		entityID, id,
		in.InstitutionName, in.SourceType,
		in.SettlementWindowDays, in.DedupWindowDays, in.AmountTolerancePct,
		in.MappingConfig,
	)
	if err != nil {
		return Institution{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Institution])
}

// DeleteInstitution removes an institution_mappings row.
func (s *Store) DeleteInstitution(ctx context.Context, entityID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM institution_mappings WHERE entity_id = $1 AND id = $2
	`, entityID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// EncodeCursor base64-encodes "id,timestamp" for timestamp-based keyset pagination.
func EncodeCursor(id string, createdAt time.Time) string {
	raw := id + "," + createdAt.UTC().Format(time.RFC3339Nano)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// EncodeDateCursor base64-encodes "id,date" for date-based keyset pagination (transactions, entries).
func EncodeDateCursor(id string, date time.Time) string {
	raw := id + "," + date.Format("2006-01-02")
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// decodeCursor decodes a cursor string into id and timestamp string.
func decodeCursor(cursor string) (id string, ts string, err error) {
	b, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", err
	}
	raw := string(b)
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == ',' {
			return raw[:i], raw[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("invalid cursor")
}
