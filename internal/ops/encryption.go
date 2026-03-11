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
		// For forks (LastReaders populated by importForkEpochKeys), check
		// if a reader was removed and rotate to a new epoch if so.
		if len(es.LastReaders) > 0 {
			_, removed := diffReaders(es.LastReaders, localstate.Addresses(readers))
			if removed {
				return rotateKey(state)
			}
		}
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
	// Keys are indexed by the reader's RSA modulus (base64url), which is
	// what goar's wallet.Owner() returns — the value the reader will use
	// to look up their wrapped key when cloning.
	for _, r := range readers {
		if r.PubKey == "" || r.PubKey == ownerAddress {
			continue
		}
		pub, err := rsaPubKeyFromModulus(r.PubKey)
		if err != nil {
			return "", fmt.Errorf("ops: parse pubkey for reader %s: %w", r.Address, err)
		}
		pubKeys[r.PubKey] = pub
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

// buildOpenKeyMap creates an open keymap from local encryption state.
// Returns nil if there is no encryption state (repo was never private).
func buildOpenKeyMap(state *localstate.State) (*crypto.KeyMap, error) {
	es, err := state.LoadEncryption()
	if err != nil {
		return nil, err
	}
	if es == nil {
		return nil, nil
	}

	km := crypto.NewKeyMap()
	km.Open = true
	for epochStr, keyB64 := range es.EpochKeys {
		epoch, err := strconv.Atoi(epochStr)
		if err != nil {
			continue
		}
		keyBytes, err := base64.RawURLEncoding.DecodeString(keyB64)
		if err != nil {
			return nil, fmt.Errorf("ops: decode epoch %d key: %w", epoch, err)
		}
		var key [crypto.KeySize]byte
		copy(key[:], keyBytes)
		km.SetEpochKeyOpen(epoch, &key)
	}
	return km, nil
}

// uploadOpenKeyMap marshals and uploads an open keymap transaction.
func uploadOpenKeyMap(
	ctx context.Context,
	uploader arweave.Uploader,
	km *crypto.KeyMap,
	repoName string,
) (string, error) {
	data, err := km.Marshal()
	if err != nil {
		return "", fmt.Errorf("ops: marshal open keymap: %w", err)
	}
	return uploader.Upload(ctx, data, manifest.KeyMapTags(repoName))
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

// syncReadersFromKeyMap adds reader pubkeys from the keymap's latest epoch
// to the local readers list. Only the latest epoch is used because earlier
// epochs may contain readers that were intentionally removed (triggering
// key rotation). This ensures that when forking a private repo, the current
// set of readers (including the original owner) is preserved in the fork.
func syncReadersFromKeyMap(state *localstate.State, km *crypto.KeyMap) {
	epoch := km.LatestEpoch()
	if epoch < 0 {
		return
	}
	for _, pubkey := range km.Readers(epoch) {
		addr, err := crypto.OwnerToAddress(pubkey)
		if err != nil {
			continue // skip malformed entries
		}
		_, _ = state.AddReader(addr, pubkey)
	}
}

// importForkEpochKeys fetches the original repo's keymap and unwraps all epoch
// keys using the fork owner's private key. The keys are saved to encryption.json
// so that initEncryption finds pre-populated state and reuses the same symmetric
// keys. This ensures encrypted source packs remain decryptable in the fork.
func importForkEpochKeys(
	ctx context.Context,
	ar *arweave.Client,
	state *localstate.State,
	sourceKeymapTx string,
) error {
	// Fetch and parse the original keymap.
	kmData, err := ar.Fetch(ctx, sourceKeymapTx)
	if err != nil {
		return fmt.Errorf("ops: fetch source keymap %q: %w", sourceKeymapTx, err)
	}
	km, err := crypto.ParseKeyMap(kmData)
	if err != nil {
		return fmt.Errorf("ops: parse source keymap: %w", err)
	}

	// Unwrap all epoch keys using the fork owner's private key.
	epochKeys := make(map[string]string, len(km.Epochs))
	latestEpoch := -1
	for epochStr := range km.Epochs {
		epoch, err := strconv.Atoi(epochStr)
		if err != nil {
			continue
		}
		key, err := km.GetKey(epoch, ar.Owner(), ar.RSAPrivateKey())
		if err != nil {
			return fmt.Errorf("ops: unwrap epoch %d key from source keymap: %w", epoch, err)
		}
		epochKeys[epochStr] = base64.RawURLEncoding.EncodeToString(key[:])
		if epoch > latestEpoch {
			latestEpoch = epoch
		}
	}

	if latestEpoch < 0 {
		return fmt.Errorf("ops: source keymap has no epochs")
	}

	// Record the original keymap's latest-epoch readers as LastReaders so
	// that initEncryption can detect additions/removals and rotate if needed.
	var lastAddrs []string
	for _, pubkey := range km.Readers(latestEpoch) {
		addr, err := crypto.OwnerToAddress(pubkey)
		if err != nil {
			continue
		}
		lastAddrs = append(lastAddrs, addr)
	}

	es := &localstate.EncryptionState{
		CurrentEpoch: latestEpoch,
		EpochKeys:    epochKeys,
		LastReaders:  lastAddrs,
	}
	return state.SaveEncryption(es)
}

// hasEncryptedPacks returns true if any pack entry is marked as encrypted.
func hasEncryptedPacks(packs []manifest.PackEntry) bool {
	for _, pe := range packs {
		if pe.Encrypted {
			return true
		}
	}
	return false
}

// reuploadDecryptedPacks fetches and decrypts each encrypted source pack,
// then re-uploads it without encryption. Unencrypted packs are returned as-is.
func reuploadDecryptedPacks(
	ctx context.Context,
	ar *arweave.Client,
	uploader arweave.Uploader,
	state *localstate.State,
	packs []manifest.PackEntry,
	repoName string,
) ([]manifest.PackEntry, error) {
	// Load source keymap for decryption.
	sourceKeymapTx, _ := state.LoadSourceKeymap()
	if sourceKeymapTx == "" {
		return nil, fmt.Errorf("source keymap not available — cannot decrypt packs")
	}
	kmData, err := ar.Fetch(ctx, sourceKeymapTx)
	if err != nil {
		return nil, fmt.Errorf("fetch source keymap %q: %w", sourceKeymapTx, err)
	}
	km, err := crypto.ParseKeyMap(kmData)
	if err != nil {
		return nil, fmt.Errorf("parse source keymap: %w", err)
	}

	var result []manifest.PackEntry
	for _, pe := range packs {
		if !pe.Encrypted {
			result = append(result, pe)
			continue
		}

		// Fetch, decrypt, re-upload.
		data, err := ar.Fetch(ctx, pe.TX)
		if err != nil {
			return nil, fmt.Errorf("fetch encrypted pack %q: %w", pe.TX, err)
		}
		data, err = decryptPack(data, pe.Epoch, ar.Owner(), ar.RSAPrivateKey(), km)
		if err != nil {
			return nil, fmt.Errorf("decrypt pack %q: %w", pe.TX, err)
		}
		newTxID, err := uploader.Upload(ctx, data, manifest.PackTags(repoName, pe.Base, pe.Tip, ""))
		if err != nil {
			return nil, fmt.Errorf("re-upload pack: %w", err)
		}
		result = append(result, manifest.PackEntry{
			TX:   newTxID,
			Base: pe.Base,
			Tip:  pe.Tip,
			Size: int64(len(data)),
			// Epoch: 0, Encrypted: false — unencrypted
		})
	}
	return result, nil
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
