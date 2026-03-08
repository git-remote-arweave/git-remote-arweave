package localstate

import (
	"testing"
	"time"
)

func newTestState(t *testing.T) *State {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
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
	s := newTestState(t)

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
	s := newTestState(t)
	state, pack, err := s.LoadPending()
	if err != nil || state != nil || pack != nil {
		t.Errorf("LoadPending on empty state: got (%v, %v, %v), want (nil, nil, nil)", state, pack, err)
	}
}

func TestClearPending(t *testing.T) {
	s := newTestState(t)
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
	s := newTestState(t)

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
	s := newTestState(t)

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
