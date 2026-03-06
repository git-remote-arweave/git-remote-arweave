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
	s1.MarkApplied("tx-a", "tx-b")

	// reload from same dir
	s2, _ := New(dir)
	ok, err := s2.IsApplied("tx-a")
	if err != nil || !ok {
		t.Errorf("applied-packs not persisted across State instances: got (%v, %v)", ok, err)
	}
}

func TestAppliedSetIncludes(t *testing.T) {
	s := newTestState(t)
	s.MarkApplied("tx1", "tx2", "tx3")

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
		RepoID:       "repo-uuid",
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
	s.SavePending(&PendingState{PackTxID: "tx1", ManifestTxID: "tx2", UploadedAt: time.Now()}, []byte("pack"))

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
