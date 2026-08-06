package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	proposalTypeNew   = "new"
	proposalTypeEnded = "ended"
	proposalTypeDrift = "drift"
)

// EntryProposalRow is a full entry_proposals row joined with enough entry data
// to render the review card without a second query.
type EntryProposalRow struct {
	// proposal fields
	ID              string     `db:"id"`
	EntityID        string     `db:"entity_id"`
	ProposalType    string     `db:"proposal_type"`
	Status          string     `db:"status"`
	ReferenceEntryID string    `db:"reference_entry_id"`
	ProposedEndDate *time.Time `db:"proposed_end_date"`
	CreatedAt       time.Time  `db:"created_at"`

	// joined entry fields (for review card display)
	EntryDirection       string    `db:"entry_direction"`
	EntryType            string    `db:"entry_type"`
	EntryLabelName       *string   `db:"entry_label_name"`
	EntryFitness         *float64  `db:"entry_fitness"`
	EntryMerchantFit     *float64  `db:"entry_merchant_fit"`
	EntryTimingFit       *float64  `db:"entry_timing_fit"`
	EntryAmountFit       *float64  `db:"entry_amount_fit"`
	EntryMatchedCount    *int      `db:"entry_matched_count"`
	EntrySampleMerchants []string  `db:"entry_sample_merchants"`
	EntryProjectedRate   *float64  `db:"entry_projected_rate"`
	EntryPeriodDays      *int      `db:"entry_period_days"`
	EntrySource          string    `db:"entry_source"`
}

// ListPendingProposals returns all pending proposals for an entity,
// joined to their referenced entry's display fields.
func (s *Store) ListPendingProposals(ctx context.Context, entityID string) ([]EntryProposalRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			ep.id::text, ep.entity_id::text, ep.proposal_type, ep.status,
			ep.reference_entry_id::text, ep.proposed_end_date, ep.created_at,
			e.direction            AS entry_direction,
			e.entry_type           AS entry_type,
			l.name                 AS entry_label_name,
			e.fitness              AS entry_fitness,
			e.merchant_fit         AS entry_merchant_fit,
			e.timing_fit           AS entry_timing_fit,
			e.amount_fit           AS entry_amount_fit,
			e.matched_transaction_count AS entry_matched_count,
			e.sample_merchants     AS entry_sample_merchants,
			e.projected_rate_per_day::float8 AS entry_projected_rate,
			e.period_days          AS entry_period_days,
			e.source               AS entry_source
		FROM entry_proposals ep
		JOIN entries e ON e.id = ep.reference_entry_id
		LEFT JOIN labels l ON l.id = e.label_id
		WHERE ep.entity_id = $1
		  AND ep.status = 'pending'
		ORDER BY ep.created_at ASC
	`, entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[EntryProposalRow])
}

// GetProposalType loads only the proposal_type for routing approve/reject dispatch.
func (s *Store) GetProposalType(ctx context.Context, entityID, proposalID string, out *string) error {
	return s.pool.QueryRow(ctx, `
		SELECT proposal_type FROM entry_proposals
		WHERE entity_id = $1 AND id = $2::uuid AND status = 'pending'
	`, entityID, proposalID).Scan(out)
}

// ApproveNewProposal accepts a 'new' proposal: marks it accepted and records the reviewer.
// The referenced entry is already live — no changes to entries needed.
func (s *Store) ApproveNewProposal(ctx context.Context, entityID, proposalID, userID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE entry_proposals
		SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2::uuid AND proposal_type = 'new' AND status = 'pending'
	`, entityID, proposalID, userID)
	return err
}

// RejectNewProposal hard-deletes the referenced entry (and its proposal, via FK
// ON DELETE CASCADE). The entry was engine-detected and never user-approved,
// so no historical record is warranted.
func (s *Store) RejectNewProposal(ctx context.Context, entityID, proposalID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var entryID string
	err = tx.QueryRow(ctx, `
		SELECT reference_entry_id::text
		FROM entry_proposals
		WHERE entity_id = $1 AND id = $2::uuid
		  AND proposal_type = 'new' AND status = 'pending'
	`, entityID, proposalID).Scan(&entryID)
	if err != nil {
		return err
	}

	// Hard delete the entry. The proposal row is deleted via FK ON DELETE CASCADE.
	// transaction_entry_assignments for this entry are also cascade-deleted.
	if _, err = tx.Exec(ctx, `
		DELETE FROM entries WHERE entity_id = $1 AND id = $2::uuid
	`, entityID, entryID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ApproveEndedProposal accepts an 'ended' proposal: sets end_date on the referenced
// entry and marks the proposal accepted.
func (s *Store) ApproveEndedProposal(ctx context.Context, entityID, proposalID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var entryID string
	var proposedEndDate *time.Time
	err = tx.QueryRow(ctx, `
		SELECT reference_entry_id::text, proposed_end_date
		FROM entry_proposals
		WHERE entity_id = $1 AND id = $2::uuid
		  AND proposal_type = 'ended' AND status = 'pending'
	`, entityID, proposalID).Scan(&entryID, &proposedEndDate)
	if err != nil {
		return err
	}

	endDate := proposedEndDate
	if endDate == nil {
		now := time.Now().UTC()
		endDate = &now
	}

	if _, err = tx.Exec(ctx, `
		UPDATE entries SET end_date = $2
		WHERE entity_id = $1 AND id = $3::uuid
	`, entityID, *endDate, entryID); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `
		UPDATE entry_proposals
		SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2::uuid
	`, entityID, proposalID, userID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// RejectEndedProposal dismisses an 'ended' proposal and records dismissed_computed_as_of
// so the engine does not re-propose until further time has passed.
func (s *Store) RejectEndedProposal(ctx context.Context, entityID, proposalID, userID string, computedAsOf time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE entry_proposals
		SET status = 'dismissed',
			dismissed_computed_as_of = $3,
			reviewed_by = $4::uuid,
			reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2::uuid
		  AND proposal_type = 'ended' AND status = 'pending'
	`, entityID, proposalID, computedAsOf, userID)
	return err
}

// ApproveDriftProposalInPlace accepts a drift proposal by updating the
// entry's projected_rate_per_day to the proposed value in place.
// The entry keeps its start_date, label, and all other fields unchanged.
func (s *Store) ApproveDriftProposalInPlace(ctx context.Context, entityID, proposalID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var entryID string
	var proposedRate float64
	err = tx.QueryRow(ctx, `
		SELECT ep.reference_entry_id::text, ep.proposed_rate_per_day::float8
		FROM entry_proposals ep
		WHERE ep.entity_id = $1 AND ep.id = $2::uuid
		  AND ep.proposal_type = 'drift' AND ep.status = 'pending'
	`, entityID, proposalID).Scan(&entryID, &proposedRate)
	if err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `
		UPDATE entries SET projected_rate_per_day = $2
		WHERE entity_id = $1 AND id = $3::uuid
	`, entityID, proposedRate, entryID); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `
		UPDATE entry_proposals
		SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2::uuid
	`, entityID, proposalID, userID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// ApproveDriftProposalCloseNew accepts a drift proposal by closing the current
// entry (end_date = yesterday) and creating a new entry that inherits all fields
// except projected_rate_per_day, start_date, end_date, and next_due_date.
// Returns the new entry's ID.
func (s *Store) ApproveDriftProposalCloseNew(ctx context.Context, entityID, proposalID, userID string) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var entryID string
	var proposedRate float64
	err = tx.QueryRow(ctx, `
		SELECT ep.reference_entry_id::text, ep.proposed_rate_per_day::float8
		FROM entry_proposals ep
		WHERE ep.entity_id = $1 AND ep.id = $2::uuid
		  AND ep.proposal_type = 'drift' AND ep.status = 'pending'
	`, entityID, proposalID).Scan(&entryID, &proposedRate)
	if err != nil {
		return "", err
	}

	var (
		labelID          *string
		direction        string
		entryType        string
		periodDays       *int
		recurrenceAnchor *string
		conditions       *string
		rateMethod       string
		fitness          *float64
		source           string
	)
	err = tx.QueryRow(ctx, `
		SELECT
			label_id::text, direction, entry_type, period_days,
			recurrence_anchor, conditions::text, rate_method,
			fitness, source
		FROM entries
		WHERE entity_id = $1 AND id = $2::uuid
	`, entityID, entryID).Scan(
		&labelID, &direction, &entryType, &periodDays,
		&recurrenceAnchor, &conditions, &rateMethod,
		&fitness, &source,
	)
	if err != nil {
		return "", err
	}

	// Close the old entry: set end_date to yesterday.
	if _, err = tx.Exec(ctx, `
		UPDATE entries
		SET end_date = (NOW()::date - 1)
		WHERE entity_id = $1 AND id = $2::uuid
	`, entityID, entryID); err != nil {
		return "", err
	}

	var newEntryID string
	err = tx.QueryRow(ctx, `
		INSERT INTO entries (
			entity_id, label_id, direction, entry_type, period_days,
			recurrence_anchor, conditions, projected_rate_per_day,
			rate_method, fitness, source, start_date
		) VALUES (
			$1, $2::uuid, $3, $4, $5,
			$6, $7::jsonb, $8,
			$9, $10, $11, NOW()::date
		)
		RETURNING id::text
	`, entityID, labelID, direction, entryType, periodDays,
		recurrenceAnchor, conditions, proposedRate,
		rateMethod, fitness, source,
	).Scan(&newEntryID)
	if err != nil {
		return "", err
	}

	if _, err = tx.Exec(ctx, `
		UPDATE entry_proposals
		SET status = 'accepted', reviewed_by = $3::uuid, reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2::uuid
	`, entityID, proposalID, userID); err != nil {
		return "", err
	}

	return newEntryID, tx.Commit(ctx)
}

// RejectDriftProposal dismisses a drift proposal and records the current
// matched_transaction_count as the re-trigger threshold.
func (s *Store) RejectDriftProposal(ctx context.Context, entityID, proposalID, userID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var matchedCount int
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(e.matched_transaction_count, 0)
		FROM entry_proposals ep
		JOIN entries e ON e.id = ep.reference_entry_id
		WHERE ep.entity_id = $1 AND ep.id = $2::uuid
		  AND ep.proposal_type = 'drift' AND ep.status = 'pending'
	`, entityID, proposalID).Scan(&matchedCount)
	if err != nil {
		return err
	}

	if _, err = tx.Exec(ctx, `
		UPDATE entry_proposals
		SET status = 'dismissed',
			dismissed_transaction_count = $3,
			reviewed_by = $4::uuid,
			reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2::uuid
	`, entityID, proposalID, matchedCount, userID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
