package crypto

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
)

// KeyMap holds symmetric keys for all epochs. In normal (private) mode, keys
// are RSA-wrapped per reader: epoch → wallet-address → base64url(wrapped-key).
// In open mode (used when converting private→public), keys are stored as
// plaintext under the sentinel address "*".
type KeyMap struct {
	Version int                          `json:"version"`
	Open    bool                         `json:"open,omitempty"`
	Epochs  map[string]map[string]string `json:"epochs"` // epoch (string) → address → wrapped key (or plaintext if Open)
}

// NewKeyMap creates an empty key map.
func NewKeyMap() *KeyMap {
	return &KeyMap{
		Version: 1,
		Epochs:  map[string]map[string]string{},
	}
}

// ParseKeyMap deserializes a key map from JSON.
func ParseKeyMap(data []byte) (*KeyMap, error) {
	var km KeyMap
	if err := json.Unmarshal(data, &km); err != nil {
		return nil, fmt.Errorf("keymap: parse error: %w", err)
	}
	if km.Version != 1 {
		return nil, fmt.Errorf("keymap: unsupported version %d", km.Version)
	}
	return &km, nil
}

// Marshal serializes the key map to JSON.
func (km *KeyMap) Marshal() ([]byte, error) {
	return json.Marshal(km)
}

// SetEpochKey wraps the given symmetric key for all readers and stores the
// results in the key map for the specified epoch. Any existing entries for
// this epoch are overwritten.
func (km *KeyMap) SetEpochKey(epoch int, symmetricKey *[KeySize]byte, readers map[string]*rsa.PublicKey) error {
	epochStr := strconv.Itoa(epoch)
	wrapped := make(map[string]string, len(readers))
	for addr, pub := range readers {
		w, err := WrapKey(symmetricKey, pub)
		if err != nil {
			return fmt.Errorf("keymap: wrap for %s: %w", addr, err)
		}
		wrapped[addr] = base64.RawURLEncoding.EncodeToString(w)
	}
	km.Epochs[epochStr] = wrapped
	return nil
}

// openSentinel is the address used to store plaintext keys in open keymaps.
const openSentinel = "*"

// SetEpochKeyOpen stores a plaintext (unwrapped) symmetric key for an epoch.
// Used when converting a private repo to public — all epoch keys are published
// so anyone can decrypt historical packs.
func (km *KeyMap) SetEpochKeyOpen(epoch int, symmetricKey *[KeySize]byte) {
	epochStr := strconv.Itoa(epoch)
	km.Epochs[epochStr] = map[string]string{
		openSentinel: base64.RawURLEncoding.EncodeToString(symmetricKey[:]),
	}
}

// GetKey retrieves and unwraps the symmetric key for the given epoch.
// For open keymaps, address and privateKey are ignored — the key is read directly.
// For normal keymaps, the key is RSA-unwrapped using the reader's private key.
func (km *KeyMap) GetKey(epoch int, address string, privateKey *rsa.PrivateKey) ([KeySize]byte, error) {
	epochStr := strconv.Itoa(epoch)
	readers, ok := km.Epochs[epochStr]
	if !ok {
		return [KeySize]byte{}, fmt.Errorf("keymap: epoch %d not found", epoch)
	}

	if km.Open {
		return km.getOpenKey(readers)
	}

	encoded, ok := readers[address]
	if !ok {
		return [KeySize]byte{}, fmt.Errorf("keymap: no key for address %s in epoch %d", address, epoch)
	}
	wrapped, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return [KeySize]byte{}, fmt.Errorf("keymap: decode wrapped key: %w", err)
	}
	return UnwrapKey(wrapped, privateKey)
}

// getOpenKey reads a plaintext key from an open keymap epoch entry.
func (km *KeyMap) getOpenKey(readers map[string]string) ([KeySize]byte, error) {
	encoded, ok := readers[openSentinel]
	if !ok {
		return [KeySize]byte{}, fmt.Errorf("keymap: open keymap missing sentinel key")
	}
	keyBytes, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return [KeySize]byte{}, fmt.Errorf("keymap: decode open key: %w", err)
	}
	if len(keyBytes) != KeySize {
		return [KeySize]byte{}, fmt.Errorf("keymap: open key wrong size: got %d, want %d", len(keyBytes), KeySize)
	}
	var key [KeySize]byte
	copy(key[:], keyBytes)
	return key, nil
}

// EpochCount returns the number of epochs in the key map.
func (km *KeyMap) EpochCount() int {
	return len(km.Epochs)
}

// LatestEpoch returns the highest epoch number in the key map.
// Returns -1 if the key map is empty.
func (km *KeyMap) LatestEpoch() int {
	max := -1
	for epochStr := range km.Epochs {
		n, err := strconv.Atoi(epochStr)
		if err != nil {
			continue
		}
		if n > max {
			max = n
		}
	}
	return max
}

// Readers returns the set of wallet addresses that have access in the given epoch.
func (km *KeyMap) Readers(epoch int) []string {
	epochStr := strconv.Itoa(epoch)
	readers, ok := km.Epochs[epochStr]
	if !ok {
		return nil
	}
	addrs := make([]string, 0, len(readers))
	for addr := range readers {
		addrs = append(addrs, addr)
	}
	return addrs
}
