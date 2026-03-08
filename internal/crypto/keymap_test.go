package crypto

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
)

func generateTestKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	k1, err := rsa.GenerateKey(rand.Reader, 2048) // 2048 for test speed
	if err != nil {
		t.Fatal(err)
	}
	k2, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k1, k2
}

func TestKeyMapRoundTrip(t *testing.T) {
	ownerKey, readerKey := generateTestKeys(t)
	symKey, _ := GenerateKey()

	km := NewKeyMap()
	readers := map[string]*rsa.PublicKey{
		"owner-addr":  &ownerKey.PublicKey,
		"reader-addr": &readerKey.PublicKey,
	}
	if err := km.SetEpochKey(0, &symKey, readers); err != nil {
		t.Fatalf("SetEpochKey: %v", err)
	}

	// Marshal/parse round-trip
	data, err := km.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	km2, err := ParseKeyMap(data)
	if err != nil {
		t.Fatalf("ParseKeyMap: %v", err)
	}

	// Owner can unwrap
	got, err := km2.GetKey(0, "owner-addr", ownerKey)
	if err != nil {
		t.Fatalf("GetKey owner: %v", err)
	}
	if got != symKey {
		t.Fatal("owner unwrapped wrong key")
	}

	// Reader can unwrap
	got, err = km2.GetKey(0, "reader-addr", readerKey)
	if err != nil {
		t.Fatalf("GetKey reader: %v", err)
	}
	if got != symKey {
		t.Fatal("reader unwrapped wrong key")
	}
}

func TestKeyMapEpochRotation(t *testing.T) {
	ownerKey, readerKey := generateTestKeys(t)

	key0, _ := GenerateKey()
	key1, _ := GenerateKey()

	km := NewKeyMap()

	// Epoch 0: owner + reader
	allReaders := map[string]*rsa.PublicKey{
		"owner":  &ownerKey.PublicKey,
		"reader": &readerKey.PublicKey,
	}
	if err := km.SetEpochKey(0, &key0, allReaders); err != nil {
		t.Fatal(err)
	}

	// Epoch 1: reader removed — only owner
	ownerOnly := map[string]*rsa.PublicKey{
		"owner": &ownerKey.PublicKey,
	}
	if err := km.SetEpochKey(1, &key1, ownerOnly); err != nil {
		t.Fatal(err)
	}

	// Re-wrap epoch 0 for remaining readers only
	if err := km.SetEpochKey(0, &key0, ownerOnly); err != nil {
		t.Fatal(err)
	}

	if km.LatestEpoch() != 1 {
		t.Fatalf("LatestEpoch: got %d, want 1", km.LatestEpoch())
	}
	if km.EpochCount() != 2 {
		t.Fatalf("EpochCount: got %d, want 2", km.EpochCount())
	}

	// Owner can get both keys
	got0, err := km.GetKey(0, "owner", ownerKey)
	if err != nil {
		t.Fatal(err)
	}
	if got0 != key0 {
		t.Fatal("owner epoch 0 key mismatch")
	}

	got1, err := km.GetKey(1, "owner", ownerKey)
	if err != nil {
		t.Fatal(err)
	}
	if got1 != key1 {
		t.Fatal("owner epoch 1 key mismatch")
	}

	// Removed reader cannot get epoch 0 from updated keymap
	_, err = km.GetKey(0, "reader", readerKey)
	if err == nil {
		t.Fatal("removed reader should not have access to epoch 0 in updated keymap")
	}

	// Removed reader cannot get epoch 1
	_, err = km.GetKey(1, "reader", readerKey)
	if err == nil {
		t.Fatal("removed reader should not have access to epoch 1")
	}
}

func TestKeyMapMissingEpoch(t *testing.T) {
	km := NewKeyMap()
	_, err := km.GetKey(99, "addr", nil)
	if err == nil {
		t.Fatal("should fail for missing epoch")
	}
}

func TestKeyMapMissingAddress(t *testing.T) {
	ownerKey, _ := generateTestKeys(t)
	key, _ := GenerateKey()

	km := NewKeyMap()
	readers := map[string]*rsa.PublicKey{"owner": &ownerKey.PublicKey}
	if err := km.SetEpochKey(0, &key, readers); err != nil {
		t.Fatal(err)
	}

	_, err := km.GetKey(0, "unknown", ownerKey)
	if err == nil {
		t.Fatal("should fail for unknown address")
	}
}

func TestKeyMapEmptyLatestEpoch(t *testing.T) {
	km := NewKeyMap()
	if km.LatestEpoch() != -1 {
		t.Fatalf("empty keymap LatestEpoch: got %d, want -1", km.LatestEpoch())
	}
}

func TestKeyMapReaders(t *testing.T) {
	ownerKey, readerKey := generateTestKeys(t)
	key, _ := GenerateKey()

	km := NewKeyMap()
	readers := map[string]*rsa.PublicKey{
		"owner":  &ownerKey.PublicKey,
		"reader": &readerKey.PublicKey,
	}
	if err := km.SetEpochKey(0, &key, readers); err != nil {
		t.Fatal(err)
	}

	got := km.Readers(0)
	if len(got) != 2 {
		t.Fatalf("Readers: got %d, want 2", len(got))
	}

	got = km.Readers(99)
	if got != nil {
		t.Fatalf("Readers for missing epoch should be nil, got %v", got)
	}
}

func TestKeyMapOpenRoundTrip(t *testing.T) {
	key0, _ := GenerateKey()
	key1, _ := GenerateKey()

	km := NewKeyMap()
	km.Open = true
	km.SetEpochKeyOpen(0, &key0)
	km.SetEpochKeyOpen(1, &key1)

	// Marshal/parse round-trip
	data, err := km.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	km2, err := ParseKeyMap(data)
	if err != nil {
		t.Fatalf("ParseKeyMap: %v", err)
	}
	if !km2.Open {
		t.Fatal("Open flag not preserved after round-trip")
	}

	// Anyone can read keys (address and privateKey ignored)
	got0, err := km2.GetKey(0, "", nil)
	if err != nil {
		t.Fatalf("GetKey epoch 0: %v", err)
	}
	if got0 != key0 {
		t.Fatal("epoch 0 key mismatch")
	}

	got1, err := km2.GetKey(1, "", nil)
	if err != nil {
		t.Fatalf("GetKey epoch 1: %v", err)
	}
	if got1 != key1 {
		t.Fatal("epoch 1 key mismatch")
	}
}

func TestKeyMapOpenMissingEpoch(t *testing.T) {
	km := NewKeyMap()
	km.Open = true
	_, err := km.GetKey(99, "", nil)
	if err == nil {
		t.Fatal("should fail for missing epoch in open keymap")
	}
}
