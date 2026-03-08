package crypto

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"testing"
)

func TestSealOpen(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	plaintext := []byte("hello, private repo")
	box, err := Seal(plaintext, &key)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	if len(box) != len(plaintext)+Overhead {
		t.Fatalf("ciphertext size: got %d, want %d", len(box), len(plaintext)+Overhead)
	}

	got, err := Open(box, &key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("round-trip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestOpenWrongKey(t *testing.T) {
	key1, _ := GenerateKey()
	key2, _ := GenerateKey()

	box, _ := Seal([]byte("secret"), &key1)
	_, err := Open(box, &key2)
	if err == nil {
		t.Fatal("Open with wrong key should fail")
	}
}

func TestOpenTruncated(t *testing.T) {
	_, err := Open(make([]byte, Overhead-1), &[KeySize]byte{})
	if err == nil {
		t.Fatal("Open with truncated input should fail")
	}
}

func TestSealEmptyPlaintext(t *testing.T) {
	key, _ := GenerateKey()
	box, err := Seal(nil, &key)
	if err != nil {
		t.Fatalf("Seal empty: %v", err)
	}
	got, err := Open(box, &key)
	if err != nil {
		t.Fatalf("Open empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(got))
	}
}

func TestSealLargePayload(t *testing.T) {
	key, _ := GenerateKey()
	plaintext := make([]byte, 1<<20) // 1 MiB
	if _, err := rand.Read(plaintext); err != nil {
		t.Fatal(err)
	}
	box, err := Seal(plaintext, &key)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	got, err := Open(box, &key)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatal("round-trip mismatch for large payload")
	}
}

func TestWrapUnwrapKey(t *testing.T) {
	// RSA 4096 to match Arweave wallet keys
	privKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatalf("GenerateKey RSA: %v", err)
	}

	symKey, _ := GenerateKey()
	wrapped, err := WrapKey(&symKey, &privKey.PublicKey)
	if err != nil {
		t.Fatalf("WrapKey: %v", err)
	}

	got, err := UnwrapKey(wrapped, privKey)
	if err != nil {
		t.Fatalf("UnwrapKey: %v", err)
	}
	if got != symKey {
		t.Fatal("wrap/unwrap round-trip mismatch")
	}
}

func TestUnwrapWrongKey(t *testing.T) {
	priv1, _ := rsa.GenerateKey(rand.Reader, 4096)
	priv2, _ := rsa.GenerateKey(rand.Reader, 4096)

	symKey, _ := GenerateKey()
	wrapped, _ := WrapKey(&symKey, &priv1.PublicKey)

	_, err := UnwrapKey(wrapped, priv2)
	if err == nil {
		t.Fatal("UnwrapKey with wrong private key should fail")
	}
}

func TestSealNonDeterministic(t *testing.T) {
	key, _ := GenerateKey()
	plaintext := []byte("same input")

	box1, _ := Seal(plaintext, &key)
	box2, _ := Seal(plaintext, &key)
	if bytes.Equal(box1, box2) {
		t.Fatal("Seal should produce different ciphertext each time (random nonce)")
	}

	// But both should decrypt to the same plaintext
	p1, _ := Open(box1, &key)
	p2, _ := Open(box2, &key)
	if !bytes.Equal(p1, p2) {
		t.Fatal("both ciphertexts should decrypt to same plaintext")
	}
}
