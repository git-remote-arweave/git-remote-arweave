package localstate

import (
	"testing"
)

func TestReadersAddRemove(t *testing.T) {
	s := testState(t)

	// Initially empty.
	readers, err := s.LoadReaders()
	if err != nil {
		t.Fatal(err)
	}
	if len(readers) != 0 {
		t.Fatalf("expected empty readers, got %v", readers)
	}

	// Add reader without pubkey.
	added, err := s.AddReader("addr1", "")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("expected reader to be added")
	}

	// Duplicate.
	added, err = s.AddReader("addr1", "")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("duplicate should not be added")
	}

	// Add second reader with pubkey.
	if _, err := s.AddReader("addr2", "some-pubkey-modulus"); err != nil {
		t.Fatal(err)
	}

	readers, _ = s.LoadReaders()
	if len(readers) != 2 {
		t.Fatalf("expected 2 readers, got %d", len(readers))
	}
	if readers[1].PubKey != "some-pubkey-modulus" {
		t.Fatalf("expected pubkey, got %q", readers[1].PubKey)
	}

	// Remove first.
	removed, err := s.RemoveReader("addr1")
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("expected reader to be removed")
	}

	// Remove non-existent.
	removed, err = s.RemoveReader("addr1")
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("should not remove non-existent reader")
	}

	readers, _ = s.LoadReaders()
	if len(readers) != 1 || readers[0].Address != "addr2" {
		t.Fatalf("expected [addr2], got %v", readers)
	}
}

func TestReadersAddPubkeyUpdate(t *testing.T) {
	s := testState(t)

	// Add without pubkey.
	if _, err := s.AddReader("addr1", ""); err != nil {
		t.Fatal(err)
	}

	// Re-add with pubkey should update.
	added, err := s.AddReader("addr1", "my-pubkey")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("expected update when adding pubkey to existing reader")
	}

	readers, _ := s.LoadReaders()
	if readers[0].PubKey != "my-pubkey" {
		t.Fatalf("expected pubkey update, got %q", readers[0].PubKey)
	}

	// Re-add with same pubkey should be no-op.
	added, err = s.AddReader("addr1", "my-pubkey")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("should not modify when pubkey already set")
	}
}

func TestAddresses(t *testing.T) {
	readers := []Reader{
		{Address: "a", PubKey: "pk1"},
		{Address: "b"},
		{Address: "c", PubKey: "pk3"},
	}
	addrs := Addresses(readers)
	if len(addrs) != 3 || addrs[0] != "a" || addrs[1] != "b" || addrs[2] != "c" {
		t.Fatalf("Addresses: got %v", addrs)
	}
}

func TestEncryptionStateRoundTrip(t *testing.T) {
	s := testState(t)

	es := &EncryptionState{
		CurrentEpoch: 2,
		KeyMapTxID:   "km-tx-123",
		EpochKeys: map[string]string{
			"0": "key0-base64",
			"1": "key1-base64",
			"2": "key2-base64",
		},
	}

	if err := s.SaveEncryption(es); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadEncryption()
	if err != nil {
		t.Fatal(err)
	}
	if got.CurrentEpoch != 2 {
		t.Fatalf("epoch: got %d, want 2", got.CurrentEpoch)
	}
	if got.KeyMapTxID != "km-tx-123" {
		t.Fatalf("keymap tx: got %q, want %q", got.KeyMapTxID, "km-tx-123")
	}
	if len(got.EpochKeys) != 3 {
		t.Fatalf("epoch keys: got %d, want 3", len(got.EpochKeys))
	}
}

func TestEncryptionStateNotFound(t *testing.T) {
	s := testState(t)
	es, err := s.LoadEncryption()
	if err != nil {
		t.Fatal(err)
	}
	if es != nil {
		t.Fatal("expected nil for missing encryption state")
	}
}

// testState creates a State in a temp directory.
func testState(t *testing.T) *State {
	t.Helper()
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
