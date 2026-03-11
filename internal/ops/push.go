package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
	"git-remote-arweave/internal/pack"
)

// PushInput describes a set of ref updates to push.
type PushInput struct {
	// RefUpdates maps ref names to their new commit SHAs.
	// A SHA of all-zeros means "delete this ref".
	RefUpdates map[string]string
	// Force creates a new genesis manifest with a full packfile,
	// ignoring any existing remote state.
	Force bool
	// ConfirmFunc prompts the user for confirmation. It displays the
	// prompt string and returns the user's input. Required for
	// destructive operations (e.g., making data public). If nil and
	// SkipConfirm is false, operations that need confirmation will
	// fail with an error.
	ConfirmFunc func(prompt string) (string, error)
	// SkipConfirm bypasses the interactive confirmation prompt.
	// Set via ARWEAVE_CONVERT_TO_PUBLIC=yes.
	SkipConfirm bool
}

// Push uploads new objects and updates the remote ref manifest.
// It handles pending state, conflict detection, pack generation,
// and manifest creation.
func Push(
	ctx context.Context,
	ar *arweave.Client,
	uploader arweave.Uploader,
	repo *git.Repository,
	state *localstate.State,
	cfg *config.Config,
	owner, repoName string,
	input *PushInput,
) (*PushResult, error) {
	// Verify wallet identity matches the remote URL owner.
	walletAddr := ar.Address()
	if walletAddr != "" && owner != "" && walletAddr != owner {
		return nil, fmt.Errorf("ops: wallet address %q does not match remote owner %q — check ARWEAVE_WALLET / arweave.wallet config", walletAddr, owner)
	}

	if input.Force {
		return forcePush(ctx, ar, uploader, repo, state, cfg, repoName, input)
	}

	// 1. Resolve any pending push.
	res, err := resolvePending(ctx, ar, uploader, state, cfg.DropTimeout, repoName)
	if err != nil {
		return nil, err
	}

	// 2. Load remote state.
	rs, err := LoadRemoteState(ctx, ar, owner, repoName)
	if err != nil {
		var mfe *ManifestFetchError
		hasPending := res.outcome == pendingInMempool || res.outcome == pendingReUploaded
		if errors.As(err, &mfe) && hasPending && (mfe.TxID == res.manifestTxID || mfe.TxID == res.parentTxID) {
			// The latest manifest in GraphQL is either our pending manifest
			// or its parent (both Turbo bundles not yet settled).
			// We can proceed using the pending state — no need to parse
			// the manifest body since effectiveState uses pending refs.
			rs = &RemoteState{manifestTxID: mfe.TxID}
		} else if errors.As(err, &mfe) && arweave.IsTransient(mfe.Err) {
			return nil, fmt.Errorf("ops: manifest %s temporarily unavailable (gateway error), try again later", mfe.TxID)
		} else {
			return nil, err
		}
	}

	// 3. Conflict detection: verify our known parent matches on-chain.
	//    When our local state is ahead of GraphQL (previous push delivered
	//    but not yet indexed), checkConflict returns an updated RemoteState
	//    based on the local manifest fetched from the gateway.
	rs, err = checkConflict(ctx, ar, rs, res, state)
	if err != nil {
		return nil, err
	}

	// 4. Compute effective state (refs and packs) from remote + pending.
	effectiveRefs, effectivePacks := effectiveState(rs, res)

	// 4a. Fork detection: if pushing to a new repo and we have source
	// packs from a previous fetch, include them so the fork reuses
	// existing Arweave data instead of re-uploading.
	var sourcePacks []manifest.PackEntry
	var forkedFrom string
	if rs.m == nil && len(effectivePacks) == 0 {
		sp, err := state.LoadSourcePacks()
		if err != nil {
			return nil, fmt.Errorf("ops: load source packs: %w", err)
		}
		if len(sp) > 0 {
			sourcePacks = sp
			effectivePacks = sp
			forkedFrom, _ = state.LoadSourceManifest()
		}
	}

	// 5. Apply ref updates.
	newRefs := mergeRefs(effectiveRefs, input.RefUpdates)

	// 6. Determine tips and bases for pack generation.
	tips, bases := computePackRange(input.RefUpdates, effectiveRefs)
	// For forks, add source pack tips as bases so we only generate the delta.
	if len(sourcePacks) > 0 {
		for _, pe := range sourcePacks {
			if pe.Tip != "" {
				bases = append(bases, plumbing.NewHash(pe.Tip))
			}
		}
	}
	// 6a. Fork encryption handling.
	if len(sourcePacks) > 0 && cfg.IsPrivate() {
		// Private fork: import epoch keys from the original keymap so that
		// encryptAndUpload reuses the same symmetric keys for source packs.
		sourceKeymap, _ := state.LoadSourceKeymap()
		if sourceKeymap != "" {
			if err := importForkEpochKeys(ctx, ar, state, sourceKeymap); err != nil {
				return nil, fmt.Errorf("ops: import fork epoch keys: %w", err)
			}
		}
	} else if len(sourcePacks) > 0 && !cfg.IsPrivate() && hasEncryptedPacks(sourcePacks) {
		// Public fork of private repo: re-upload source packs without
		// encryption so the public repo is self-contained and does not
		// expose the source repo's encryption keys.
		if err := confirmRepoName(input, repoName,
			"WARNING: creating a public fork of a private repo will re-upload all packs without encryption"); err != nil {
			return nil, err
		}
		decrypted, err := reuploadDecryptedPacks(ctx, ar, uploader, state, sourcePacks, repoName)
		if err != nil {
			return nil, fmt.Errorf("ops: re-upload decrypted packs: %w", err)
		}
		effectivePacks = decrypted
		forkedFrom = "" // independent public repo, no link to source
	}

	if len(tips) == 0 {
		// Ref-only update (e.g., delete). No new objects to upload.
		return uploadManifestOnly(ctx, uploader, state, repoName, rs, res, newRefs, effectivePacks, forkedFrom)
	}

	// 7. Generate packfile.
	packData, err := pack.Generate(repo, bases, tips)
	if err != nil {
		return nil, fmt.Errorf("ops: generate pack: %w", err)
	}
	if packData == nil && !cfg.IsPrivate() {
		// No new objects, public repo. Upload manifest only.
		result, err := uploadManifestOnly(ctx, uploader, state, repoName, rs, res, newRefs, effectivePacks, forkedFrom)
		if err != nil {
			return nil, err
		}
		if len(sourcePacks) > 0 {
			state.ClearSourceState()
		}
		return result, nil
	}
	// For private repos packData may be nil (e.g., pure fork) but we still
	// need encryptAndUpload to handle encryption, keymap, and encrypted manifest.

	// 7b. Private→public conversion: upload open keymap so historical
	// encrypted packs remain accessible to anyone.
	var openKeymapTx string
	if !cfg.IsPrivate() {
		openKM, err := buildOpenKeyMap(state)
		if err != nil {
			return nil, fmt.Errorf("ops: build open keymap: %w", err)
		}
		if openKM != nil {
			if err := confirmRepoName(input, repoName,
				"WARNING: switching to public will expose all historical encryption keys"); err != nil {
				return nil, err
			}
			openKeymapTx, err = uploadOpenKeyMap(ctx, uploader, openKM, repoName)
			if err != nil {
				return nil, fmt.Errorf("ops: upload open keymap: %w", err)
			}
		}
	}

	// 8. Encrypt, upload pack+keymap+manifest, save pending.
	baseSHA, tipSHA := tips[0].String(), tips[len(tips)-1].String()
	if len(bases) > 0 {
		baseSHA = bases[0].String()
	}
	parentTx := effectiveParentTx(rs, res)

	result, err := encryptAndUpload(ctx, ar, uploader, state, cfg, repoName, &uploadParams{
		packData:      packData,
		refs:          newRefs,
		existingPacks: effectivePacks,
		baseSHA:       baseSHA,
		tipSHA:        tipSHA,
		parentTx:      parentTx,
		forkedFrom:    forkedFrom,
		openKeymapTx:  openKeymapTx,
		extensions:    extensions(rs),
	})
	if err != nil {
		return nil, err
	}

	// Clean up source state after successful fork push.
	if len(sourcePacks) > 0 {
		state.ClearSourceState()
	}

	return result, nil
}

// forcePush creates a new genesis manifest with a full packfile,
// ignoring any existing remote state. Old manifests and packs
// remain on Arweave but are superseded by the new genesis.
func forcePush(
	ctx context.Context,
	ar *arweave.Client,
	uploader arweave.Uploader,
	repo *git.Repository,
	state *localstate.State,
	cfg *config.Config,
	repoName string,
	input *PushInput,
) (*PushResult, error) {
	// Clear per-remote state — start fresh.
	_ = state.ClearPending()
	_ = state.SaveLastManifestTxID("")

	// Load source packs before clearing — fork force push reuses them.
	sourcePacks, _ := state.LoadSourcePacks()
	forkedFrom, _ := state.LoadSourceManifest()

	// Collect tips.
	var tips []plumbing.Hash
	zeroHash := plumbing.ZeroHash.String()
	for _, sha := range input.RefUpdates {
		if sha != zeroHash {
			tips = append(tips, plumbing.NewHash(sha))
		}
	}
	if len(tips) == 0 {
		return nil, fmt.Errorf("ops: force push with no refs")
	}

	// Use source pack tips as bases so we only upload the delta.
	var bases []plumbing.Hash
	for _, pe := range sourcePacks {
		if pe.Tip != "" {
			bases = append(bases, plumbing.NewHash(pe.Tip))
		}
	}

	// Private fork: import epoch keys from the original keymap.
	if len(sourcePacks) > 0 && cfg.IsPrivate() {
		sourceKeymap, _ := state.LoadSourceKeymap()
		if sourceKeymap != "" {
			if err := importForkEpochKeys(ctx, ar, state, sourceKeymap); err != nil {
				return nil, fmt.Errorf("ops: import fork epoch keys: %w", err)
			}
		}
	}

	packData, err := pack.Generate(repo, bases, tips)
	if err != nil {
		return nil, fmt.Errorf("ops: generate pack: %w", err)
	}

	tipSHA := tips[0].String()
	baseSHA := ""
	if len(bases) > 0 {
		baseSHA = bases[0].String()
	}

	result, err := encryptAndUpload(ctx, ar, uploader, state, cfg, repoName, &uploadParams{
		packData:      packData,
		refs:          input.RefUpdates,
		existingPacks: sourcePacks,
		baseSHA:       baseSHA,
		tipSHA:        tipSHA,
		forkedFrom:    forkedFrom,
	})
	if err != nil {
		return nil, err
	}

	state.ClearSourceState()
	return result, nil
}

// confirmRepoName asks the user to type the repo name to confirm a destructive operation.
func confirmRepoName(input *PushInput, repoName, prompt string) error {
	if input.SkipConfirm {
		return nil
	}
	if input.ConfirmFunc == nil {
		return fmt.Errorf("ops: %s — set ARWEAVE_CONVERT_TO_PUBLIC=yes to skip this check", prompt)
	}
	answer, err := input.ConfirmFunc(fmt.Sprintf("%s\nType the repository name (%s) to confirm", prompt, repoName))
	if err != nil {
		return fmt.Errorf("ops: confirmation failed: %w", err)
	}
	if strings.TrimSpace(answer) != repoName {
		return fmt.Errorf("ops: repository name mismatch, aborting")
	}
	return nil
}

// effectiveState returns the refs and packs to use as a base for the new manifest.
// When a push is still in mempool, we use the pending push's refs snapshot
// (which already includes the on-chain refs + the pending push's updates).
func effectiveState(rs *RemoteState, res *pendingResolution) (map[string]string, []manifest.PackEntry) {
	hasPending := res.outcome == pendingInMempool || res.outcome == pendingReUploaded

	// Refs: use pending refs if available (they are a superset of on-chain refs).
	var refs map[string]string
	if hasPending && res.refs != nil {
		refs = make(map[string]string, len(res.refs))
		for k, v := range res.refs {
			refs[k] = v
		}
	} else if rs.m != nil {
		refs = make(map[string]string, len(rs.m.Refs))
		for k, v := range rs.m.Refs {
			refs[k] = v
		}
	} else {
		refs = map[string]string{}
	}

	// Packs: on-chain packs + pending pack.
	// When rs.m is nil but we have pending packs (e.g., on-chain manifest
	// unfetchable), use the pending pack list which includes the full history.
	var packs []manifest.PackEntry
	if rs.m != nil {
		packs = make([]manifest.PackEntry, len(rs.m.Packs))
		copy(packs, rs.m.Packs)
		if hasPending {
			packs = append(packs, manifest.PackEntry{TX: res.packTxID})
		}
	} else if hasPending && len(res.packs) > 0 {
		packs = make([]manifest.PackEntry, len(res.packs))
		copy(packs, res.packs)
	} else if hasPending {
		packs = []manifest.PackEntry{{TX: res.packTxID}}
	}

	return refs, packs
}

// effectiveParentTx returns the parent tx to use for the new manifest.
func effectiveParentTx(rs *RemoteState, res *pendingResolution) string {
	// If there's a pending push in mempool, the new manifest chains from it
	// (we don't have its tx-id yet since it hasn't confirmed). In this case
	// we chain from the on-chain manifest (same parent as the pending).
	// The pending manifest will confirm and become the actual parent; if it
	// doesn't confirm and gets re-uploaded, both will share the same parent.
	return rs.manifestTxID
}

// mergeRefs applies updates to the base refs.
func mergeRefs(base, updates map[string]string) map[string]string {
	merged := make(map[string]string, len(base))
	for k, v := range base {
		merged[k] = v
	}
	zeroHash := plumbing.ZeroHash.String()
	for k, v := range updates {
		if v == zeroHash {
			delete(merged, k)
		} else {
			merged[k] = v
		}
	}
	return merged
}

// computePackRange determines tips and bases for pack generation.
func computePackRange(updates, currentRefs map[string]string) (tips, bases []plumbing.Hash) {
	zeroHash := plumbing.ZeroHash.String()
	for _, sha := range updates {
		if sha != zeroHash {
			tips = append(tips, plumbing.NewHash(sha))
		}
	}
	for _, sha := range currentRefs {
		bases = append(bases, plumbing.NewHash(sha))
	}
	return tips, bases
}

// extensions returns the extensions map from the remote state, or nil.
func extensions(rs *RemoteState) map[string]json.RawMessage {
	if rs.m != nil {
		return rs.m.Extensions
	}
	return nil
}
