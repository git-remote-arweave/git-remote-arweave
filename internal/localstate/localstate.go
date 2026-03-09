package localstate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git-remote-arweave/internal/manifest"
)

const (
	dirName          = "arweave"
	appliedPacksFile = "applied-packs"
	pendingJSONFile  = "pending.json"
	pendingPackFile  = "pending.pack"
	lastManifestFile = "last-manifest"
	sourcePacksFile    = "source-packs.json"
	sourceManifestFile = "source-manifest"
	genesisManifestFile = "genesis-manifest"
)

// State manages all local state stored under <gitDir>/arweave/.
type State struct {
	dir string // absolute path to <gitDir>/arweave/
}

// New creates a State rooted at <gitDir>/arweave/.
// The directory is created if it does not exist.
func New(gitDir string) (*State, error) {
	dir := filepath.Join(gitDir, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("localstate: create dir %q: %w", dir, err)
	}
	return &State{dir: dir}, nil
}

// --- applied-packs ---

// IsApplied reports whether a pack tx-id has already been applied locally.
func (s *State) IsApplied(txID string) (bool, error) {
	applied, err := s.loadAppliedPacks()
	if err != nil {
		return false, err
	}
	return applied[txID], nil
}

// MarkApplied adds tx-ids to the applied-packs set.
func (s *State) MarkApplied(txIDs ...string) error {
	applied, err := s.loadAppliedPacks()
	if err != nil {
		return err
	}
	for _, id := range txIDs {
		applied[id] = true
	}
	return s.saveAppliedPacks(applied)
}

// AppliedSet returns the full set of applied pack tx-ids.
func (s *State) AppliedSet() (map[string]bool, error) {
	return s.loadAppliedPacks()
}

func (s *State) loadAppliedPacks() (map[string]bool, error) {
	path := filepath.Join(s.dir, appliedPacksFile)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localstate: open applied-packs: %w", err)
	}
	defer func() { _ = f.Close() }()

	result := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			result[line] = true
		}
	}
	return result, scanner.Err()
}

func (s *State) saveAppliedPacks(applied map[string]bool) error {
	path := filepath.Join(s.dir, appliedPacksFile)
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("localstate: write applied-packs: %w", err)
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	for id := range applied {
		if _, err := fmt.Fprintln(w, id); err != nil {
			return fmt.Errorf("localstate: write applied pack id: %w", err)
		}
	}
	return w.Flush()
}

// --- pending push state ---

// PendingState holds the state of a push that has been uploaded
// but not yet confirmed on-chain.
type PendingState struct {
	PackTxID     string               `json:"pack_tx"`
	ManifestTxID string               `json:"manifest_tx"`
	ParentTxID   string               `json:"parent_tx"`            // parent used when building this manifest
	Refs         map[string]string    `json:"refs"`                 // full refs snapshot after this push
	Packs        []manifest.PackEntry `json:"packs,omitempty"`      // full pack list (for recovery when manifest is unfetchable)
	PackBase     string               `json:"pack_base"`            // Base commit SHA for pack tags
	PackTip      string               `json:"pack_tip"`             // Tip commit SHA for pack tags
	UploadedAt   time.Time            `json:"uploaded_at"`
	Guaranteed   bool                 `json:"guaranteed,omitempty"` // true for Turbo uploads (delivery guaranteed, no re-upload)
}

// SavePending writes the pending state and pack data to disk.
// packData is the raw packfile bytes kept for re-upload on drop.
// packData may be nil for ref-only updates (no new pack).
func (s *State) SavePending(state *PendingState, packData []byte) error {
	// write packfile (or remove stale one if nil)
	packPath := filepath.Join(s.dir, pendingPackFile)
	if packData != nil {
		if err := os.WriteFile(packPath, packData, 0o600); err != nil {
			return fmt.Errorf("localstate: write pending pack: %w", err)
		}
	} else {
		_ = os.Remove(packPath) // ignore error — file may not exist
	}

	// write JSON metadata
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("localstate: marshal pending state: %w", err)
	}
	jsonPath := filepath.Join(s.dir, pendingJSONFile)
	if err := os.WriteFile(jsonPath, data, 0o600); err != nil {
		return fmt.Errorf("localstate: write pending.json: %w", err)
	}
	return nil
}

// LoadPending reads the pending state and pack data.
// Returns nil, nil, nil if no pending state exists.
func (s *State) LoadPending() (*PendingState, []byte, error) {
	jsonPath := filepath.Join(s.dir, pendingJSONFile)
	data, err := os.ReadFile(jsonPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("localstate: read pending.json: %w", err)
	}

	var state PendingState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, nil, fmt.Errorf("localstate: parse pending.json: %w", err)
	}

	packPath := filepath.Join(s.dir, pendingPackFile)
	packData, err := os.ReadFile(packPath)
	if errors.Is(err, os.ErrNotExist) {
		// ref-only update — no pack data
		return &state, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("localstate: read pending pack: %w", err)
	}

	return &state, packData, nil
}

// ClearPending removes the pending state and pack data after confirmation.
func (s *State) ClearPending() error {
	jsonPath := filepath.Join(s.dir, pendingJSONFile)
	packPath := filepath.Join(s.dir, pendingPackFile)

	for _, path := range []string{jsonPath, packPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("localstate: clear pending: %w", err)
		}
	}
	return nil
}

// HasPending reports whether there is an unresolved pending push.
func (s *State) HasPending() bool {
	_, err := os.Stat(filepath.Join(s.dir, pendingJSONFile))
	return err == nil
}

// --- last confirmed manifest ---

// SaveLastManifest records the tx-id and parent tx-id of the last confirmed manifest.
// Used as Parent-Tx for the next push and for conflict detection.
func (s *State) SaveLastManifest(txID, parentTxID string) error {
	content := txID + "\n" + parentTxID
	return os.WriteFile(filepath.Join(s.dir, lastManifestFile), []byte(content), 0o600)
}

// SaveLastManifestTxID records only the tx-id (legacy convenience wrapper).
func (s *State) SaveLastManifestTxID(txID string) error {
	return s.SaveLastManifest(txID, "")
}

// LoadLastManifest returns the last confirmed manifest tx-id and its parent, or "" if none.
func (s *State) LoadLastManifest() (txID, parentTxID string, err error) {
	data, err := os.ReadFile(filepath.Join(s.dir, lastManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("localstate: read last-manifest: %w", err)
	}
	lines := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)
	txID = lines[0]
	if len(lines) > 1 {
		parentTxID = lines[1]
	}
	return txID, parentTxID, nil
}

// LoadLastManifestTxID returns the last confirmed manifest tx-id, or "" if none.
func (s *State) LoadLastManifestTxID() (string, error) {
	txID, _, err := s.LoadLastManifest()
	return txID, err
}

// --- source packs (for fork support) ---

// SaveSourcePacks stores the pack entries from a fetched manifest.
// When pushing to a new repository (fork), these entries are included
// in the genesis manifest so the fork reuses existing Arweave data
// instead of re-uploading everything.
func (s *State) SaveSourcePacks(packs []manifest.PackEntry) error {
	data, err := json.Marshal(packs)
	if err != nil {
		return fmt.Errorf("localstate: marshal source packs: %w", err)
	}
	return os.WriteFile(filepath.Join(s.dir, sourcePacksFile), data, 0o600)
}

// LoadSourcePacks returns the pack entries saved from a previous fetch.
// Returns nil, nil if no source packs exist (not a fork scenario).
func (s *State) LoadSourcePacks() ([]manifest.PackEntry, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, sourcePacksFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("localstate: read source packs: %w", err)
	}
	var packs []manifest.PackEntry
	if err := json.Unmarshal(data, &packs); err != nil {
		return nil, fmt.Errorf("localstate: parse source packs: %w", err)
	}
	return packs, nil
}

// ClearSourcePacks removes the source packs file.
// Called after the first fork push completes (packs are now in the manifest).
func (s *State) ClearSourcePacks() error {
	err := os.Remove(filepath.Join(s.dir, sourcePacksFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// SaveSourceManifest stores the manifest tx-id from the source repository.
// Used as the Forked-From tag value when pushing a fork genesis.
func (s *State) SaveSourceManifest(txID string) error {
	return os.WriteFile(filepath.Join(s.dir, sourceManifestFile), []byte(txID), 0o600)
}

// LoadSourceManifest returns the source manifest tx-id, or "" if none.
func (s *State) LoadSourceManifest() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, sourceManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("localstate: read source-manifest: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ClearSourceManifest removes the source manifest file.
func (s *State) ClearSourceManifest() error {
	err := os.Remove(filepath.Join(s.dir, sourceManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// --- genesis manifest ---

// SaveGenesisManifest stores the genesis manifest tx-id for the current chain.
func (s *State) SaveGenesisManifest(txID string) error {
	return os.WriteFile(filepath.Join(s.dir, genesisManifestFile), []byte(txID), 0o600)
}

// LoadGenesisManifest returns the genesis manifest tx-id, or "" if none.
func (s *State) LoadGenesisManifest() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, genesisManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("localstate: read genesis-manifest: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
