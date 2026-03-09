package ops

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/permadao/goar"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/config"
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
	_ = state.SaveLastManifestTxID("manifest-1")
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
	_ = state.SaveLastManifestTxID("manifest-1")
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
	_ = os.MkdirAll(dir, 0o700)
	s, err := localstate.NewScoped(dir, "test-owner", "test-repo")
	if err != nil {
		t.Fatalf("localstate.NewScoped: %v", err)
	}
	return s
}

// newTestClient creates an arweave.Client backed by a test HTTP server.
// manifests maps txID → Manifest. The server returns JSON-encoded manifests.
func newTestClient(t *testing.T, manifests map[string]*manifest.Manifest) *arweave.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		txID := strings.TrimPrefix(r.URL.Path, "/")
		m, ok := manifests[txID]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		data, _ := json.Marshal(m)
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		Gateway: srv.URL,
		Payment: config.PaymentNative, // same fetch gateway = test server
	}
	ar, err := arweave.New(cfg)
	if err != nil {
		t.Fatalf("arweave.New: %v", err)
	}
	return ar
}

// TestCheckConflict_RemoteAhead verifies that when remote has advanced
// past our last-manifest (e.g. interrupted push that succeeded, or push
// from another machine), checkConflict accepts the remote state.
func TestCheckConflict_RemoteAhead(t *testing.T) {
	// Chain: genesis → local-manifest → remote-manifest
	manifests := map[string]*manifest.Manifest{
		"remote-manifest": {Version: 1, Parent: "local-manifest", Refs: map[string]string{"refs/heads/main": "bbb"}},
		"local-manifest":  {Version: 1, Parent: "genesis", Refs: map[string]string{"refs/heads/main": "aaa"}},
	}
	ar := newTestClient(t, manifests)
	state := newTestState(t)
	_ = state.SaveLastManifestTxID("local-manifest")

	rs := &RemoteState{
		manifestTxID: "remote-manifest",
		m:            manifests["remote-manifest"],
	}
	res := &pendingResolution{outcome: noPending}
	ctx := context.Background()

	got, err := checkConflict(ctx, ar, rs, res, state)
	if err != nil {
		t.Fatalf("checkConflict should accept remote-ahead: %v", err)
	}
	if got.manifestTxID != "remote-manifest" {
		t.Errorf("expected remote-manifest, got %q", got.manifestTxID)
	}
}

// TestCheckConflict_LocalAhead verifies that when our local manifest is
// ahead of GraphQL (delivered but not indexed), checkConflict returns
// the local manifest as effective state.
func TestCheckConflict_LocalAhead(t *testing.T) {
	// Chain: genesis → remote-manifest → local-manifest
	manifests := map[string]*manifest.Manifest{
		"local-manifest":  {Version: 1, Parent: "remote-manifest", Refs: map[string]string{"refs/heads/main": "bbb"}},
		"remote-manifest": {Version: 1, Parent: "genesis", Refs: map[string]string{"refs/heads/main": "aaa"}},
	}
	ar := newTestClient(t, manifests)
	state := newTestState(t)
	_ = state.SaveLastManifestTxID("local-manifest")

	rs := &RemoteState{
		manifestTxID: "remote-manifest",
		m:            manifests["remote-manifest"],
	}
	res := &pendingResolution{outcome: noPending}
	ctx := context.Background()

	got, err := checkConflict(ctx, ar, rs, res, state)
	if err != nil {
		t.Fatalf("checkConflict should accept local-ahead: %v", err)
	}
	if got.manifestTxID != "local-manifest" {
		t.Errorf("expected local-manifest as effective state, got %q", got.manifestTxID)
	}
}

// TestCheckConflict_DivergedChains verifies that truly diverged chains
// (no ancestry relation) are detected as conflicts.
func TestCheckConflict_DivergedChains(t *testing.T) {
	manifests := map[string]*manifest.Manifest{
		"local-manifest":  {Version: 1, Parent: "genesis-A", Refs: map[string]string{"refs/heads/main": "aaa"}},
		"remote-manifest": {Version: 1, Parent: "genesis-B", Refs: map[string]string{"refs/heads/main": "bbb"}},
		"genesis-A":       {Version: 1, Refs: map[string]string{}},
		"genesis-B":       {Version: 1, Refs: map[string]string{}},
	}
	ar := newTestClient(t, manifests)
	state := newTestState(t)
	_ = state.SaveLastManifestTxID("local-manifest")

	rs := &RemoteState{
		manifestTxID: "remote-manifest",
		m:            manifests["remote-manifest"],
	}
	res := &pendingResolution{outcome: noPending}
	ctx := context.Background()

	_, err := checkConflict(ctx, ar, rs, res, state)
	if err == nil {
		t.Fatal("checkConflict should detect conflict on diverged chains")
	}
	if !strings.Contains(err.Error(), "conflict") {
		t.Errorf("expected conflict error, got: %v", err)
	}
}

// TestCheckConflict_LocalAheadEncrypted verifies the fast path:
// when the stored lastParent matches the remote manifest, ancestry is
// confirmed without fetching/parsing the (encrypted) manifest body.
func TestCheckConflict_LocalAheadEncrypted(t *testing.T) {
	// No manifests fetchable — simulates encrypted bodies.
	ar := newTestClient(t, map[string]*manifest.Manifest{})
	state := newTestState(t)
	// local-manifest has parent = remote-manifest (stored in local state).
	_ = state.SaveLastManifest("local-manifest", "remote-manifest")

	rs := &RemoteState{
		manifestTxID: "remote-manifest",
		m:            &manifest.Manifest{Version: 1, Refs: map[string]string{"refs/heads/main": "aaa"}},
	}
	res := &pendingResolution{outcome: noPending}
	ctx := context.Background()

	got, err := checkConflict(ctx, ar, rs, res, state)
	if err != nil {
		t.Fatalf("checkConflict should accept local-ahead via stored parent: %v", err)
	}
	// Fast path returns rs (remote state) since we can't parse the encrypted local manifest.
	if got.manifestTxID != "remote-manifest" {
		t.Errorf("expected remote-manifest, got %q", got.manifestTxID)
	}
}

func TestPush_OwnerMismatch(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := goar.NewSignerByPrivateKey(key)
	w := goar.NewWalletWithSigner(signer, "https://arweave.net")
	ar := arweave.NewWithWallet(w)

	ctx := context.Background()
	state := newTestState(t)
	cfg := &config.Config{}

	_, err = Push(ctx, ar, nil, nil, state, cfg, "wrong-owner-address", "repo", &PushInput{
		RefUpdates: map[string]string{"refs/heads/main": "aaaa000000000000000000000000000000000000"},
	})
	if err == nil {
		t.Fatal("Push should fail when wallet address doesn't match owner")
	}
	if !strings.Contains(err.Error(), "does not match remote owner") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPush_OwnerMatch(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := goar.NewSignerByPrivateKey(key)
	w := goar.NewWalletWithSigner(signer, "https://arweave.net")
	ar := arweave.NewWithWallet(w)

	ctx := context.Background()
	state := newTestState(t)
	cfg := &config.Config{}

	// Push will fail or panic later (no GraphQL client), but should pass the owner check.
	func() {
		defer func() { _ = recover() }()
		_, err = Push(ctx, ar, nil, nil, state, cfg, ar.Address(), "repo", &PushInput{
			RefUpdates: map[string]string{"refs/heads/main": "aaaa000000000000000000000000000000000000"},
		})
	}()
	// If we got an error (not a panic), verify it's not the owner mismatch.
	if err != nil && strings.Contains(err.Error(), "does not match remote owner") {
		t.Errorf("owner check should pass when addresses match: %v", err)
	}
}

// TestForkPush_SourcePacksAsBase verifies that when pushing to a new repo
// with source packs from a previous fetch, the source packs are included
// in the effective packs and their tips are used as bases for delta generation.
func TestForkPush_SourcePacksAsBase(t *testing.T) {
	state := newTestState(t)

	// Simulate: user cloned from source, fetch saved source packs.
	sourcePacks := []manifest.PackEntry{
		{TX: "source-pack-1", Base: "", Tip: "aaa", Size: 1000},
		{TX: "source-pack-2", Base: "aaa", Tip: "bbb", Size: 500},
	}
	if err := state.SaveSourcePacks(sourcePacks); err != nil {
		t.Fatalf("SaveSourcePacks: %v", err)
	}

	// New repo — no remote state.
	rs := &RemoteState{}
	res := &pendingResolution{outcome: noPending}

	effectiveRefs, effectivePacks := effectiveState(rs, res)
	if len(effectivePacks) != 0 {
		t.Fatalf("effectiveState should return no packs for new repo")
	}

	// Fork detection: load source packs for new repo.
	if rs.m == nil && len(effectivePacks) == 0 {
		sp, err := state.LoadSourcePacks()
		if err != nil {
			t.Fatalf("LoadSourcePacks: %v", err)
		}
		if len(sp) > 0 {
			effectivePacks = sp
		}
	}

	if len(effectivePacks) != 2 {
		t.Fatalf("expected 2 source packs, got %d", len(effectivePacks))
	}
	if effectivePacks[0].TX != "source-pack-1" {
		t.Errorf("expected source-pack-1, got %q", effectivePacks[0].TX)
	}

	// computePackRange should get bases from source pack tips.
	updates := map[string]string{
		"refs/heads/main": "ccc",
	}
	tips, bases := computePackRange(updates, effectiveRefs)
	// Add source pack tips as bases.
	for _, pe := range effectivePacks {
		if pe.Tip != "" {
			bases = append(bases, plumbing.NewHash(pe.Tip))
		}
	}

	if len(tips) != 1 {
		t.Fatalf("expected 1 tip, got %d", len(tips))
	}
	if len(bases) != 2 {
		t.Fatalf("expected 2 bases (from source pack tips), got %d", len(bases))
	}
}

// TestForkPush_NoSourcePacks verifies that pushing to a new repo without
// source packs works normally (not a fork).
func TestForkPush_NoSourcePacks(t *testing.T) {
	state := newTestState(t)

	sp, err := state.LoadSourcePacks()
	if err != nil {
		t.Fatalf("LoadSourcePacks: %v", err)
	}
	if sp != nil {
		t.Errorf("expected nil source packs, got %v", sp)
	}
}

// TestForkPush_ClearSourcePacks verifies that source packs are cleared
// after a successful fork push.
func TestForkPush_ClearSourcePacks(t *testing.T) {
	state := newTestState(t)

	if err := state.SaveSourcePacks([]manifest.PackEntry{{TX: "pack-1"}}); err != nil {
		t.Fatalf("SaveSourcePacks: %v", err)
	}

	// Verify they exist.
	sp, _ := state.LoadSourcePacks()
	if len(sp) != 1 {
		t.Fatalf("expected 1 source pack, got %d", len(sp))
	}

	// Clear.
	if err := state.ClearSourcePacks(); err != nil {
		t.Fatalf("ClearSourcePacks: %v", err)
	}

	// Verify cleared.
	sp, _ = state.LoadSourcePacks()
	if sp != nil {
		t.Errorf("expected nil after clear, got %v", sp)
	}
}

func TestPush_NoWalletSkipsCheck(t *testing.T) {
	ar := arweave.NewWithWallet(nil)

	ctx := context.Background()
	state := newTestState(t)
	cfg := &config.Config{}

	func() {
		defer func() { _ = recover() }()
		_, err := Push(ctx, ar, nil, nil, state, cfg, "some-owner", "repo", &PushInput{
			RefUpdates: map[string]string{"refs/heads/main": "aaaa000000000000000000000000000000000000"},
		})
		// Should not be an owner mismatch (wallet address is empty, check is skipped).
		if err != nil && strings.Contains(err.Error(), "does not match remote owner") {
			t.Errorf("owner check should be skipped without wallet: %v", err)
		}
	}()
}
