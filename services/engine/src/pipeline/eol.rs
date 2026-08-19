//! End-of-life (EOL) detection utilities shared across pipeline stages.
//!
//! Stage 2 uses these at entry creation time to stamp `end_date` when a
//! cluster's pattern has already ended before the current import date.
//! Stage 7 uses them to detect live/pending entries whose signal has lapsed
//! as `computed_as_of` advances with each new import.

use chrono::{Duration, NaiveDate};

/// Returns `true` when `computed_as_of` has passed `next_due` by more than
/// `tolerance_days`. Pass [`super::TIMING_VARIANCE_THRESHOLD_DAYS`] as `i64`
/// for `tolerance_days`.
pub(crate) fn entry_has_lapsed(
    next_due:       NaiveDate,
    computed_as_of: NaiveDate,
    tolerance_days: i64,
) -> bool {
    computed_as_of > next_due + Duration::days(tolerance_days)
}

/// Compute the `end_date` for an entry if its signal has lapsed, or `None`
/// if it is still within its active window.
///
/// - If `next_due_date` is Some (standing entries): lapse is detected when
///   `computed_as_of > next_due_date + tolerance`. Returns `next_due_date`
///   — the last expected but unfulfilled occurrence.
/// - If `next_due_date` is None (variable / one-off entries): uses
///   `last_tx_date + period_days` as the natural window end. Lapse is
///   detected when `computed_as_of > window_end + tolerance`. Returns
///   `window_end`.
/// - Returns `None` if neither condition applies, or if `last_tx_date` is
///   None for an entry without a `next_due_date`.
pub(crate) fn compute_end_date_if_lapsed(
    next_due_date:  Option<NaiveDate>,
    last_tx_date:   Option<NaiveDate>,
    period_days:    i32,
    computed_as_of: NaiveDate,
    tolerance_days: i64,
) -> Option<NaiveDate> {
    if let Some(next_due) = next_due_date {
        if entry_has_lapsed(next_due, computed_as_of, tolerance_days) {
            return Some(next_due);
        }
        return None;
    }

    let last_tx    = last_tx_date?;
    let window_end = last_tx + Duration::days(i64::from(period_days));
    if entry_has_lapsed(window_end, computed_as_of, tolerance_days) {
        Some(window_end)
    } else {
        None
    }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    fn d(s: &str) -> NaiveDate { s.parse().unwrap() }

    // ── entry_has_lapsed ─────────────────────────────────────────────────────

    #[test]
    fn on_next_due_not_lapsed() { assert!(!entry_has_lapsed(d("2026-03-15"), d("2026-03-15"), 5)); }

    #[test]
    fn within_tolerance_not_lapsed() { assert!(!entry_has_lapsed(d("2026-03-15"), d("2026-03-18"), 5)); }

    #[test]
    fn at_boundary_not_lapsed() { assert!(!entry_has_lapsed(d("2026-03-15"), d("2026-03-20"), 5)); }

    #[test]
    fn one_past_tolerance_lapsed() { assert!(entry_has_lapsed(d("2026-03-15"), d("2026-03-21"), 5)); }

    #[test]
    fn far_past_lapsed() { assert!(entry_has_lapsed(d("2026-03-01"), d("2026-04-15"), 5)); }

    #[test]
    fn before_next_due_not_lapsed() { assert!(!entry_has_lapsed(d("2026-03-15"), d("2026-03-01"), 5)); }

    // ── compute_end_date_if_lapsed ────────────────────────────────────────────

    #[test]
    fn standing_lapsed_returns_next_due() {
        let end = compute_end_date_if_lapsed(
            Some(d("2026-01-15")), None, 30, d("2026-03-01"), 5,
        );
        assert_eq!(end, Some(d("2026-01-15")));
    }

    #[test]
    fn standing_not_lapsed_returns_none() {
        let end = compute_end_date_if_lapsed(
            Some(d("2026-03-01")), None, 30, d("2026-03-04"), 5,
        );
        assert_eq!(end, None);
    }

    #[test]
    fn variable_lapsed_returns_window_end() {
        // last_tx = 2026-01-01, period = 30 → window_end = 2026-01-31
        // computed_as_of = 2026-03-01 > 2026-01-31 + 5 = 2026-02-05 → lapsed
        let end = compute_end_date_if_lapsed(
            None, Some(d("2026-01-01")), 30, d("2026-03-01"), 5,
        );
        assert_eq!(end, Some(d("2026-01-31")));
    }

    #[test]
    fn variable_not_lapsed_returns_none() {
        // last_tx = 2026-02-25, period = 30 → window_end = 2026-03-27
        // computed_as_of = 2026-03-01 is before window_end + tolerance → not lapsed
        let end = compute_end_date_if_lapsed(
            None, Some(d("2026-02-25")), 30, d("2026-03-01"), 5,
        );
        assert_eq!(end, None);
    }

    #[test]
    fn no_last_tx_returns_none() {
        let end = compute_end_date_if_lapsed(None, None, 30, d("2026-03-01"), 5);
        assert_eq!(end, None);
    }
}
