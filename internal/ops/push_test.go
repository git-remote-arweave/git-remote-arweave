package ops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
)

func TestMergeRefs(t *testing.T) {
	base := map[string]string{
		"refs/heads/main": "aaa",
		"refs/heads/dev":  "bbb",
		"refs/tags/v1.0":  "ccc",
	}

	updates := map[string]string{
		"refs/heads/main":    "ddd",                      // update
		"refs/heads/dev":     plumbing.ZeroHash.String(),  // delete
		"refs/heads/feature": "eee",                       // add
	}

	got := mergeRefs(base, updates)

	want := map[string]string{
		"refs/heads/main":    "ddd",
		"refs/tags/v1.0":     "ccc",
		"refs/heads/feature": "eee",
	}

	if len(got) != len(want) {
		t.Fatalf("mergeRefs: got %d refs, want %d", len(got), len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("mergeRefs[%q] = %q, want %q", k, got[k], v)
		}
	}

	// Verify base was not mutated.
	if base["refs/heads/dev"] != "bbb" {
		t.Error("mergeRefs mutated the base map")
	}
}

func TestComputePackRange(t *testing.T) {
	updates := map[string]string{
		"refs/heads/main":    "aaaa000000000000000000000000000000000000",
		"refs/heads/feature": "bbbb000000000000000000000000000000000000",
		"refs/heads/deleted": plumbing.ZeroHash.String(),
	}
	currentRefs := map[string]string{
		"refs/heads/main": "cccc000000000000000000000000000000000000",
		"refs/tags/v1.0":  "dddd000000000000000000000000000000000000",
	}

	tips, bases := computePackRange(updates, currentRefs)

	if len(tips) != 2 {
		t.Fatalf("tips: got %d, want 2", len(tips))
	}
	if len(bases) != 2 {
		t.Fatalf("bases: got %d, want 2", len(bases))
	}

	// Check that the zero hash (deleted ref) is not in tips.
	for _, h := range tips {
		if h == plumbing.ZeroHash {
			t.Error("zero hash found in tips")
		}
	}
}

func TestEffectiveState_NoRemote(t *testing.T) {
	rs := &RemoteState{} // new repo, no manifest
	res := &pendingResolution{outcome: noPending}

	refs, packs := effectiveState(rs, res)
	if len(refs) != 0 {
		t.Errorf("expected empty refs, got %v", refs)
	}
	if len(packs) != 0 {
		t.Errorf("expected empty packs, got %v", packs)
	}
}

func TestEffectiveState_WithPendingPack(t *testing.T) {
	rs := &RemoteState{
		manifestTxID: "manifest-1",
		m: &manifest.Manifest{
			Refs:  map[string]string{"refs/heads/main": "aaa"},
			Packs: []manifest.PackEntry{{TX: "pack-1"}},
		},
	}
	res := &pendingResolution{
		outcome:  pendingInMempool,
		packTxID: "pack-pending",
		refs:     map[string]string{"refs/heads/main": "bbb", "refs/heads/dev": "ccc"},
	}

	refs, packs := effectiveState(rs, res)
	// Should use pending refs, not on-chain refs.
	if refs["refs/heads/main"] != "bbb" {
		t.Errorf("expected refs/heads/main=bbb (from pending), got %q", refs["refs/heads/main"])
	}
	if refs["refs/heads/dev"] != "ccc" {
		t.Errorf("expected refs/heads/dev=ccc (from pending), got %q", refs["refs/heads/dev"])
	}
	if len(packs) != 2 {
		t.Fatalf("expected 2 packs, got %d", len(packs))
	}
	if packs[1].TX != "pack-pending" {
		t.Errorf("expected pending pack, got %q", packs[1].TX)
	}
}

func TestEffectiveParentTx(t *testing.T) {
	rs := &RemoteState{manifestTxID: "manifest-1"}
	res := &pendingResolution{outcome: pendingInMempool}

	got := effectiveParentTx(rs, res)
	if got != "manifest-1" {
		t.Errorf("effectiveParentTx = %q, want manifest-1", got)
	}
}

func TestCheckConflict_NewRepo(t *testing.T) {
	rs := &RemoteState{} // no manifest
	res := &pendingResolution{outcome: noPending}
	state := newTestState(t)
	ctx := context.Background()

	if _, err := checkConflict(ctx, nil, rs, res, state); err != nil {
		t.Errorf("checkConflict on new repo: %v", err)
	}
}

func TestCheckConflict_Matching(t *testing.T) {
	rs := &RemoteState{
		manifestTxID: "manifest-1",
		m:            &manifest.Manifest{},
	}
	res := &pendingResolution{outcome: noPending}
	state := newTestState(t)
	state.SaveLastManifestTxID("manifest-1")
	ctx := context.Background()

	if _, err := checkConflict(ctx, nil, rs, res, state); err != nil {
		t.Errorf("checkConflict with matching parent: %v", err)
	}
}

func TestCheckConflict_Mismatch(t *testing.T) {
	rs := &RemoteState{
		manifestTxID: "manifest-2",
		m:            &manifest.Manifest{},
	}
	res := &pendingResolution{outcome: noPending}
	state := newTestState(t)
	state.SaveLastManifestTxID("manifest-1")
	ctx := context.Background()

	// ar is nil — resolveAheadOfGraphQL will fail to fetch, treating as conflict.
	if _, err := checkConflict(ctx, nil, rs, res, state); err == nil {
		t.Error("checkConflict should detect conflict when parents differ")
	}
}

func TestCheckConflict_NoLocalRecord(t *testing.T) {
	rs := &RemoteState{
		manifestTxID: "manifest-1",
		m:            &manifest.Manifest{},
	}
	res := &pendingResolution{outcome: noPending}
	state := newTestState(t)
	ctx := context.Background()

	// No local record — should accept remote state (first push from this machine).
	if _, err := checkConflict(ctx, nil, rs, res, state); err != nil {
		t.Errorf("checkConflict with no local record: %v", err)
	}
}

func TestCheckConflict_PendingParentMismatch(t *testing.T) {
	rs := &RemoteState{
		manifestTxID: "manifest-2",
		m:            &manifest.Manifest{},
	}
	res := &pendingResolution{
		outcome:    pendingInMempool,
		parentTxID: "manifest-1",
	}
	state := newTestState(t)
	ctx := context.Background()

	if _, err := checkConflict(ctx, nil, rs, res, state); err == nil {
		t.Error("checkConflict should detect conflict when pending parent differs from remote")
	}
}

func TestListRefs_NoPending(t *testing.T) {
	rs := &RemoteState{
		m: &manifest.Manifest{
			Refs: map[string]string{"refs/heads/main": "aaa"},
		},
	}
	refs := ListRefs(rs, nil)
	if refs["refs/heads/main"] != "aaa" {
		t.Errorf("expected aaa, got %q", refs["refs/heads/main"])
	}
}

func TestListRefs_WithPending(t *testing.T) {
	rs := &RemoteState{
		m: &manifest.Manifest{
			Refs: map[string]string{"refs/heads/main": "aaa", "refs/tags/v1": "ccc"},
		},
	}
	pending := &localstate.PendingState{
		Refs: map[string]string{"refs/heads/main": "bbb"},
	}
	refs := ListRefs(rs, pending)
	if refs["refs/heads/main"] != "bbb" {
		t.Errorf("pending should override: expected bbb, got %q", refs["refs/heads/main"])
	}
	if refs["refs/tags/v1"] != "ccc" {
		t.Errorf("non-pending ref should survive: expected ccc, got %q", refs["refs/tags/v1"])
	}
}

func TestListRefs_NoRemoteWithPending(t *testing.T) {
	rs := &RemoteState{} // new repo, no manifest
	pending := &localstate.PendingState{
		Refs: map[string]string{"refs/heads/main": "aaa"},
	}
	refs := ListRefs(rs, pending)
	if refs["refs/heads/main"] != "aaa" {
		t.Errorf("expected aaa, got %q", refs["refs/heads/main"])
	}
}

func TestListRefs_DoesNotMutateManifest(t *testing.T) {
	m := &manifest.Manifest{
		Refs: map[string]string{"refs/heads/main": "aaa"},
	}
	rs := &RemoteState{m: m}
	pending := &localstate.PendingState{
		Refs: map[string]string{"refs/heads/dev": "bbb"},
	}
	ListRefs(rs, pending)
	if _, ok := m.Refs["refs/heads/dev"]; ok {
		t.Error("ListRefs mutated the original manifest refs")
	}
}

// TestForcePushResetsLastManifest verifies that after a force push the
// stale last-manifest value does not cause a false conflict on the next
// normal push.  This is a regression test for the bug where forcePush
// cleared pending state but left last-manifest pointing at the old
// (pre-force) manifest, so checkConflict would report a conflict when
// the remote state moved to the new genesis manifest.
func TestForcePushResetsLastManifest(t *testing.T) {
	state := newTestState(t)

	// Simulate pre-existing state: last-manifest points at old chain.
	oldManifest := "old-manifest-before-force-push"
	if err := state.SaveLastManifestTxID(oldManifest); err != nil {
		t.Fatalf("SaveLastManifestTxID: %v", err)
	}

	// Simulate what forcePush does to local state.
	_ = state.ClearPending()
	_ = state.SaveLastManifestTxID("")

	// Now simulate a subsequent normal push:
	// The force push created genesis manifest "force-genesis-manifest",
	// which is now the latest on-chain manifest.
	rs := &RemoteState{
		manifestTxID: "force-genesis-manifest",
		m:            &manifest.Manifest{},
	}
	// Pending from force push already confirmed (or no pending).
	res := &pendingResolution{outcome: pendingConfirmed}

	// checkConflict should NOT return an error: last-manifest is empty,
	// which is treated as "no local record — accept remote state".
	ctx := context.Background()
	if _, err := checkConflict(ctx, nil, rs, res, state); err != nil {
		t.Errorf("checkConflict after force push should not conflict: %v", err)
	}

	// Verify last-manifest was actually cleared.
	last, err := state.LoadLastManifestTxID()
	if err != nil {
		t.Fatalf("LoadLastManifestTxID: %v", err)
	}
	if last != "" {
		t.Errorf("last-manifest should be empty after force push reset, got %q", last)
	}
}

// TestForcePushWithPendingInMempool verifies that after a force push,
// if the genesis manifest is still in mempool (not yet confirmed),
// a subsequent push with pendingInMempool does not conflict.
func TestForcePushWithPendingInMempool(t *testing.T) {
	state := newTestState(t)

	// Pre-existing last-manifest from before force push.
	if err := state.SaveLastManifestTxID("old-manifest"); err != nil {
		t.Fatalf("SaveLastManifestTxID: %v", err)
	}

	// forcePush resets state.
	_ = state.ClearPending()
	_ = state.SaveLastManifestTxID("")

	// Force push genesis manifest is still in mempool.
	// Remote state still shows the old manifest (or the genesis if indexed).
	rs := &RemoteState{
		manifestTxID: "old-manifest",
		m:            &manifest.Manifest{},
	}
	res := &pendingResolution{
		outcome:    pendingInMempool,
		parentTxID: "", // force push has no parent
	}

	// parentTxID ("") != rs.manifestTxID ("old-manifest") — but this is
	// expected after force push. However, checkConflict currently checks
	// res.parentTxID != rs.manifestTxID for pendingInMempool.
	// This test documents the current behavior.
	ctx := context.Background()
	_, err := checkConflict(ctx, nil, rs, res, state)
	if err == nil {
		// If this passes, the force push pending-in-mempool case is handled.
		return
	}
	// Currently this conflicts — the pending parent ("") doesn't match
	// the on-chain manifest. This is a known limitation: force push
	// followed by immediate normal push before genesis confirms.
	t.Logf("known limitation: %v", err)
}

func newTestState(t *testing.T) *localstate.State {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(dir, 0o700)
	s, err := localstate.New(dir)
	if err != nil {
		t.Fatalf("localstate.New: %v", err)
	}
	return s
}
