package localstate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	readersFile       = "readers"
	encryptionFile    = "encryption.json"
)

// EncryptionState holds local encryption metadata for private repos.
type EncryptionState struct {
	// CurrentEpoch is the active encryption epoch.
	CurrentEpoch int `json:"current_epoch"`
	// KeyMapTxID is the tx-id of the current on-chain keymap.
	KeyMapTxID string `json:"keymap_tx"`
	// EpochKeys maps epoch number (as string) to the base64url-encoded
	// symmetric key. Stored locally for the owner only — never uploaded.
	EpochKeys map[string]string `json:"epoch_keys"`
	// LastReaders is the reader list at the time of the last keymap upload.
	// Used to detect reader additions/removals.
	LastReaders []string `json:"last_readers,omitempty"`
}

// SaveEncryption writes the encryption state to disk.
func (s *State) SaveEncryption(es *EncryptionState) error {
	data, err := json.Marshal(es)
	if err != nil {
		return fmt.Errorf("localstate: marshal encryption state: %w", err)
	}
	return os.WriteFile(filepath.Join(s.dir, encryptionFile), data, 0o600)
}

// LoadEncryption reads the encryption state.
// Returns nil, nil if no encryption state exists.
func (s *State) LoadEncryption() (*EncryptionState, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, encryptionFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localstate: read encryption state: %w", err)
	}
	var es EncryptionState
	if err := json.Unmarshal(data, &es); err != nil {
		return nil, fmt.Errorf("localstate: parse encryption state: %w", err)
	}
	return &es, nil
}

// --- reader management ---

// Reader represents an authorized reader with an optional public key.
type Reader struct {
	Address string // Arweave wallet address
	PubKey  string // base64url-encoded RSA modulus (n), empty if not provided
}

// Addresses returns just the wallet addresses from a reader list.
func Addresses(readers []Reader) []string {
	addrs := make([]string, len(readers))
	for i, r := range readers {
		addrs[i] = r.Address
	}
	return addrs
}

// LoadReaders returns the list of readers with their optional public keys.
// File format: one reader per line, "address" or "address\tpubkey".
func (s *State) LoadReaders() ([]Reader, error) {
	path := filepath.Join(s.dir, readersFile)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localstate: open readers: %w", err)
	}
	defer func() { _ = f.Close() }()

	var readers []Reader
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		r := Reader{Address: parts[0]}
		if len(parts) == 2 {
			r.PubKey = parts[1]
		}
		readers = append(readers, r)
	}
	return readers, scanner.Err()
}

// SaveReaders writes the reader list to disk.
func (s *State) SaveReaders(readers []Reader) error {
	path := filepath.Join(s.dir, readersFile)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("localstate: write readers: %w", err)
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	for _, r := range readers {
		line := r.Address
		if r.PubKey != "" {
			line += "\t" + r.PubKey
		}
		if _, err := fmt.Fprintln(w, line); err != nil {
			return fmt.Errorf("localstate: write reader: %w", err)
		}
	}
	return w.Flush()
}

// AddReader adds a reader to the list if not already present.
// If the address exists but had no pubkey, and pubkey is now provided, it updates the entry.
// Returns true if the reader list was modified.
func (s *State) AddReader(address, pubkey string) (bool, error) {
	readers, err := s.LoadReaders()
	if err != nil {
		return false, err
	}
	for i, r := range readers {
		if r.Address == address {
			if r.PubKey == "" && pubkey != "" {
				// Update existing entry with newly provided pubkey.
				readers[i].PubKey = pubkey
				return true, s.SaveReaders(readers)
			}
			return false, nil
		}
	}
	readers = append(readers, Reader{Address: address, PubKey: pubkey})
	return true, s.SaveReaders(readers)
}

// RemoveReader removes a wallet address from the reader list.
// Returns true if the reader was found and removed.
func (s *State) RemoveReader(address string) (bool, error) {
	readers, err := s.LoadReaders()
	if err != nil {
		return false, err
	}
	for i, r := range readers {
		if r.Address == address {
			readers = append(readers[:i], readers[i+1:]...)
			return true, s.SaveReaders(readers)
		}
	}
	return false, nil
}
