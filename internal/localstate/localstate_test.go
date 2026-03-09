package localstate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"git-remote-arweave/internal/manifest"
)

func newTestState(t *testing.T) *State {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func newScopedTestState(t *testing.T) *State {
	t.Helper()
	s, err := NewScoped(t.TempDir(), "test-owner", "test-repo")
	if err != nil {
		t.Fatalf("NewScoped: %v", err)
	}
	return s
}

func TestAppliedPacks(t *testing.T) {
	s := newTestState(t)

	ok, err := s.IsApplied("tx1")
	if err != nil || ok {
		t.Errorf("IsApplied on empty set: got (%v, %v), want (false, nil)", ok, err)
	}

	if err := s.MarkApplied("tx1", "tx2"); err != nil {
		t.Fatalf("MarkApplied: %v", err)
	}

	ok, err = s.IsApplied("tx1")
	if err != nil || !ok {
		t.Errorf("IsApplied after mark: got (%v, %v), want (true, nil)", ok, err)
	}

	ok, err = s.IsApplied("tx3")
	if err != nil || ok {
		t.Errorf("IsApplied for unknown tx: got (%v, %v), want (false, nil)", ok, err)
	}
}

func TestAppliedPacksPersisted(t *testing.T) {
	dir := t.TempDir()

	s1, _ := New(dir)
	_ = s1.MarkApplied("tx-a", "tx-b")

	// reload from same dir
	s2, _ := New(dir)
	ok, err := s2.IsApplied("tx-a")
	if err != nil || !ok {
		t.Errorf("applied-packs not persisted across State instances: got (%v, %v)", ok, err)
	}
}

func TestAppliedSetIncludes(t *testing.T) {
	s := newTestState(t)
	_ = s.MarkApplied("tx1", "tx2", "tx3")

	set, err := s.AppliedSet()
	if err != nil {
		t.Fatalf("AppliedSet: %v", err)
	}
	for _, id := range []string{"tx1", "tx2", "tx3"} {
		if !set[id] {
			t.Errorf("AppliedSet missing %q", id)
		}
	}
}

func TestPendingRoundtrip(t *testing.T) {
	s := newScopedTestState(t)

	if s.HasPending() {
		t.Error("HasPending should be false initially")
	}

	state := &PendingState{
		PackTxID:     "pack-tx-1",
		ManifestTxID: "manifest-tx-1",
		ParentTxID:   "parent-tx-0",
		Refs:         map[string]string{"refs/heads/main": "abc123"},
		PackBase:     "base-sha",
		PackTip:      "tip-sha",
		UploadedAt:   time.Unix(1000, 0),
	}
	packData := []byte("fake packfile data")

	if err := s.SavePending(state, packData); err != nil {
		t.Fatalf("SavePending: %v", err)
	}

	if !s.HasPending() {
		t.Error("HasPending should be true after SavePending")
	}

	loaded, loadedPack, err := s.LoadPending()
	if err != nil {
		t.Fatalf("LoadPending: %v", err)
	}
	if loaded.PackTxID != state.PackTxID {
		t.Errorf("PackTxID = %q, want %q", loaded.PackTxID, state.PackTxID)
	}
	if loaded.ManifestTxID != state.ManifestTxID {
		t.Errorf("ManifestTxID = %q, want %q", loaded.ManifestTxID, state.ManifestTxID)
	}
	if loaded.Refs["refs/heads/main"] != "abc123" {
		t.Errorf("Refs mismatch: got %v", loaded.Refs)
	}
	if loaded.PackBase != "base-sha" || loaded.PackTip != "tip-sha" {
		t.Errorf("PackBase/Tip mismatch: got %q/%q", loaded.PackBase, loaded.PackTip)
	}
	if string(loadedPack) != string(packData) {
		t.Errorf("pack data mismatch")
	}
}

func TestPendingNone(t *testing.T) {
	s := newScopedTestState(t)
	state, pack, err := s.LoadPending()
	if err != nil || state != nil || pack != nil {
		t.Errorf("LoadPending on empty state: got (%v, %v, %v), want (nil, nil, nil)", state, pack, err)
	}
}

func TestClearPending(t *testing.T) {
	s := newScopedTestState(t)
	_ = s.SavePending(&PendingState{PackTxID: "tx1", ManifestTxID: "tx2", UploadedAt: time.Now()}, []byte("pack"))

	if err := s.ClearPending(); err != nil {
		t.Fatalf("ClearPending: %v", err)
	}
	if s.HasPending() {
		t.Error("HasPending should be false after ClearPending")
	}

	// idempotent
	if err := s.ClearPending(); err != nil {
		t.Errorf("ClearPending again: %v", err)
	}
}

func TestPendingRefOnly(t *testing.T) {
	s := newScopedTestState(t)

	state := &PendingState{
		ManifestTxID: "manifest-tx-1",
		ParentTxID:   "parent-tx-0",
		Refs:         map[string]string{"refs/heads/main": "abc123"},
		UploadedAt:   time.Now(),
	}
	if err := s.SavePending(state, nil); err != nil {
		t.Fatalf("SavePending with nil pack: %v", err)
	}

	loaded, loadedPack, err := s.LoadPending()
	if err != nil {
		t.Fatalf("LoadPending: %v", err)
	}
	if loaded.ManifestTxID != "manifest-tx-1" {
		t.Errorf("ManifestTxID = %q, want manifest-tx-1", loaded.ManifestTxID)
	}
	if loadedPack != nil {
		t.Errorf("expected nil pack data, got %d bytes", len(loadedPack))
	}
}

func TestLastManifestTxID(t *testing.T) {
	s := newScopedTestState(t)

	// empty initially
	id, err := s.LoadLastManifestTxID()
	if err != nil || id != "" {
		t.Errorf("LoadLastManifestTxID on empty: got (%q, %v), want (\"\", nil)", id, err)
	}

	if err := s.SaveLastManifestTxID("manifest-tx-1"); err != nil {
		t.Fatalf("SaveLastManifestTxID: %v", err)
	}

	id, err = s.LoadLastManifestTxID()
	if err != nil || id != "manifest-tx-1" {
		t.Errorf("LoadLastManifestTxID: got (%q, %v), want (manifest-tx-1, nil)", id, err)
	}

	// overwrite
	if err := s.SaveLastManifestTxID("manifest-tx-2"); err != nil {
		t.Fatalf("SaveLastManifestTxID overwrite: %v", err)
	}
	id, _ = s.LoadLastManifestTxID()
	if id != "manifest-tx-2" {
		t.Errorf("after overwrite: got %q, want manifest-tx-2", id)
	}
}

func TestLastManifestWithParent(t *testing.T) {
	s := newScopedTestState(t)

	if err := s.SaveLastManifest("manifest-1", "parent-1"); err != nil {
		t.Fatalf("SaveLastManifest: %v", err)
	}

	txID, parentTxID, err := s.LoadLastManifest()
	if err != nil {
		t.Fatalf("LoadLastManifest: %v", err)
	}
	if txID != "manifest-1" {
		t.Errorf("txID = %q, want manifest-1", txID)
	}
	if parentTxID != "parent-1" {
		t.Errorf("parentTxID = %q, want parent-1", parentTxID)
	}

	// LoadLastManifestTxID should still work (backward compat).
	id, err := s.LoadLastManifestTxID()
	if err != nil || id != "manifest-1" {
		t.Errorf("LoadLastManifestTxID: got (%q, %v)", id, err)
	}
}

func TestGenesisManifest(t *testing.T) {
	s := newScopedTestState(t)

	txID, err := s.LoadGenesisManifest()
	if err != nil || txID != "" {
		t.Errorf("LoadGenesisManifest on empty: got (%q, %v), want (\"\", nil)", txID, err)
	}

	if err := s.SaveGenesisManifest("genesis-tx-1"); err != nil {
		t.Fatalf("SaveGenesisManifest: %v", err)
	}

	txID, err = s.LoadGenesisManifest()
	if err != nil || txID != "genesis-tx-1" {
		t.Errorf("LoadGenesisManifest: got (%q, %v), want (genesis-tx-1, nil)", txID, err)
	}

	// overwrite (e.g. force push creates new genesis)
	if err := s.SaveGenesisManifest("genesis-tx-2"); err != nil {
		t.Fatalf("SaveGenesisManifest overwrite: %v", err)
	}
	txID, _ = s.LoadGenesisManifest()
	if txID != "genesis-tx-2" {
		t.Errorf("after overwrite: got %q, want genesis-tx-2", txID)
	}
}

func TestSourceManifest(t *testing.T) {
	s := newScopedTestState(t)

	// empty initially
	txID, err := s.LoadSourceManifest()
	if err != nil || txID != "" {
		t.Errorf("LoadSourceManifest on empty: got (%q, %v), want (\"\", nil)", txID, err)
	}

	if err := s.SaveSourceManifest("source-tx-1"); err != nil {
		t.Fatalf("SaveSourceManifest: %v", err)
	}

	txID, err = s.LoadSourceManifest()
	if err != nil || txID != "source-tx-1" {
		t.Errorf("LoadSourceManifest: got (%q, %v), want (source-tx-1, nil)", txID, err)
	}

	if err := s.ClearSourceManifest(); err != nil {
		t.Fatalf("ClearSourceManifest: %v", err)
	}

	txID, err = s.LoadSourceManifest()
	if err != nil || txID != "" {
		t.Errorf("after clear: got (%q, %v), want (\"\", nil)", txID, err)
	}
}

func TestSourceKeymap(t *testing.T) {
	s := newScopedTestState(t)

	// Empty initially.
	txID, err := s.LoadSourceKeymap()
	if err != nil || txID != "" {
		t.Errorf("LoadSourceKeymap on empty: got (%q, %v), want (\"\", nil)", txID, err)
	}

	if err := s.SaveSourceKeymap("km-tx-abc"); err != nil {
		t.Fatalf("SaveSourceKeymap: %v", err)
	}

	txID, err = s.LoadSourceKeymap()
	if err != nil || txID != "km-tx-abc" {
		t.Errorf("LoadSourceKeymap: got (%q, %v), want (km-tx-abc, nil)", txID, err)
	}

	if err := s.ClearSourceKeymap(); err != nil {
		t.Fatalf("ClearSourceKeymap: %v", err)
	}

	txID, err = s.LoadSourceKeymap()
	if err != nil || txID != "" {
		t.Errorf("after clear: got (%q, %v), want (\"\", nil)", txID, err)
	}

	// ClearSourceKeymap on already-cleared should not error.
	if err := s.ClearSourceKeymap(); err != nil {
		t.Errorf("ClearSourceKeymap idempotent: %v", err)
	}
}

func TestLastManifestLegacyFormat(t *testing.T) {
	s := newScopedTestState(t)

	// Legacy format: only tx-id, no parent.
	if err := s.SaveLastManifestTxID("manifest-old"); err != nil {
		t.Fatalf("SaveLastManifestTxID: %v", err)
	}

	txID, parentTxID, err := s.LoadLastManifest()
	if err != nil {
		t.Fatalf("LoadLastManifest: %v", err)
	}
	if txID != "manifest-old" {
		t.Errorf("txID = %q, want manifest-old", txID)
	}
	if parentTxID != "" {
		t.Errorf("parentTxID = %q, want empty for legacy", parentTxID)
	}
}

func TestScopedIsolation(t *testing.T) {
	dir := t.TempDir()

	s1, err := NewScoped(dir, "alice", "repo-a")
	if err != nil {
		t.Fatalf("NewScoped alice: %v", err)
	}
	s2, err := NewScoped(dir, "bob", "repo-b")
	if err != nil {
		t.Fatalf("NewScoped bob: %v", err)
	}

	// Save pending in s1, verify s2 doesn't see it.
	_ = s1.SavePending(&PendingState{ManifestTxID: "alice-manifest", UploadedAt: time.Now()}, nil)

	if !s1.HasPending() {
		t.Error("s1 should have pending")
	}
	if s2.HasPending() {
		t.Error("s2 should NOT have pending from s1")
	}

	// Save genesis in s2, verify s1 doesn't see it.
	_ = s2.SaveGenesisManifest("bob-genesis")

	g1, _ := s1.LoadGenesisManifest()
	g2, _ := s2.LoadGenesisManifest()
	if g1 != "" {
		t.Errorf("s1 genesis should be empty, got %q", g1)
	}
	if g2 != "bob-genesis" {
		t.Errorf("s2 genesis = %q, want bob-genesis", g2)
	}
}

func TestMigrateLegacyFiles(t *testing.T) {
	dir := t.TempDir()

	// Create legacy flat files as if from old version.
	arDir := filepath.Join(dir, dirName)
	_ = os.MkdirAll(arDir, 0o700)
	_ = os.WriteFile(filepath.Join(arDir, pendingJSONFile), []byte(`{"manifest_tx":"old-pending"}`), 0o600)
	_ = os.WriteFile(filepath.Join(arDir, lastManifestFile), []byte("old-manifest"), 0o600)
	_ = os.WriteFile(filepath.Join(arDir, genesisManifestFile), []byte("old-genesis"), 0o600)

	// NewScoped should migrate them.
	s, err := NewScoped(dir, "owner", "repo")
	if err != nil {
		t.Fatalf("NewScoped: %v", err)
	}

	// Verify old files are gone.
	for _, name := range []string{pendingJSONFile, lastManifestFile, genesisManifestFile} {
		if _, err := os.Stat(filepath.Join(arDir, name)); !os.IsNotExist(err) {
			t.Errorf("legacy file %q should be removed after migration", name)
		}
	}

	// Verify new files are accessible.
	pending, _, _ := s.LoadPending()
	if pending == nil || pending.ManifestTxID != "old-pending" {
		t.Errorf("migrated pending = %v, want manifest_tx=old-pending", pending)
	}

	txID, _ := s.LoadLastManifestTxID()
	if txID != "old-manifest" {
		t.Errorf("migrated last-manifest = %q, want old-manifest", txID)
	}

	genesis, _ := s.LoadGenesisManifest()
	if genesis != "old-genesis" {
		t.Errorf("migrated genesis = %q, want old-genesis", genesis)
	}
}

func TestSourcePacksCrossRemote(t *testing.T) {
	dir := t.TempDir()

	// Fetch from source remote saves source-packs.
	s1, err := NewScoped(dir, "alice", "original")
	if err != nil {
		t.Fatalf("NewScoped alice: %v", err)
	}
	packs := []manifest.PackEntry{{TX: "pack-1"}, {TX: "pack-2"}}
	if err := s1.SaveSourcePacks(packs); err != nil {
		t.Fatalf("SaveSourcePacks: %v", err)
	}
	_ = s1.SaveSourceManifest("source-manifest-tx")

	// Push to fork remote should see the same source-packs.
	s2, err := NewScoped(dir, "bob", "fork")
	if err != nil {
		t.Fatalf("NewScoped bob: %v", err)
	}
	loaded, err := s2.LoadSourcePacks()
	if err != nil {
		t.Fatalf("LoadSourcePacks from fork: %v", err)
	}
	if len(loaded) != 2 || loaded[0].TX != "pack-1" {
		t.Errorf("source packs from fork = %v, want [{pack-1} {pack-2}]", loaded)
	}
	sm, err := s2.LoadSourceManifest()
	if err != nil || sm != "source-manifest-tx" {
		t.Errorf("source manifest from fork = (%q, %v), want (source-manifest-tx, nil)", sm, err)
	}
}

func TestUnscopedPanicsOnPerRemoteMethod(t *testing.T) {
	s := newTestState(t)

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on unscoped State calling per-remote method")
		}
	}()
	s.HasPending() // should panic
}
