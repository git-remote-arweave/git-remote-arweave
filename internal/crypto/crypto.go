// Package crypto provides encryption primitives for private repositories.
//
// Pack data and manifest bodies are encrypted with NaCl secretbox
// (XSalsa20-Poly1305). The 32-byte symmetric key is wrapped per-reader
// using RSA-OAEP with SHA-256, leveraging Arweave wallet RSA keys directly.
package crypto

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/nacl/secretbox"
)

const (
	// KeySize is the NaCl secretbox key size in bytes.
	KeySize = 32
	// NonceSize is the NaCl secretbox nonce size in bytes.
	NonceSize = 24
	// Overhead is the total byte overhead added by Seal: nonce + Poly1305 tag.
	Overhead = NonceSize + secretbox.Overhead // 24 + 16 = 40
)

// GenerateKey creates a new random 32-byte symmetric key.
func GenerateKey() ([KeySize]byte, error) {
	var key [KeySize]byte
	if _, err := io.ReadFull(rand.Reader, key[:]); err != nil {
		return key, fmt.Errorf("crypto: generate key: %w", err)
	}
	return key, nil
}

// Seal encrypts plaintext with the given key using NaCl secretbox.
// Returns: [24-byte nonce][ciphertext with 16-byte Poly1305 auth tag].
func Seal(plaintext []byte, key *[KeySize]byte) ([]byte, error) {
	var nonce [NonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return nil, fmt.Errorf("crypto: generate nonce: %w", err)
	}
	out := make([]byte, NonceSize)
	copy(out, nonce[:])
	out = secretbox.Seal(out, plaintext, &nonce, key)
	return out, nil
}

// Open decrypts ciphertext produced by Seal.
func Open(box []byte, key *[KeySize]byte) ([]byte, error) {
	if len(box) < Overhead {
		return nil, fmt.Errorf("crypto: ciphertext too short (%d bytes)", len(box))
	}
	var nonce [NonceSize]byte
	copy(nonce[:], box[:NonceSize])
	plaintext, ok := secretbox.Open(nil, box[NonceSize:], &nonce, key)
	if !ok {
		return nil, fmt.Errorf("crypto: decryption failed (wrong key or corrupted data)")
	}
	return plaintext, nil
}

// OwnerToAddress derives an Arweave wallet address from an owner key
// (base64url-encoded RSA modulus). The address is base64url(SHA-256(raw_modulus)).
func OwnerToAddress(owner string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(owner)
	if err != nil {
		return "", fmt.Errorf("crypto: decode owner key: %w", err)
	}
	hash := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(hash[:]), nil
}

// WrapKey encrypts a symmetric key for a reader using RSA-OAEP with SHA-256.
func WrapKey(symmetricKey *[KeySize]byte, recipientPub *rsa.PublicKey) ([]byte, error) {
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, recipientPub, symmetricKey[:], nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: wrap key: %w", err)
	}
	return wrapped, nil
}

// UnwrapKey decrypts a wrapped symmetric key using the reader's RSA private key.
func UnwrapKey(wrapped []byte, privateKey *rsa.PrivateKey) ([KeySize]byte, error) {
	var key [KeySize]byte
	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, privateKey, wrapped, nil)
	if err != nil {
		return key, fmt.Errorf("crypto: unwrap key: %w", err)
	}
	if len(plaintext) != KeySize {
		return key, fmt.Errorf("crypto: unwrapped key has wrong size %d (expected %d)", len(plaintext), KeySize)
	}
	copy(key[:], plaintext)
	return key, nil
}
