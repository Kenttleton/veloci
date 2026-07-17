// Package response provides the standard envelope used by all veloci-api endpoints.
//
// Every non-empty response body is wrapped as:
//
//	{ "data": T, "meta": {} }                        — single object
//	{ "data": [T], "meta": { "next_cursor", ... } }  — paginated list
//
// Errors use Huma's native RFC 7807 problem+json format and are never wrapped.
package response

// Meta carries pagination state. All fields are omitted when empty so non-paginated
// responses serialize to the literal {"meta":{}}.
type Meta struct {
	NextCursor *string `json:"next_cursor,omitempty"`
	Limit      *int    `json:"limit,omitempty"`
	HasMore    *bool   `json:"has_more,omitempty"`
}

// Envelope wraps any response value in the standard { "data", "meta" } shape.
// T may be a struct (single resource) or a slice (list resource).
type Envelope[T any] struct {
	Data T    `json:"data"`
	Meta Meta `json:"meta"`
}

// Single wraps a value with empty pagination metadata.
func Single[T any](data T) Envelope[T] {
	return Envelope[T]{Data: data}
}

// Page wraps a slice with full pagination metadata.
// Pass nextCursor as nil when there are no further pages.
func Page[T any](data T, nextCursor *string, limit int, hasMore bool) Envelope[T] {
	l := limit
	hm := hasMore
	return Envelope[T]{
		Data: data,
		Meta: Meta{
			NextCursor: nextCursor,
			Limit:      &l,
			HasMore:    &hm,
		},
	}
}
