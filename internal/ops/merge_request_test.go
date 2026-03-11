package ops

import (
	"testing"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/manifest"
)

// Helper to create a base original MR transaction.
func baseOriginal() arweave.MergeRequestTx {
	return arweave.MergeRequestTx{
		TxID:         "orig-tx",
		Signer:       "source-owner",
		TargetOwner:  "target-owner",
		TargetRepo:   "repo",
		SourceOwner:  "source-owner",
		SourceRepo:   "fork",
		SourceRefSha: "refsha123",
		Timestamp:    "2025-01-01T00:00:00.000Z",
	}
}

// Helper to create an update from a given signer.
func makeUpdate(txID, parentTx, signer, status string, merged bool, timestamp string, orig arweave.MergeRequestTx) ResolvedUpdate {
	return ResolvedUpdate{
		MergeRequestTx: arweave.MergeRequestTx{
			TxID:         txID,
			Signer:       signer,
			TargetOwner:  orig.TargetOwner,
			TargetRepo:   orig.TargetRepo,
			SourceOwner:  orig.SourceOwner,
			SourceRepo:   orig.SourceRepo,
			SourceRefSha: orig.SourceRefSha,
			ParentTx:     parentTx,
			Timestamp:    timestamp,
		},
		Merged: merged,
		Status: status,
	}
}

func TestResolveStatus_NoUpdates(t *testing.T) {
	orig := baseOriginal()
	status, head := resolveStatus(orig, nil)
	if status != MergeRequestOpen {
		t.Errorf("status = %q, want open", status)
	}
	if head != orig.TxID {
		t.Errorf("head = %q, want %q", head, orig.TxID)
	}
}

func TestResolveStatus_SingleClose(t *testing.T) {
	orig := baseOriginal()
	updates := []ResolvedUpdate{
		makeUpdate("close-tx", orig.TxID, orig.TargetOwner, manifest.StatusClosed, false, "2025-01-02T00:00:00.000Z", orig),
	}
	status, head := resolveStatus(orig, updates)
	if status != MergeRequestClosed {
		t.Errorf("status = %q, want closed", status)
	}
	if head != "close-tx" {
		t.Errorf("head = %q, want close-tx", head)
	}
}

func TestResolveStatus_CloseAndReopen(t *testing.T) {
	orig := baseOriginal()
	updates := []ResolvedUpdate{
		makeUpdate("close-tx", orig.TxID, orig.TargetOwner, manifest.StatusClosed, false, "2025-01-02T00:00:00.000Z", orig),
		makeUpdate("reopen-tx", "close-tx", orig.SourceOwner, manifest.StatusOpen, false, "2025-01-03T00:00:00.000Z", orig),
	}
	status, head := resolveStatus(orig, updates)
	if status != MergeRequestOpen {
		t.Errorf("status = %q, want open", status)
	}
	if head != "reopen-tx" {
		t.Errorf("head = %q, want reopen-tx", head)
	}
}

func TestResolveStatus_MergedShortCircuit(t *testing.T) {
	orig := baseOriginal()
	// Merge update from target owner — should be terminal.
	updates := []ResolvedUpdate{
		makeUpdate("merge-tx", orig.TxID, orig.TargetOwner, "", true, "2025-01-02T00:00:00.000Z", orig),
	}
	status, head := resolveStatus(orig, updates)
	if status != MergeRequestMerged {
		t.Errorf("status = %q, want merged", status)
	}
	if head != "merge-tx" {
		t.Errorf("head = %q, want merge-tx", head)
	}
}

func TestResolveStatus_MergedOverridesClose(t *testing.T) {
	orig := baseOriginal()
	// Close first, then merge — merged wins (short-circuit).
	updates := []ResolvedUpdate{
		makeUpdate("close-tx", orig.TxID, orig.TargetOwner, manifest.StatusClosed, false, "2025-01-02T00:00:00.000Z", orig),
		makeUpdate("merge-tx", "close-tx", orig.TargetOwner, "", true, "2025-01-03T00:00:00.000Z", orig),
	}
	status, head := resolveStatus(orig, updates)
	if status != MergeRequestMerged {
		t.Errorf("status = %q, want merged", status)
	}
	if head != "merge-tx" {
		t.Errorf("head = %q, want merge-tx", head)
	}
}

func TestResolveStatus_MergedSanitization(t *testing.T) {
	orig := baseOriginal()
	// Source owner posts merged: true — should be sanitized (treated as regular status update).
	updates := []ResolvedUpdate{
		makeUpdate("fake-merge-tx", orig.TxID, orig.SourceOwner, manifest.StatusClosed, true, "2025-01-02T00:00:00.000Z", orig),
	}
	status, head := resolveStatus(orig, updates)
	// Should NOT be merged — source owner can't merge.
	if status == MergeRequestMerged {
		t.Error("source owner should not be able to merge")
	}
	// Should be closed (from the status field).
	if status != MergeRequestClosed {
		t.Errorf("status = %q, want closed", status)
	}
	if head != "fake-merge-tx" {
		t.Errorf("head = %q, want fake-merge-tx", head)
	}
}

func TestResolveStatus_ChainFork_TargetOwnerWins(t *testing.T) {
	orig := baseOriginal()
	// Two updates with same parent — target owner should win.
	updates := []ResolvedUpdate{
		makeUpdate("source-close", orig.TxID, orig.SourceOwner, manifest.StatusClosed, false, "2025-01-02T00:00:00.000Z", orig),
		makeUpdate("target-reopen", orig.TxID, orig.TargetOwner, manifest.StatusOpen, false, "2025-01-02T00:00:01.000Z", orig),
	}
	status, head := resolveStatus(orig, updates)
	if status != MergeRequestOpen {
		t.Errorf("status = %q, want open (target owner wins fork)", status)
	}
	if head != "target-reopen" {
		t.Errorf("head = %q, want target-reopen", head)
	}
}

func TestResolveStatus_ChainFork_SameSignerLaterWins(t *testing.T) {
	orig := baseOriginal()
	// Two updates from same signer, same parent — later timestamp wins.
	updates := []ResolvedUpdate{
		makeUpdate("early-close", orig.TxID, orig.SourceOwner, manifest.StatusClosed, false, "2025-01-02T00:00:00.000Z", orig),
		makeUpdate("late-reopen", orig.TxID, orig.SourceOwner, manifest.StatusOpen, false, "2025-01-02T00:01:00.000Z", orig),
	}
	status, head := resolveStatus(orig, updates)
	if status != MergeRequestOpen {
		t.Errorf("status = %q, want open (later timestamp wins)", status)
	}
	if head != "late-reopen" {
		t.Errorf("head = %q, want late-reopen", head)
	}
}

func TestResolveStatus_MultiStepChain(t *testing.T) {
	orig := baseOriginal()
	updates := []ResolvedUpdate{
		makeUpdate("u1", orig.TxID, orig.SourceOwner, manifest.StatusClosed, false, "2025-01-02T00:00:00.000Z", orig),
		makeUpdate("u2", "u1", orig.TargetOwner, manifest.StatusOpen, false, "2025-01-03T00:00:00.000Z", orig),
		makeUpdate("u3", "u2", orig.SourceOwner, manifest.StatusClosed, false, "2025-01-04T00:00:00.000Z", orig),
	}
	status, head := resolveStatus(orig, updates)
	if status != MergeRequestClosed {
		t.Errorf("status = %q, want closed", status)
	}
	if head != "u3" {
		t.Errorf("head = %q, want u3", head)
	}
}

func TestIsValidUpdate_ValidFromTargetOwner(t *testing.T) {
	orig := baseOriginal()
	u := makeUpdate("u1", orig.TxID, orig.TargetOwner, manifest.StatusClosed, false, "ts", orig)
	if !isValidUpdate(u, orig) {
		t.Error("expected valid update from target owner")
	}
}

func TestIsValidUpdate_ValidFromSourceOwner(t *testing.T) {
	orig := baseOriginal()
	u := makeUpdate("u1", orig.TxID, orig.SourceOwner, manifest.StatusClosed, false, "ts", orig)
	if !isValidUpdate(u, orig) {
		t.Error("expected valid update from source owner")
	}
}

func TestIsValidUpdate_RejectsUnauthorizedSigner(t *testing.T) {
	orig := baseOriginal()
	u := makeUpdate("u1", orig.TxID, "random-person", manifest.StatusClosed, false, "ts", orig)
	if isValidUpdate(u, orig) {
		t.Error("expected invalid update from unauthorized signer")
	}
}

func TestIsValidUpdate_RejectsNoParentTx(t *testing.T) {
	orig := baseOriginal()
	u := makeUpdate("u1", "", orig.TargetOwner, manifest.StatusClosed, false, "ts", orig)
	if isValidUpdate(u, orig) {
		t.Error("expected invalid update without Parent-Tx")
	}
}

func TestIsValidUpdate_RejectsMismatchedTags(t *testing.T) {
	orig := baseOriginal()

	tests := []struct {
		name   string
		modify func(*ResolvedUpdate)
	}{
		{"TargetOwner", func(u *ResolvedUpdate) { u.TargetOwner = "wrong" }},
		{"TargetRepo", func(u *ResolvedUpdate) { u.TargetRepo = "wrong" }},
		{"SourceOwner", func(u *ResolvedUpdate) { u.SourceOwner = "wrong" }},
		{"SourceRepo", func(u *ResolvedUpdate) { u.SourceRepo = "wrong" }},
		{"SourceRefSha", func(u *ResolvedUpdate) { u.SourceRefSha = "wrong" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := makeUpdate("u1", orig.TxID, orig.TargetOwner, manifest.StatusClosed, false, "ts", orig)
			tt.modify(&u)
			// Fix signer to match the modified SourceOwner if needed.
			if tt.name == "SourceOwner" {
				u.Signer = orig.TargetOwner
			}
			if isValidUpdate(u, orig) {
				t.Errorf("expected invalid update with mismatched %s", tt.name)
			}
		})
	}
}

func TestSplitOriginalsAndUpdates(t *testing.T) {
	txs := []arweave.MergeRequestTx{
		{TxID: "orig1", ParentTx: ""},
		{TxID: "update1", ParentTx: "orig1"},
		{TxID: "orig2", ParentTx: ""},
		{TxID: "update2", ParentTx: "orig2"},
		{TxID: "update3", ParentTx: "update1"},
	}

	originals, updates := splitOriginalsAndUpdates(txs)
	if len(originals) != 2 {
		t.Errorf("originals count = %d, want 2", len(originals))
	}
	if len(updates) != 3 {
		t.Errorf("updates count = %d, want 3", len(updates))
	}
}

func TestResolveChainFork_TargetOwnerPriority(t *testing.T) {
	candidates := []ResolvedUpdate{
		{MergeRequestTx: arweave.MergeRequestTx{TxID: "source-tx", Signer: "source", Timestamp: "2025-01-02T00:00:00.000Z"}, Status: manifest.StatusClosed},
		{MergeRequestTx: arweave.MergeRequestTx{TxID: "target-tx", Signer: "target", Timestamp: "2025-01-01T00:00:00.000Z"}, Status: manifest.StatusOpen},
	}

	winner := resolveChainFork(candidates, "target")
	if winner.TxID != "target-tx" {
		t.Errorf("winner = %q, want target-tx (target owner priority)", winner.TxID)
	}
}

func TestResolveChainFork_SameSignerLaterTimestamp(t *testing.T) {
	candidates := []ResolvedUpdate{
		{MergeRequestTx: arweave.MergeRequestTx{TxID: "early", Signer: "source", Timestamp: "2025-01-01T00:00:00.000Z"}, Status: manifest.StatusClosed},
		{MergeRequestTx: arweave.MergeRequestTx{TxID: "late", Signer: "source", Timestamp: "2025-01-02T00:00:00.000Z"}, Status: manifest.StatusOpen},
	}

	winner := resolveChainFork(candidates, "target")
	if winner.TxID != "late" {
		t.Errorf("winner = %q, want late (later timestamp)", winner.TxID)
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"single line", "single line"},
		{"first\nsecond\nthird", "first"},
		{"", ""},
		{"title\n", "title"},
	}
	for _, tt := range tests {
		got := firstLine(tt.input)
		if got != tt.want {
			t.Errorf("firstLine(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestResolveStatus_IgnoresUpdatesForDifferentMR(t *testing.T) {
	orig := baseOriginal()
	// Update with different SourceRefSha — should be filtered out.
	u := makeUpdate("u1", orig.TxID, orig.TargetOwner, manifest.StatusClosed, false, "ts", orig)
	u.SourceRefSha = "different-sha"

	status, head := resolveStatus(orig, []ResolvedUpdate{u})
	if status != MergeRequestOpen {
		t.Errorf("status = %q, want open (mismatched update should be ignored)", status)
	}
	if head != orig.TxID {
		t.Errorf("head = %q, want %q", head, orig.TxID)
	}
}

func TestResolveStatus_IgnoresUnauthorizedSigner(t *testing.T) {
	orig := baseOriginal()
	u := makeUpdate("u1", orig.TxID, "random-person", manifest.StatusClosed, false, "ts", orig)
	u.Signer = "random-person"

	status, head := resolveStatus(orig, []ResolvedUpdate{u})
	if status != MergeRequestOpen {
		t.Errorf("status = %q, want open (unauthorized signer should be ignored)", status)
	}
	if head != orig.TxID {
		t.Errorf("head = %q, want %q", head, orig.TxID)
	}
}
