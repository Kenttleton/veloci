package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// EntityConfig holds per-entity pipeline configuration.
type EntityConfig struct {
	EntityID          string    `db:"entity_id"`
	SystemWindowDays  int       `db:"system_window_days"`
	CreatedAt         time.Time `db:"created_at"`
	UpdatedAt         time.Time `db:"updated_at"`
}

// GetEntityConfig returns the entity_config row for the given entity, or a
// default struct if no row exists yet.
func (s *Store) GetEntityConfig(ctx context.Context, entityID string) (EntityConfig, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT entity_id::text, system_window_days, created_at, updated_at
		FROM entity_config
		WHERE entity_id = $1
	`, entityID)
	if err != nil {
		return EntityConfig{}, err
	}
	cfg, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[EntityConfig])
	if err == pgx.ErrNoRows {
		return EntityConfig{EntityID: entityID, SystemWindowDays: 90}, nil
	}
	return cfg, err
}

// UpdateEntityConfig updates the system_window_days for the given entity.
// The entity_config row must already exist (created by EnsureEntityConfig).
func (s *Store) UpdateEntityConfig(ctx context.Context, entityID string, systemWindowDays int) (EntityConfig, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE entity_config
		SET system_window_days = $2, updated_at = NOW()
		WHERE entity_id = $1
		RETURNING entity_id::text, system_window_days, created_at, updated_at
	`, entityID, systemWindowDays)
	if err != nil {
		return EntityConfig{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[EntityConfig])
}

// EnsureEntityConfig creates the entity_config row if it does not exist.
func (s *Store) EnsureEntityConfig(ctx context.Context, entityID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO entity_config (entity_id)
		VALUES ($1)
		ON CONFLICT (entity_id) DO NOTHING
	`, entityID)
	return err
}

// EnsureSystemData idempotently initialises all system-managed data for an entity:
//   - system labels (Income, Spend)
//   - entity_config row
//   - Income and Spend system entries (scope='system', priority=9999, status='live')
func (s *Store) EnsureSystemData(ctx context.Context, entityID string) error {
	if err := s.EnsureSystemLabels(ctx, entityID); err != nil {
		return err
	}
	if err := s.EnsureEntityConfig(ctx, entityID); err != nil {
		return err
	}

	for _, dir := range []string{"income", "spend"} {
		labelName := "Income"
		if dir == "spend" {
			labelName = "Spend"
		}
		conds := `{"entry_direction": "` + dir + `"}`

		_, err := s.pool.Exec(ctx, `
			INSERT INTO entries (
				id, entity_id, label_id, direction, entry_type,
				scope, status, source, priority, conditions, start_date, created_at
			)
			SELECT
				gen_random_uuid(), $1::uuid,
				(SELECT id FROM labels WHERE entity_id = $1::uuid AND name = $2 AND scope = 'system'),
				$3, 'irregular',
				'system', 'live', 'engine', 9999, $4::jsonb, '2000-01-01', NOW()
			WHERE NOT EXISTS (
				SELECT 1 FROM entries
				WHERE entity_id = $1::uuid AND scope = 'system' AND direction = $3
			)
		`, entityID, labelName, dir, conds)
		if err != nil {
			return err
		}
	}
	return nil
}
