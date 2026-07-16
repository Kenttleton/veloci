package store

import (
	"fmt"
	"strings"
	"time"
)

// DateRange is the resolved date filter passed to list queries.
// From and To are explicit YYYY-MM-DD bounds (empty = no bound).
// SpanInterval is a Postgres interval string used only when no explicit
// bounds could be computed — the store anchors it to MAX(date) via subquery.
type DateRange struct {
	From         string // "YYYY-MM-DD" or ""
	To           string // "YYYY-MM-DD" or ""
	SpanInterval string // e.g. "30 days", "1 months 15 days" — non-empty only when span-only
}

// ResolveRange computes an effective DateRange from any combination of explicit
// dates and span components. Resolution rules:
//
//   - From + To          → use both directly
//   - From + span        → To = From + span (Go date math)
//   - To + span          → From = To - span (Go date math)
//   - span only          → SpanInterval set; store anchors on MAX(date)
//   - From only / To only → single bound, other is open
//   - nothing            → zero DateRange (no filter)
func ResolveRange(dateFrom, dateTo string, spanDays, spanMonths, spanYears int) DateRange {
	hasSpan := spanDays > 0 || spanMonths > 0 || spanYears > 0

	var spanInterval string
	if hasSpan {
		parts := []string{}
		if spanYears > 0 {
			parts = append(parts, fmt.Sprintf("%d years", spanYears))
		}
		if spanMonths > 0 {
			parts = append(parts, fmt.Sprintf("%d months", spanMonths))
		}
		if spanDays > 0 {
			parts = append(parts, fmt.Sprintf("%d days", spanDays))
		}
		spanInterval = strings.Join(parts, " ")
	}

	hasFrom := dateFrom != ""
	hasTo := dateTo != ""

	// Both explicit bounds — span ignored.
	if hasFrom && hasTo {
		return DateRange{From: dateFrom, To: dateTo}
	}

	if hasSpan {
		if hasFrom {
			// Compute To = From + span.
			t, err := time.Parse("2006-01-02", dateFrom)
			if err == nil {
				to := t.AddDate(spanYears, spanMonths, spanDays).Format("2006-01-02")
				return DateRange{From: dateFrom, To: to}
			}
		}
		if hasTo {
			// Compute From = To - span.
			t, err := time.Parse("2006-01-02", dateTo)
			if err == nil {
				from := t.AddDate(-spanYears, -spanMonths, -spanDays).Format("2006-01-02")
				return DateRange{From: from, To: dateTo}
			}
		}
		// Span only — store will anchor on MAX(date).
		return DateRange{SpanInterval: spanInterval}
	}

	return DateRange{From: dateFrom, To: dateTo}
}
