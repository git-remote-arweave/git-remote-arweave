package ops

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"git-remote-arweave/internal/crypto"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
)

func TestRemoteState_HasKeyMap(t *testing.T) {
	// No manifest.
	rs := &RemoteState{}
	if rs.HasKeyMap() {
		t.Error("HasKeyMap() = true for nil manifest")
	}

	// Manifest without keymap.
	rs = &RemoteState{m: &manifest.Manifest{}}
	if rs.HasKeyMap() {
		t.Error("HasKeyMap() = true for empty KeyMap")
	}

	// Manifest with keymap.
	rs = &RemoteState{m: &manifest.Manifest{KeyMap: "km-tx-123"}}
	if !rs.HasKeyMap() {
		t.Error("HasKeyMap() = false for manifest with KeyMap")
	}
}

func TestRemoteState_KeyMapTx(t *testing.T) {
	// Nil manifest.
	rs := &RemoteState{}
	if got := rs.KeyMapTx(); got != "" {
		t.Errorf("KeyMapTx() = %q for nil manifest, want empty", got)
	}

	// Manifest without keymap.
	rs = &RemoteState{m: &manifest.Manifest{}}
	if got := rs.KeyMapTx(); got != "" {
		t.Errorf("KeyMapTx() = %q for empty KeyMap, want empty", got)
	}

	// Manifest with keymap.
	rs = &RemoteState{m: &manifest.Manifest{KeyMap: "km-tx-456"}}
	if got := rs.KeyMapTx(); got != "km-tx-456" {
		t.Errorf("KeyMapTx() = %q, want km-tx-456", got)
	}
}

func TestSyncReadersFromKeyMap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".git")
	_ = os.MkdirAll(dir, 0o700)
	state, err := localstate.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Build a keymap with two readers in epoch 0 and one removed in epoch 1.
	km := crypto.NewKeyMap()
	pubkeyAlice := base64.RawURLEncoding.EncodeToString([]byte("alice-modulus"))
	pubkeyBob := base64.RawURLEncoding.EncodeToString([]byte("bob-modulus"))
	// Epoch 0: both readers (simulate wrapped keys with dummy values).
	km.Epochs["0"] = map[string]string{
		pubkeyAlice: "wrapped-key-alice",
		pubkeyBob:   "wrapped-key-bob",
	}
	// Epoch 1: only Alice (Bob was removed, key rotated).
	km.Epochs["1"] = map[string]string{
		pubkeyAlice: "wrapped-key-alice-v2",
	}

	syncReadersFromKeyMap(state, km)

	readers, err := state.LoadReaders()
	if err != nil {
		t.Fatal(err)
	}

	// Should only have Alice (from latest epoch 1), not Bob.
	if len(readers) != 1 {
		t.Fatalf("expected 1 reader, got %d: %v", len(readers), readers)
	}
	if readers[0].PubKey != pubkeyAlice {
		t.Errorf("reader pubkey = %q, want %q", readers[0].PubKey, pubkeyAlice)
	}
}

func TestSyncReadersFromKeyMap_EmptyKeyMap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".git")
	_ = os.MkdirAll(dir, 0o700)
	state, err := localstate.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	km := crypto.NewKeyMap() // no epochs
	syncReadersFromKeyMap(state, km)

	readers, _ := state.LoadReaders()
	if len(readers) != 0 {
		t.Fatalf("expected 0 readers for empty keymap, got %d", len(readers))
	}
}
