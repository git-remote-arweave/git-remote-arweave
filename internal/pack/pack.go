package pack

import (
	"bytes"
	"fmt"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/go-git/go-git/v5/plumbing/revlist"
)

const deltaWindowSize = 10

// Generate creates a packfile containing all objects reachable from tips
// but not reachable from bases (objects the remote already has).
// Pass nil or empty bases to include all objects (e.g. on first push or clone).
// Returns nil, nil if all tips are already reachable from bases (no new objects).
func Generate(repo *git.Repository, bases, tips []plumbing.Hash) ([]byte, error) {
	if len(tips) == 0 {
		return nil, fmt.Errorf("pack: no tips specified")
	}

	objects, err := revlist.Objects(repo.Storer, tips, bases)
	if err != nil {
		return nil, fmt.Errorf("pack: revlist error: %w", err)
	}
	if len(objects) == 0 {
		return nil, nil
	}

	var buf bytes.Buffer
	enc := packfile.NewEncoder(&buf, repo.Storer, false)
	if _, err := enc.Encode(objects, deltaWindowSize); err != nil {
		return nil, fmt.Errorf("pack: encode error: %w", err)
	}

	return buf.Bytes(), nil
}

// Apply writes all objects from a packfile into the repository's object storage.
func Apply(repo *git.Repository, data []byte) error {
	if err := packfile.UpdateObjectStorage(repo.Storer, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("pack: apply error: %w", err)
	}
	return nil
}
