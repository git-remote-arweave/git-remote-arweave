package ops

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"git-remote-arweave/internal/localstate"
)

func TestDiffReaders_NoChange(t *testing.T) {
	added, removed := diffReaders([]string{"a", "b"}, []string{"a", "b"})
	if added || removed {
		t.Errorf("no change: added=%v, removed=%v", added, removed)
	}
}

func TestDiffReaders_Added(t *testing.T) {
	added, removed := diffReaders([]string{"a"}, []string{"a", "b"})
	if !added {
		t.Error("expected added=true")
	}
	if removed {
		t.Error("expected removed=false")
	}
}

func TestDiffReaders_Removed(t *testing.T) {
	added, removed := diffReaders([]string{"a", "b"}, []string{"a"})
	if added {
		t.Error("expected added=false")
	}
	if !removed {
		t.Error("expected removed=true")
	}
}

func TestDiffReaders_AddedAndRemoved(t *testing.T) {
	added, removed := diffReaders([]string{"a", "b"}, []string{"a", "c"})
	if !added || !removed {
		t.Errorf("expected both: added=%v, removed=%v", added, removed)
	}
}

func TestDiffReaders_Empty(t *testing.T) {
	added, removed := diffReaders(nil, nil)
	if added || removed {
		t.Errorf("both nil: added=%v, removed=%v", added, removed)
	}

	added, _ = diffReaders(nil, []string{"a"})
	if !added {
		t.Error("nil→[a]: expected added=true")
	}

	_, removed = diffReaders([]string{"a"}, nil)
	if !removed {
		t.Error("[a]→nil: expected removed=true")
	}
}

// TestInitEncryption_FirstPush verifies epoch 0 is created on first push.
func TestInitEncryption_FirstPush(t *testing.T) {
	state := newEncTestState(t)
	ec, err := initEncryption(state)
	if err != nil {
		t.Fatal(err)
	}
	if ec.epoch != 0 {
		t.Errorf("epoch = %d, want 0", ec.epoch)
	}
	if !ec.changed {
		t.Error("expected changed=true for first push")
	}

	// Verify encryption state was persisted.
	es, _ := state.LoadEncryption()
	if es == nil {
		t.Fatal("encryption state not saved")
	}
	if es.CurrentEpoch != 0 {
		t.Errorf("persisted epoch = %d, want 0", es.CurrentEpoch)
	}
}

// TestInitEncryption_ForkNoRotation verifies that a fork with unchanged readers
// does not rotate the key (same epoch, changed=true because no keymap yet).
func TestInitEncryption_ForkNoRotation(t *testing.T) {
	state := newEncTestState(t)

	// Simulate importForkEpochKeys: pre-populate encryption state with
	// LastReaders and no KeyMapTxID.
	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	es := &localstate.EncryptionState{
		CurrentEpoch: 1,
		EpochKeys:    map[string]string{"0": key, "1": key},
		LastReaders:  []string{"reader-a", "reader-b"},
	}
	if err := state.SaveEncryption(es); err != nil {
		t.Fatal(err)
	}

	// Set current readers to match LastReaders — no rotation needed.
	if _, err := state.AddReader("reader-a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := state.AddReader("reader-b", ""); err != nil {
		t.Fatal(err)
	}

	ec, err := initEncryption(state)
	if err != nil {
		t.Fatal(err)
	}
	if ec.epoch != 1 {
		t.Errorf("epoch = %d, want 1 (no rotation)", ec.epoch)
	}
	if !ec.changed {
		t.Error("expected changed=true (no keymap uploaded yet)")
	}
}

// TestInitEncryption_ForkWithRotation verifies that a fork with a removed reader
// triggers key rotation to a new epoch.
func TestInitEncryption_ForkWithRotation(t *testing.T) {
	state := newEncTestState(t)

	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	es := &localstate.EncryptionState{
		CurrentEpoch: 0,
		EpochKeys:    map[string]string{"0": key},
		LastReaders:  []string{"reader-a", "reader-b"},
	}
	if err := state.SaveEncryption(es); err != nil {
		t.Fatal(err)
	}

	// Only reader-a in current readers — reader-b was removed.
	if _, err := state.AddReader("reader-a", ""); err != nil {
		t.Fatal(err)
	}

	ec, err := initEncryption(state)
	if err != nil {
		t.Fatal(err)
	}
	if ec.epoch != 1 {
		t.Errorf("epoch = %d, want 1 (rotation after reader removal)", ec.epoch)
	}
	if !ec.changed {
		t.Error("expected changed=true after rotation")
	}

	// Verify new epoch key was created and persisted.
	es2, _ := state.LoadEncryption()
	if _, ok := es2.EpochKeys["1"]; !ok {
		t.Error("epoch 1 key not found in persisted state")
	}
}

// TestInitEncryption_ExistingKeymapReaderAdded verifies that adding a reader
// to an existing repo (keymap already uploaded) triggers keymap rebuild
// but no key rotation.
func TestInitEncryption_ExistingKeymapReaderAdded(t *testing.T) {
	state := newEncTestState(t)

	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	es := &localstate.EncryptionState{
		CurrentEpoch: 0,
		KeyMapTxID:   "existing-km-tx",
		EpochKeys:    map[string]string{"0": key},
		LastReaders:  []string{"reader-a"},
	}
	if err := state.SaveEncryption(es); err != nil {
		t.Fatal(err)
	}

	// Current readers: reader-a + reader-b (added).
	if _, err := state.AddReader("reader-a", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := state.AddReader("reader-b", ""); err != nil {
		t.Fatal(err)
	}

	ec, err := initEncryption(state)
	if err != nil {
		t.Fatal(err)
	}
	if ec.epoch != 0 {
		t.Errorf("epoch = %d, want 0 (no rotation on add)", ec.epoch)
	}
	if !ec.changed {
		t.Error("expected changed=true (reader added)")
	}
	// keymapTx carries the old value, but changed=true means
	// buildAndUploadKeyMap will replace it during push.
}

// TestInitEncryption_ExistingKeymapNoChange verifies reuse of keymap
// when readers haven't changed.
func TestInitEncryption_ExistingKeymapNoChange(t *testing.T) {
	state := newEncTestState(t)

	key := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	es := &localstate.EncryptionState{
		CurrentEpoch: 0,
		KeyMapTxID:   "existing-km-tx",
		EpochKeys:    map[string]string{"0": key},
		LastReaders:  []string{"reader-a"},
	}
	if err := state.SaveEncryption(es); err != nil {
		t.Fatal(err)
	}

	if _, err := state.AddReader("reader-a", ""); err != nil {
		t.Fatal(err)
	}

	ec, err := initEncryption(state)
	if err != nil {
		t.Fatal(err)
	}
	if ec.epoch != 0 {
		t.Errorf("epoch = %d, want 0", ec.epoch)
	}
	if ec.changed {
		t.Error("expected changed=false (no reader changes)")
	}
	if ec.keymapTx != "existing-km-tx" {
		t.Errorf("keymapTx = %q, want existing-km-tx", ec.keymapTx)
	}
}

func newEncTestState(t *testing.T) *localstate.State {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".git")
	_ = os.MkdirAll(dir, 0o700)
	s, err := localstate.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
