package ops

import (
	"context"
	"fmt"
	"time"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
)

// pendingOutcome describes the result of resolving a pending push.
type pendingOutcome int

const (
	// noPending means there was no pending push to resolve.
	noPending pendingOutcome = iota
	// pendingConfirmed means the pending push was confirmed on-chain.
	pendingConfirmed
	// pendingInMempool means the pending push is still in the mempool.
	pendingInMempool
	// pendingReUploaded means the pending push was dropped and the pack
	// was re-uploaded. The caller must build a new manifest.
	pendingReUploaded
)

// pendingResolution contains the resolved pending state and its outcome.
type pendingResolution struct {
	outcome pendingOutcome
	// packTxID is the pack tx-id to include in the next manifest.
	// Set when outcome is pendingInMempool or pendingReUploaded.
	packTxID string
	// manifestTxID is the pending manifest's tx-id.
	// Set when outcome is pendingInMempool or pendingReUploaded.
	manifestTxID string
	// parentTxID is the Parent-Tx the pending manifest was built against.
	// Set when outcome is pendingInMempool or pendingReUploaded.
	parentTxID string
	// refs is the full refs snapshot from the pending push.
	// Set when outcome is pendingInMempool or pendingReUploaded.
	refs map[string]string
}

// resolvePending checks and resolves any pending push state.
// It promotes confirmed pushes, re-uploads dropped packs, and returns
// the outcome so Push can determine the effective parent and packs.
func resolvePending(
	ctx context.Context,
	ar *arweave.Client,
	uploader arweave.Uploader,
	state *localstate.State,
	dropTimeout time.Duration,
	repoName string,
) (*pendingResolution, error) {
	pending, packData, err := state.LoadPending()
	if err != nil {
		return nil, fmt.Errorf("ops: load pending: %w", err)
	}
	if pending == nil {
		return &pendingResolution{outcome: noPending}, nil
	}

	// For guaranteed uploads (Turbo), check if the data is accessible
	// via the gateway. Turbo bundles data items (ANS-104) which are not
	// visible to goar's L1 tx/{id}/status endpoint, but are available
	// through the gateway's /{id} endpoint once indexed.
	if pending.Guaranteed {
		_, fetchErr := ar.Fetch(ctx, pending.ManifestTxID)
		if fetchErr == nil {
			// Data is accessible — treat as confirmed.
			if err := state.MarkApplied(pending.PackTxID); err != nil {
				return nil, fmt.Errorf("ops: mark applied: %w", err)
			}
			if err := state.SaveLastManifestTxID(pending.ManifestTxID); err != nil {
				return nil, fmt.Errorf("ops: save last manifest: %w", err)
			}
			if err := state.ClearPending(); err != nil {
				return nil, fmt.Errorf("ops: clear pending: %w", err)
			}
			return &pendingResolution{outcome: pendingConfirmed}, nil
		}
		// Not yet accessible — keep waiting.
		return &pendingResolution{
			outcome:      pendingInMempool,
			packTxID:     pending.PackTxID,
			manifestTxID: pending.ManifestTxID,
			parentTxID:   pending.ParentTxID,
			refs:         pending.Refs,
		}, nil
	}

	status, err := ar.TxStatus(ctx, pending.ManifestTxID)
	if err != nil {
		return nil, fmt.Errorf("ops: check tx status %q: %w", pending.ManifestTxID, err)
	}

	switch status {
	case arweave.StatusConfirmed:
		if err := state.MarkApplied(pending.PackTxID); err != nil {
			return nil, fmt.Errorf("ops: mark applied: %w", err)
		}
		if err := state.SaveLastManifestTxID(pending.ManifestTxID); err != nil {
			return nil, fmt.Errorf("ops: save last manifest: %w", err)
		}
		if err := state.ClearPending(); err != nil {
			return nil, fmt.Errorf("ops: clear pending: %w", err)
		}
		return &pendingResolution{outcome: pendingConfirmed}, nil

	case arweave.StatusPending:
		return &pendingResolution{
			outcome:      pendingInMempool,
			packTxID:     pending.PackTxID,
			manifestTxID: pending.ManifestTxID,
			parentTxID:   pending.ParentTxID,
			refs:         pending.Refs,
		}, nil

	case arweave.StatusNotFound:

		// Apply drop timeout: if uploaded recently, treat as still pending.
		if time.Since(pending.UploadedAt) < dropTimeout {
			return &pendingResolution{
				outcome:    pendingInMempool,
				packTxID:   pending.PackTxID,
				parentTxID: pending.ParentTxID,
				refs:       pending.Refs,
			}, nil
		}

		// Dropped — re-upload the pack. Push will build a fresh manifest.
		newPackTxID, err := uploader.Upload(ctx, packData, manifest.PackTags(repoName, pending.PackBase, pending.PackTip))
		if err != nil {
			return nil, fmt.Errorf("ops: re-upload pack: %w", err)
		}

		// Update pending with new pack tx-id and reset timer.
		newPending := &localstate.PendingState{
			PackTxID:   newPackTxID,
			ParentTxID: pending.ParentTxID,
			Refs:       pending.Refs,
			PackBase:   pending.PackBase,
			PackTip:    pending.PackTip,
			UploadedAt: time.Now(),
		}
		if err := state.SavePending(newPending, packData); err != nil {
			return nil, fmt.Errorf("ops: save re-uploaded pending: %w", err)
		}

		return &pendingResolution{
			outcome:      pendingReUploaded,
			packTxID:     newPackTxID,
			manifestTxID: pending.ManifestTxID,
			parentTxID:   pending.ParentTxID,
			refs:         pending.Refs,
		}, nil

	default:
		return nil, fmt.Errorf("ops: unexpected tx status %d for %q", status, pending.ManifestTxID)
	}
}
