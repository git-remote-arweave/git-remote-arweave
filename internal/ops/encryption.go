package ops

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"math/big"
	"strconv"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/crypto"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
)

// encryptionContext holds all state needed for encryption during a push.
type encryptionContext struct {
	epoch    int
	key      [crypto.KeySize]byte
	keymapTx string // current keymap tx-id (may be reused or new)
	changed  bool   // true if keymap needs re-upload (readers changed)
}

// initEncryption loads or creates encryption state for a private repo push.
// It detects reader changes and handles key rotation.
func initEncryption(state *localstate.State) (*encryptionContext, error) {
	es, err := state.LoadEncryption()
	if err != nil {
		return nil, err
	}

	readers, err := state.LoadReaders()
	if err != nil {
		return nil, err
	}

	if es == nil {
		// First push as private — create epoch 0.
		key, err := crypto.GenerateKey()
		if err != nil {
			return nil, err
		}
		keyB64 := base64.RawURLEncoding.EncodeToString(key[:])
		es = &localstate.EncryptionState{
			CurrentEpoch: 0,
			EpochKeys:    map[string]string{"0": keyB64},
		}
		if err := state.SaveEncryption(es); err != nil {
			return nil, err
		}
		return &encryptionContext{epoch: 0, key: key, changed: true}, nil
	}

	// Load current epoch key.
	epochStr := strconv.Itoa(es.CurrentEpoch)
	keyB64, ok := es.EpochKeys[epochStr]
	if !ok {
		return nil, fmt.Errorf("ops: encryption key for epoch %d not found in local state", es.CurrentEpoch)
	}
	keyBytes, err := base64.RawURLEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("ops: decode epoch key: %w", err)
	}
	var key [crypto.KeySize]byte
	copy(key[:], keyBytes)

	// Check if readers changed since last keymap upload.
	if es.KeyMapTxID == "" {
		// No keymap uploaded yet — need to upload one.
		return &encryptionContext{epoch: es.CurrentEpoch, key: key, changed: true}, nil
	}

	added, removed := diffReaders(es.LastReaders, localstate.Addresses(readers))
	if removed {
		// Reader removed — rotate key to a new epoch.
		return rotateKey(state)
	}

	return &encryptionContext{
		epoch:    es.CurrentEpoch,
		key:      key,
		keymapTx: es.KeyMapTxID,
		changed:  added, // rebuild keymap if readers were added
	}, nil
}

// rotateKey creates a new epoch with a fresh symmetric key.
// Call this when a reader has been removed.
func rotateKey(state *localstate.State) (*encryptionContext, error) {
	es, err := state.LoadEncryption()
	if err != nil {
		return nil, err
	}
	if es == nil {
		return nil, fmt.Errorf("ops: cannot rotate key — no encryption state")
	}

	newEpoch := es.CurrentEpoch + 1
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, err
	}
	keyB64 := base64.RawURLEncoding.EncodeToString(key[:])
	es.CurrentEpoch = newEpoch
	es.EpochKeys[strconv.Itoa(newEpoch)] = keyB64

	if err := state.SaveEncryption(es); err != nil {
		return nil, err
	}

	return &encryptionContext{epoch: newEpoch, key: key, changed: true}, nil
}

// buildAndUploadKeyMap constructs a keymap from local encryption state,
// wraps all epoch keys for current readers, and uploads it.
func buildAndUploadKeyMap(
	ctx context.Context,
	uploader arweave.Uploader,
	state *localstate.State,
	repoName, ownerAddress string,
	ownerPubKey *rsa.PublicKey,
) (string, error) {
	es, err := state.LoadEncryption()
	if err != nil {
		return "", err
	}

	readers, err := state.LoadReaders()
	if err != nil {
		return "", err
	}

	// Build reader public key map. Owner is always included.
	pubKeys := map[string]*rsa.PublicKey{
		ownerAddress: ownerPubKey,
	}

	// Add readers that have a stored public key.
	for _, r := range readers {
		if r.PubKey == "" || r.Address == ownerAddress {
			continue
		}
		pub, err := rsaPubKeyFromModulus(r.PubKey)
		if err != nil {
			return "", fmt.Errorf("ops: parse pubkey for reader %s: %w", r.Address, err)
		}
		pubKeys[r.Address] = pub
	}

	km := crypto.NewKeyMap()
	for epochStr, keyB64 := range es.EpochKeys {
		epoch, err := strconv.Atoi(epochStr)
		if err != nil {
			continue
		}
		keyBytes, err := base64.RawURLEncoding.DecodeString(keyB64)
		if err != nil {
			return "", fmt.Errorf("ops: decode epoch %d key: %w", epoch, err)
		}
		var key [crypto.KeySize]byte
		copy(key[:], keyBytes)
		if err := km.SetEpochKey(epoch, &key, pubKeys); err != nil {
			return "", fmt.Errorf("ops: wrap epoch %d: %w", epoch, err)
		}
	}

	kmData, err := km.Marshal()
	if err != nil {
		return "", fmt.Errorf("ops: marshal keymap: %w", err)
	}

	txID, err := uploader.Upload(ctx, kmData, manifest.KeyMapTags(repoName))
	if err != nil {
		return "", fmt.Errorf("ops: upload keymap: %w", err)
	}

	// Update local state with new keymap tx-id and reader snapshot.
	es.KeyMapTxID = txID
	es.LastReaders = localstate.Addresses(readers)
	if err := state.SaveEncryption(es); err != nil {
		return "", fmt.Errorf("ops: save encryption state: %w", err)
	}

	return txID, nil
}

// encryptData encrypts data with the encryption context's current key.
func (ec *encryptionContext) encryptData(data []byte) ([]byte, error) {
	return crypto.Seal(data, &ec.key)
}

// decryptPack decrypts a pack using the appropriate epoch key from the keymap.
func decryptPack(
	data []byte,
	epoch int,
	ownerAddress string,
	privateKey *rsa.PrivateKey,
	km *crypto.KeyMap,
) ([]byte, error) {
	key, err := km.GetKey(epoch, ownerAddress, privateKey)
	if err != nil {
		return nil, fmt.Errorf("ops: unwrap key for epoch %d: %w", epoch, err)
	}
	return crypto.Open(data, &key)
}

// rsaPubKeyFromModulus reconstructs an RSA public key from a base64url-encoded
// modulus (n). Arweave uses a fixed public exponent of 65537 (0x10001).
func rsaPubKeyFromModulus(nB64 string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	return &rsa.PublicKey{N: n, E: 65537}, nil
}

// diffReaders compares current readers against lastReaders.
// Returns (added, removed) indicating if any reader was added or removed.
func diffReaders(lastReaders, currentReaders []string) (added, removed bool) {
	last := make(map[string]bool, len(lastReaders))
	for _, r := range lastReaders {
		last[r] = true
	}
	current := make(map[string]bool, len(currentReaders))
	for _, r := range currentReaders {
		current[r] = true
		if !last[r] {
			added = true
		}
	}
	for _, r := range lastReaders {
		if !current[r] {
			removed = true
		}
	}
	return added, removed
}
