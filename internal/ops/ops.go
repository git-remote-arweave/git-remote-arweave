package ops

import (
	"context"
	"fmt"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/manifest"
)

// PushResult contains information about a completed push operation.
type PushResult struct {
	PackTxID      string
	ManifestTxID  string
	BytesUploaded int // total bytes uploaded (pack + manifest)
}

// FetchResult contains the refs from the latest remote manifest.
type FetchResult struct {
	Refs map[string]string // ref name → commit SHA
}

// RemoteState is the parsed state of the remote repository.
// Load it once with LoadRemoteState and pass to ListRefs / Fetch / Push.
type RemoteState struct {
	manifestTxID string
	m            *manifest.Manifest // nil if repository does not exist yet
}

// LoadRemoteState queries and parses the latest manifest for (owner, repoName).
// rs.m is nil when the repository has no manifests yet (new repo).
// If the manifest exists in GraphQL but its body cannot be fetched (e.g.,
// bundled via a devnet bundler whose data items are not indexed), the
// error is returned so callers can decide how to handle it.
func LoadRemoteState(ctx context.Context, ar *arweave.Client, owner, repoName string) (*RemoteState, error) {
	info, err := ar.QueryLatestManifest(ctx, owner, repoName)
	if err != nil {
		return nil, fmt.Errorf("ops: query manifest: %w", err)
	}
	if info == nil {
		return &RemoteState{}, nil
	}

	data, err := ar.Fetch(ctx, info.TxID)
	if err != nil {
		return nil, &ManifestFetchError{TxID: info.TxID, Err: err}
	}

	m, err := manifest.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("ops: parse manifest %q: %w", info.TxID, err)
	}

	return &RemoteState{manifestTxID: info.TxID, m: m}, nil
}

// ManifestFetchError indicates that a manifest was found via GraphQL
// but its body could not be downloaded. This can happen when data items
// are bundled by an untrusted bundler (e.g., Turbo devnet).
type ManifestFetchError struct {
	TxID string
	Err  error
}

func (e *ManifestFetchError) Error() string {
	return fmt.Sprintf("ops: fetch manifest body %q: %v", e.TxID, e.Err)
}

func (e *ManifestFetchError) Unwrap() error { return e.Err }
