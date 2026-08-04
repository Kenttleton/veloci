package store

import (
	"testing"
)

func TestSortSystemEntries(t *testing.T) {
	name := func(s string) *string { return &s }

	entries := []EntryRow{
		{ID: "c", LabelName: name("Spend")},
		{ID: "a", LabelName: name("All")},
		{ID: "x", LabelName: name("Unrecognized")},
		{ID: "b", LabelName: name("Income")},
	}

	sortSystemEntries(entries)

	want := []string{"All", "Income", "Spend", "Unrecognized"}
	for i, e := range entries {
		got := ""
		if e.LabelName != nil {
			got = *e.LabelName
		}
		if got != want[i] {
			t.Errorf("position %d: got %q, want %q", i, got, want[i])
		}
	}
}

func TestSortSystemEntriesNilLabel(t *testing.T) {
	entries := []EntryRow{
		{ID: "b", LabelName: nil},
		{ID: "a", LabelName: func() *string { s := "Income"; return &s }()},
	}

	sortSystemEntries(entries)

	// "Income" is mapped (rank 1), nil label is unmapped — Income should come first
	if entries[0].ID != "a" {
		t.Errorf("expected Income (mapped) first, got ID=%q", entries[0].ID)
	}
}
