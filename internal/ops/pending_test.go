package ops

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
)

func TestResolvePending_NoPending(t *testing.T) {
	state := newTestState(t)
	ctx := context.Background()

	res, err := resolvePending(ctx, nil, nil, state, 30*time.Minute, "test-repo")
	if err != nil {
		t.Fatalf("resolvePending: %v", err)
	}
	if res.outcome != noPending {
		t.Errorf("expected noPending, got %d", res.outcome)
	}
}

func TestResolvePending_GuaranteedPendingState(t *testing.T) {
	state := newTestState(t)

	pending := &localstate.PendingState{
		PackTxID:     "pack-1",
		ManifestTxID: "manifest-1",
		ParentTxID:   "parent-1",
		Refs:         map[string]string{"refs/heads/main": "aaa"},
		PackBase:     "base",
		PackTip:      "tip",
		UploadedAt:   time.Now(),
		Guaranteed:   true,
	}
	if err := state.SavePending(pending, []byte("packdata")); err != nil {
		t.Fatalf("SavePending: %v", err)
	}

	loaded, packData, err := state.LoadPending()
	if err != nil {
		t.Fatalf("LoadPending: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected pending state")
	}
	if !loaded.Guaranteed {
		t.Error("expected Guaranteed=true")
	}
	if loaded.ManifestTxID != "manifest-1" {
		t.Errorf("ManifestTxID = %q, want manifest-1", loaded.ManifestTxID)
	}
	if loaded.ParentTxID != "parent-1" {
		t.Errorf("ParentTxID = %q, want parent-1", loaded.ParentTxID)
	}
	if string(packData) != "packdata" {
		t.Errorf("packData = %q, want packdata", string(packData))
	}
}

func TestPendingResolution_FieldsSet(t *testing.T) {
	res := &pendingResolution{
		outcome:      pendingInMempool,
		packTxID:     "pack-1",
		manifestTxID: "manifest-1",
		parentTxID:   "parent-1",
		refs:         map[string]string{"refs/heads/main": "aaa"},
	}

	if res.outcome != pendingInMempool {
		t.Errorf("outcome = %d, want pendingInMempool", res.outcome)
	}
	if res.packTxID != "pack-1" {
		t.Errorf("packTxID = %q, want pack-1", res.packTxID)
	}
	if res.manifestTxID != "manifest-1" {
		t.Errorf("manifestTxID = %q, want manifest-1", res.manifestTxID)
	}
	if res.parentTxID != "parent-1" {
		t.Errorf("parentTxID = %q, want parent-1", res.parentTxID)
	}
	if res.refs["refs/heads/main"] != "aaa" {
		t.Errorf("refs[main] = %q, want aaa", res.refs["refs/heads/main"])
	}
}

func TestEffectiveState_WithReUploadedPack(t *testing.T) {
	rs := &RemoteState{
		manifestTxID: "manifest-1",
		m: &manifest.Manifest{
			Refs:  map[string]string{"refs/heads/main": "aaa"},
			Packs: []manifest.PackEntry{{TX: "pack-1"}},
		},
	}
	res := &pendingResolution{
		outcome:      pendingReUploaded,
		packTxID:     "pack-reuploaded",
		manifestTxID: "manifest-1",
		parentTxID:   "parent-1",
		refs:         map[string]string{"refs/heads/main": "bbb"},
	}

	refs, packs := effectiveState(rs, res)

	if refs["refs/heads/main"] != "bbb" {
		t.Errorf("expected pending refs, got %q", refs["refs/heads/main"])
	}
	if len(packs) != 2 {
		t.Fatalf("expected 2 packs, got %d", len(packs))
	}
	if packs[1].TX != "pack-reuploaded" {
		t.Errorf("expected re-uploaded pack, got %q", packs[1].TX)
	}
}

func TestCheckConflict_PendingMatchesRemote(t *testing.T) {
	rs := &RemoteState{
		manifestTxID: "manifest-1",
		m:            &manifest.Manifest{},
	}
	res := &pendingResolution{
		outcome:    pendingInMempool,
		parentTxID: "manifest-1",
	}
	state := newTestState(t)

	if err := checkConflict(rs, res, state); err != nil {
		t.Errorf("checkConflict with matching pending parent: %v", err)
	}
}

func TestCheckConflict_ReUploadedMatchesRemote(t *testing.T) {
	rs := &RemoteState{
		manifestTxID: "manifest-1",
		m:            &manifest.Manifest{},
	}
	res := &pendingResolution{
		outcome:    pendingReUploaded,
		parentTxID: "manifest-1",
	}
	state := newTestState(t)

	if err := checkConflict(rs, res, state); err != nil {
		t.Errorf("checkConflict with re-uploaded matching parent: %v", err)
	}
}

func TestPendingState_PacksRoundTrip(t *testing.T) {
	state := newTestState(t)

	packs := []manifest.PackEntry{
		{TX: "pack-1", Base: "", Tip: "aaa", Size: 100},
		{TX: "pack-2", Base: "aaa", Tip: "bbb", Size: 200},
	}
	pending := &localstate.PendingState{
		PackTxID:     "pack-2",
		ManifestTxID: "manifest-1",
		ParentTxID:   "parent-1",
		Refs:         map[string]string{"refs/heads/main": "bbb"},
		Packs:        packs,
		PackBase:     "aaa",
		PackTip:      "bbb",
		UploadedAt:   time.Now(),
		Guaranteed:   true,
	}
	if err := state.SavePending(pending, []byte("data")); err != nil {
		t.Fatalf("SavePending: %v", err)
	}

	loaded, _, err := state.LoadPending()
	if err != nil {
		t.Fatalf("LoadPending: %v", err)
	}
	if len(loaded.Packs) != 2 {
		t.Fatalf("expected 2 packs, got %d", len(loaded.Packs))
	}
	if loaded.Packs[0].TX != "pack-1" || loaded.Packs[1].TX != "pack-2" {
		t.Errorf("packs = %v, want pack-1 and pack-2", loaded.Packs)
	}
}

func TestEffectiveState_UnfetchableManifestWithPendingPacks(t *testing.T) {
	// rs.m is nil — manifest found in GraphQL but body unfetchable.
	// Pending packs contain the full history.
	rs := &RemoteState{manifestTxID: "manifest-unfetchable"}
	res := &pendingResolution{
		outcome:      pendingInMempool,
		packTxID:     "pack-2",
		manifestTxID: "manifest-1",
		parentTxID:   "parent-1",
		refs:         map[string]string{"refs/heads/main": "bbb"},
		packs: []manifest.PackEntry{
			{TX: "pack-1", Base: "", Tip: "aaa", Size: 100},
			{TX: "pack-2", Base: "aaa", Tip: "bbb", Size: 200},
		},
	}

	refs, packs := effectiveState(rs, res)

	// Should use pending refs.
	if refs["refs/heads/main"] != "bbb" {
		t.Errorf("expected pending refs, got %q", refs["refs/heads/main"])
	}
	// Should use full pack list from pending (not just packTxID).
	if len(packs) != 2 {
		t.Fatalf("expected 2 packs from pending, got %d", len(packs))
	}
	if packs[0].TX != "pack-1" {
		t.Errorf("packs[0] = %q, want pack-1", packs[0].TX)
	}
	if packs[1].TX != "pack-2" {
		t.Errorf("packs[1] = %q, want pack-2", packs[1].TX)
	}
}

func TestEffectiveState_UnfetchableManifestNoPendingPacks(t *testing.T) {
	// rs.m is nil, pending has no packs (old pending format).
	// Should fall back to just packTxID.
	rs := &RemoteState{manifestTxID: "manifest-unfetchable"}
	res := &pendingResolution{
		outcome:  pendingInMempool,
		packTxID: "pack-1",
		refs:     map[string]string{"refs/heads/main": "aaa"},
	}

	_, packs := effectiveState(rs, res)

	if len(packs) != 1 {
		t.Fatalf("expected 1 pack, got %d", len(packs))
	}
	if packs[0].TX != "pack-1" {
		t.Errorf("pack TX = %q, want pack-1", packs[0].TX)
	}
}

func TestManifestFetchError(t *testing.T) {
	inner := fmt.Errorf("connection refused")
	mfe := &ManifestFetchError{TxID: "tx-123", Err: inner}

	msg := mfe.Error()
	if msg != `ops: fetch manifest body "tx-123": connection refused` {
		t.Errorf("unexpected error message: %s", msg)
	}

	if mfe.Unwrap() != inner {
		t.Error("Unwrap should return inner error")
	}
}

func TestManifestFetchError_ErrorsAs(t *testing.T) {
	inner := fmt.Errorf("not found")
	mfe := &ManifestFetchError{TxID: "tx-456", Err: inner}
	wrapped := fmt.Errorf("some context: %w", mfe)

	var target *ManifestFetchError
	if !errors.As(wrapped, &target) {
		t.Fatal("errors.As should match ManifestFetchError")
	}
	if target.TxID != "tx-456" {
		t.Errorf("TxID = %q, want tx-456", target.TxID)
	}
}
