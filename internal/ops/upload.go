package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/manifest"
)

// uploadParams describes what to upload after pack generation.
type uploadParams struct {
	packData      []byte
	refs          map[string]string
	existingPacks []manifest.PackEntry               // packs from previous manifests
	baseSHA       string                              // "" for genesis/force
	tipSHA        string
	parentTx      string                              // "" for genesis/force
	forkedFrom    string                              // source manifest tx for forks
	openKeymapTx  string                              // private→public conversion keymap
	extensions    map[string]json.RawMessage
}

// encryptAndUpload handles the common tail of both normal and force push:
// encrypt pack → upload pack → upload keymap → build manifest → encrypt
// manifest → upload manifest → save genesis → save pending.
func encryptAndUpload(
	ctx context.Context,
	ar *arweave.Client,
	uploader arweave.Uploader,
	state *localstate.State,
	cfg *config.Config,
	repoName string,
	p *uploadParams,
) (*PushResult, error) {
	packData := p.packData

	// 1. Encryption (private repos).
	var ec *encryptionContext
	visibility := ""
	keymapTx := p.openKeymapTx // may be set by private→public conversion
	epoch := 0
	if cfg.IsPrivate() {
		visibility = manifest.VisibilityPrivate
		if _, err := state.AddReader(ar.Address(), ar.Owner()); err != nil {
			return nil, fmt.Errorf("ops: ensure owner in readers: %w", err)
		}
		var err error
		ec, err = initEncryption(state)
		if err != nil {
			return nil, fmt.Errorf("ops: init encryption: %w", err)
		}
		epoch = ec.epoch
		if packData != nil {
			packData, err = ec.encryptData(packData)
			if err != nil {
				return nil, fmt.Errorf("ops: encrypt pack: %w", err)
			}
		}
	}

	// 2. Upload pack (skipped for manifest-only pushes like pure forks).
	var packTxID string
	if packData != nil {
		var err error
		packTxID, err = uploader.Upload(ctx, packData, manifest.PackTags(repoName, p.baseSHA, p.tipSHA, visibility))
		if err != nil {
			return nil, fmt.Errorf("ops: upload pack: %w", err)
		}
	}

	// 3. Upload keymap if needed (private repos).
	if ec != nil {
		var err error
		if ec.changed {
			keymapTx, err = buildAndUploadKeyMap(ctx, uploader, state, repoName, ar.Owner(), ar.RSAPublicKey())
			if err != nil {
				return nil, fmt.Errorf("ops: upload keymap: %w", err)
			}
		} else {
			keymapTx = ec.keymapTx
		}
	}

	// 4. Build manifest.
	allPacks := make([]manifest.PackEntry, len(p.existingPacks))
	copy(allPacks, p.existingPacks)
	if packTxID != "" {
		allPacks = append(allPacks, manifest.PackEntry{
			TX:        packTxID,
			Base:      p.baseSHA,
			Tip:       p.tipSHA,
			Size:      int64(len(packData)),
			Epoch:     epoch,
			Encrypted: ec != nil,
		})
	}

	var m *manifest.Manifest
	if p.parentTx == "" {
		m = manifest.NewGenesis()
		m.Refs = p.refs
		m.Packs = allPacks
	} else {
		m = manifest.New(p.refs, allPacks, p.parentTx, p.extensions)
	}
	m.KeyMap = keymapTx

	manifestData, err := m.Marshal()
	if err != nil {
		return nil, fmt.Errorf("ops: marshal manifest: %w", err)
	}

	// 5. Encrypt manifest body (private repos).
	if ec != nil {
		manifestData, err = ec.encryptData(manifestData)
		if err != nil {
			return nil, fmt.Errorf("ops: encrypt manifest: %w", err)
		}
	}

	// 6. Upload manifest.
	genesisTx, _ := state.LoadGenesisManifest()
	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(manifest.RefsTagsOpts{
		RepoName:   repoName,
		ParentTx:   p.parentTx,
		Visibility: visibility,
		KeyMapTx:   keymapTx,
		ForkedFrom: p.forkedFrom,
		GenesisTx:  genesisTx,
		Encrypted:  ec != nil,
	}))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	// 7. Save genesis tx-id on genesis push.
	if p.parentTx == "" {
		_ = state.SaveGenesisManifest(manifestTxID)
	}

	// 8. Save pending state.
	pending := &localstate.PendingState{
		PackTxID:     packTxID,
		ManifestTxID: manifestTxID,
		ParentTxID:   p.parentTx,
		Refs:         p.refs,
		Packs:        allPacks,
		PackBase:     p.baseSHA,
		PackTip:      p.tipSHA,
		UploadedAt:   time.Now(),
		Guaranteed:   uploader.Guaranteed(),
	}
	if err := state.SavePending(pending, packData); err != nil {
		return nil, fmt.Errorf("ops: save pending: %w", err)
	}

	return &PushResult{PackTxID: packTxID, ManifestTxID: manifestTxID, BytesUploaded: len(packData) + len(manifestData)}, nil
}

// uploadManifestOnly handles ref-only updates (no new pack data).
func uploadManifestOnly(
	ctx context.Context,
	uploader arweave.Uploader,
	state *localstate.State,
	repoName string,
	rs *RemoteState,
	res *pendingResolution,
	newRefs map[string]string,
	packs []manifest.PackEntry,
	forkedFrom string,
) (*PushResult, error) {
	parentTx := effectiveParentTx(rs, res)

	var m *manifest.Manifest
	if parentTx == "" {
		m = manifest.NewGenesis()
		m.Refs = newRefs
		m.Packs = packs
	} else {
		m = manifest.New(newRefs, packs, parentTx, extensions(rs))
	}

	manifestData, err := m.Marshal()
	if err != nil {
		return nil, fmt.Errorf("ops: marshal manifest: %w", err)
	}

	genesisTx, _ := state.LoadGenesisManifest()
	manifestTxID, err := uploader.Upload(ctx, manifestData, manifest.RefsTags(manifest.RefsTagsOpts{
		RepoName:   repoName,
		ParentTx:   parentTx,
		ForkedFrom: forkedFrom,
		GenesisTx:  genesisTx,
	}))
	if err != nil {
		return nil, fmt.Errorf("ops: upload manifest: %w", err)
	}

	if parentTx == "" {
		_ = state.SaveGenesisManifest(manifestTxID)
	}

	pending := &localstate.PendingState{
		ManifestTxID: manifestTxID,
		ParentTxID:   parentTx,
		Refs:         newRefs,
		Packs:        packs,
		UploadedAt:   time.Now(),
		Guaranteed:   uploader.Guaranteed(),
	}
	if err := state.SavePending(pending, nil); err != nil {
		return nil, fmt.Errorf("ops: save pending: %w", err)
	}

	return &PushResult{ManifestTxID: manifestTxID, BytesUploaded: len(manifestData)}, nil
}
