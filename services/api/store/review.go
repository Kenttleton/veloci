package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReviewItem represents a row from the review_queue table.
type ReviewItem struct {
	ID                    string          `db:"id"`
	EntityID              string          `db:"entity_id"`
	EntryID               string          `db:"entry_id"`
	JobID                 string          `db:"job_id"`
	SuggestedName         *string         `db:"suggested_name"`
	SuggestedEntryType    *string         `db:"suggested_entry_type"`
	SuggestedConditions   json.RawMessage `db:"suggested_conditions"`
	SuggestedRatePerDay   *float64        `db:"suggested_rate_per_day"`
	MatchedTransactionCount int           `db:"matched_transaction_count"`
	AlertType             string          `db:"alert_type"`
	Confidence            *float64        `db:"confidence"`
	MerchantConfidence    *float64        `db:"merchant_confidence"`
	TimingConfidence      *float64        `db:"timing_confidence"`
	AmountConfidence      *float64        `db:"amount_confidence"`
	SampleMerchants       []string        `db:"sample_merchants"`
	Status                string          `db:"status"`
	ReviewedBy            *string         `db:"reviewed_by"`
	ReviewedAt            *time.Time      `db:"reviewed_at"`
}

const reviewCols = `
	id::text, entity_id::text, entry_id::text, job_id::text,
	suggested_name, suggested_entry_type, suggested_conditions,
	suggested_rate_per_day, matched_transaction_count, alert_type,
	confidence, merchant_confidence, timing_confidence, amount_confidence,
	sample_merchants, status, reviewed_by::text, reviewed_at
`

// GetReviewItem fetches a single review_queue item.
func (s *Store) GetReviewItem(ctx context.Context, entityID, id string) (ReviewItem, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM review_queue
		WHERE entity_id = $1 AND id = $2
	`, reviewCols), entityID, id)
	if err != nil {
		return ReviewItem{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[ReviewItem])
}

// ListReviewItems returns a paginated list of review_queue items for an entity.
func (s *Store) ListReviewItems(ctx context.Context, entityID string, limit int, cursor string) ([]ReviewItem, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM review_queue
			WHERE entity_id = $1
			ORDER BY id DESC
			LIMIT $2
		`, reviewCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[ReviewItem])
	}

	cursorID, _, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM review_queue
		WHERE entity_id = $1 AND id < $2
		ORDER BY id DESC
		LIMIT $3
	`, reviewCols), entityID, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ReviewItem])
}

// UpdateReviewStatus updates the status of a review_queue item.
func (s *Store) UpdateReviewStatus(ctx context.Context, entityID, id, status, reviewedBy string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE review_queue SET
			status = $3,
			reviewed_by = $4::uuid,
			reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2
	`, entityID, id, status, reviewedBy)
	return err
}
