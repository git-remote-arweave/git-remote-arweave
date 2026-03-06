package ops

import (
	"context"
	"fmt"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/manifest"
)

// PushResult contains information about a completed push operation.
type PushResult struct {
	PackTxID     string
	ManifestTxID string
}

// FetchResult contains the refs from the latest remote manifest.
type FetchResult struct {
	Refs map[string]string // ref name → commit SHA
}

// remoteState is the parsed state of the remote repository.
type remoteState struct {
	manifestTxID string
	m            *manifest.Manifest // nil if repository does not exist yet
}

// loadRemoteState queries and parses the latest manifest for (owner, repoName).
// rs.m is nil when the repository has no manifests yet (new repo).
func loadRemoteState(ctx context.Context, ar *arweave.Client, owner, repoName string) (*remoteState, error) {
	info, err := ar.QueryLatestManifest(ctx, owner, repoName)
	if err != nil {
		return nil, fmt.Errorf("ops: query manifest: %w", err)
	}
	if info == nil {
		return &remoteState{}, nil
	}

	data, err := ar.Fetch(ctx, info.TxID)
	if err != nil {
		return nil, fmt.Errorf("ops: fetch manifest body %q: %w", info.TxID, err)
	}

	m, err := manifest.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("ops: parse manifest %q: %w", info.TxID, err)
	}

	return &remoteState{manifestTxID: info.TxID, m: m}, nil
}
