package store

import (
	"testing"
)

func TestProposalTypeConstants(t *testing.T) {
	// Verify the proposal type constants used throughout the store are correct strings.
	if proposalTypeNew != "new" {
		t.Errorf("expected 'new', got %q", proposalTypeNew)
	}
	if proposalTypeEnded != "ended" {
		t.Errorf("expected 'ended', got %q", proposalTypeEnded)
	}
}

func TestDriftProposalTypeConstant(t *testing.T) {
	if proposalTypeDrift != "drift" {
		t.Errorf("expected 'drift', got %q", proposalTypeDrift)
	}
}
