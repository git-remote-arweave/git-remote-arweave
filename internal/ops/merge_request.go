package ops

import (
	"context"
	"fmt"
	"strings"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/manifest"
)

// MergeRequestStatus represents the resolved status of a merge request.
type MergeRequestStatus string

const (
	MergeRequestOpen   MergeRequestStatus = "open"
	MergeRequestMerged MergeRequestStatus = "merged"
	MergeRequestClosed MergeRequestStatus = "closed"
)

// ResolvedUpdate holds parsed body info for a validated update transaction.
type ResolvedUpdate struct {
	arweave.MergeRequestTx
	Merged         bool   // from body
	Status         string // from body ("open" or "closed"), empty for merge updates
	SourceManifest string // from body (only status updates from MR author)
	Message        string // from body (only status updates from MR author)
}

// MergeRequestSummary is a merge request with its resolved status.
type MergeRequestSummary struct {
	arweave.MergeRequestTx           // the original transaction
	ResolvedStatus         MergeRequestStatus
	Title                  string // first line of message, empty if body not fetched
	ChainHead              string // tx-id of the latest update in the chain (or original if none)
}

// CreateMergeRequestOpts configures a new merge request.
type CreateMergeRequestOpts struct {
	TargetOwner    string
	TargetRepo     string
	TargetRef      string
	SourceOwner    string
	SourceRepo     string
	SourceRef      string
	SourceManifest string
	BaseManifest   string
	Message        string
}

// CreateMergeRequest uploads a merge-request transaction.
func CreateMergeRequest(
	ctx context.Context,
	uploader arweave.Uploader,
	opts *CreateMergeRequestOpts,
) (string, error) {
	body := &manifest.MergeRequestBody{
		Version:        manifest.Version,
		Message:        opts.Message,
		SourceRef:      opts.SourceRef,
		TargetRef:      opts.TargetRef,
		SourceManifest: opts.SourceManifest,
		BaseManifest:   opts.BaseManifest,
	}

	data, err := body.Marshal()
	if err != nil {
		return "", fmt.Errorf("ops: marshal merge request: %w", err)
	}

	tags := manifest.MergeRequestTags(manifest.MergeRequestTagsOpts{
		TargetOwner:  opts.TargetOwner,
		TargetRepo:   opts.TargetRepo,
		SourceOwner:  opts.SourceOwner,
		SourceRepo:   opts.SourceRepo,
		SourceRefSha: manifest.SourceRefSha(opts.SourceRef),
	})

	txID, err := uploader.Upload(ctx, data, tags)
	if err != nil {
		return "", fmt.Errorf("ops: upload merge request: %w", err)
	}

	return txID, nil
}

// PostStatusUpdate uploads a status update (close/reopen/update) to the chain.
type PostStatusUpdateOpts struct {
	// Original MR fields (denormalized for tags).
	TargetOwner  string
	TargetRepo   string
	SourceOwner  string
	SourceRepo   string
	SourceRefSha string
	// Chain.
	ParentTx string // tx-id of the previous transaction in the chain
	// Update content (in body).
	Status         string // "open" or "closed"
	Message        string // optional, only meaningful from MR author
	SourceManifest string // optional, only meaningful from MR author
}

func PostStatusUpdate(
	ctx context.Context,
	uploader arweave.Uploader,
	opts *PostStatusUpdateOpts,
) (string, error) {
	body := &manifest.StatusUpdateBody{
		Version:        manifest.Version,
		Status:         opts.Status,
		Message:        opts.Message,
		SourceManifest: opts.SourceManifest,
	}

	data, err := body.Marshal()
	if err != nil {
		return "", fmt.Errorf("ops: marshal status update: %w", err)
	}

	tags := manifest.UpdateTags(manifest.UpdateTagsOpts{
		TargetOwner:  opts.TargetOwner,
		TargetRepo:   opts.TargetRepo,
		SourceOwner:  opts.SourceOwner,
		SourceRepo:   opts.SourceRepo,
		SourceRefSha: opts.SourceRefSha,
		ParentTx:     opts.ParentTx,
	})

	txID, err := uploader.Upload(ctx, data, tags)
	if err != nil {
		return "", fmt.Errorf("ops: upload status update: %w", err)
	}

	return txID, nil
}

// PostMergeUpdate uploads a merge update (merged: true in body) to the chain.
type PostMergeUpdateOpts struct {
	// Original MR fields (denormalized for tags).
	TargetOwner  string
	TargetRepo   string
	SourceOwner  string
	SourceRepo   string
	SourceRefSha string
	// Chain.
	ParentTx string
	// Content (in body).
	MergeCommit string // optional
}

func PostMergeUpdate(
	ctx context.Context,
	uploader arweave.Uploader,
	opts *PostMergeUpdateOpts,
) (string, error) {
	body := &manifest.MergeUpdateBody{
		Version:     manifest.Version,
		Merged:      true,
		MergeCommit: opts.MergeCommit,
	}

	data, err := body.Marshal()
	if err != nil {
		return "", fmt.Errorf("ops: marshal merge update: %w", err)
	}

	tags := manifest.UpdateTags(manifest.UpdateTagsOpts{
		TargetOwner:  opts.TargetOwner,
		TargetRepo:   opts.TargetRepo,
		SourceOwner:  opts.SourceOwner,
		SourceRepo:   opts.SourceRepo,
		SourceRefSha: opts.SourceRefSha,
		ParentTx:     opts.ParentTx,
	})

	txID, err := uploader.Upload(ctx, data, tags)
	if err != nil {
		return "", fmt.Errorf("ops: upload merge update: %w", err)
	}

	return txID, nil
}

// ListMergeRequests returns merge requests for a repo with resolved statuses.
// direction is "incoming" or "outgoing".
func ListMergeRequests(
	ctx context.Context,
	ar *arweave.Client,
	owner, repoName, direction string,
) ([]MergeRequestSummary, error) {
	var txs []arweave.MergeRequestTx
	var err error

	switch direction {
	case "incoming":
		txs, err = ar.QueryMergeRequests(ctx, owner, repoName)
	case "outgoing":
		txs, err = ar.QueryOutgoingMergeRequests(ctx, owner)
	default:
		return nil, fmt.Errorf("ops: invalid direction %q", direction)
	}
	if err != nil {
		return nil, err
	}

	// Separate originals from updates.
	originals, updates := splitOriginalsAndUpdates(txs)

	// Fetch and parse bodies of all updates to determine status/merged.
	resolved := resolveUpdateBodies(ctx, ar, updates)

	var results []MergeRequestSummary
	for _, orig := range originals {
		// Validate: signer must match Source-Owner.
		if orig.Signer != orig.SourceOwner {
			continue // spoofed
		}

		status, chainHead := resolveStatus(orig, resolved)

		summary := MergeRequestSummary{
			MergeRequestTx: orig,
			ResolvedStatus: status,
			ChainHead:      chainHead,
		}

		// Fetch body for title (best-effort).
		data, fetchErr := ar.Fetch(ctx, orig.TxID)
		if fetchErr == nil {
			if mr, parseErr := manifest.ParseMergeRequestBody(data); parseErr == nil {
				summary.Title = firstLine(mr.Message)
			}
		}

		results = append(results, summary)
	}

	return results, nil
}

// ResolveMergeRequestStatus determines the current status and chain head
// for a single merge request, given all transactions and their resolved bodies.
func ResolveMergeRequestStatus(
	orig arweave.MergeRequestTx,
	resolved []ResolvedUpdate,
) (MergeRequestStatus, string) {
	return resolveStatus(orig, resolved)
}

// LatestSourceManifest walks the resolved update chain and returns the last
// non-empty source_manifest, or "" if none found.
func LatestSourceManifest(orig arweave.MergeRequestTx, resolved []ResolvedUpdate) string {
	// Build parent→children map for valid updates.
	byParent := make(map[string][]ResolvedUpdate)
	for _, u := range resolved {
		if !isValidUpdate(u, orig) {
			continue
		}
		byParent[u.ParentTx] = append(byParent[u.ParentTx], u)
	}

	latest := ""
	current := orig.TxID
	visited := map[string]bool{current: true}
	for {
		children := byParent[current]
		if len(children) == 0 {
			break
		}
		winner := resolveChainFork(children, orig.TargetOwner)
		if visited[winner.TxID] {
			break // cycle detected
		}
		visited[winner.TxID] = true
		if winner.SourceManifest != "" {
			latest = winner.SourceManifest
		}
		current = winner.TxID
	}
	return latest
}

// FetchAndResolveUpdates fetches bodies for update transactions and returns
// ResolvedUpdate entries with status/merged info from the body.
func FetchAndResolveUpdates(ctx context.Context, ar *arweave.Client, txs []arweave.MergeRequestTx) []ResolvedUpdate {
	_, updates := splitOriginalsAndUpdates(txs)
	return resolveUpdateBodies(ctx, ar, updates)
}

// resolveUpdateBodies fetches and parses bodies of update transactions.
func resolveUpdateBodies(ctx context.Context, ar *arweave.Client, updates []arweave.MergeRequestTx) []ResolvedUpdate {
	var resolved []ResolvedUpdate
	for _, u := range updates {
		data, err := ar.Fetch(ctx, u.TxID)
		if err != nil {
			continue // skip unreadable updates
		}
		body, err := manifest.ParseUpdateBody(data)
		if err != nil {
			continue
		}
		resolved = append(resolved, ResolvedUpdate{
			MergeRequestTx: u,
			Merged:         body.Merged,
			Status:         body.Status,
			SourceManifest: body.SourceManifest,
			Message:        body.Message,
		})
	}
	return resolved
}

// resolveStatus implements the status resolution algorithm from the spec.
func resolveStatus(
	orig arweave.MergeRequestTx,
	allResolved []ResolvedUpdate,
) (MergeRequestStatus, string) {
	// Filter to valid updates for this MR (matching tags, authorized signer).
	// sanitizeUpdate clears Merged if signer is not target owner.
	var valid []ResolvedUpdate
	for _, u := range allResolved {
		if !isValidUpdate(u, orig) {
			continue
		}
		if u.Merged && u.Signer != orig.TargetOwner {
			u.Merged = false
		}
		valid = append(valid, u)
	}

	if len(valid) == 0 {
		return MergeRequestOpen, orig.TxID
	}

	// Step 4: Merged short-circuit — any valid merged update from target owner.
	for _, u := range valid {
		if u.Merged && u.Signer == orig.TargetOwner {
			return MergeRequestMerged, u.TxID
		}
	}

	// Step 5-6: Walk the chain from original, resolving forks.
	byParent := make(map[string][]ResolvedUpdate)
	for _, u := range valid {
		byParent[u.ParentTx] = append(byParent[u.ParentTx], u)
	}

	current := orig.TxID
	currentStatus := MergeRequestOpen
	visited := map[string]bool{current: true}

	for {
		children := byParent[current]
		if len(children) == 0 {
			break
		}
		winner := resolveChainFork(children, orig.TargetOwner)
		if visited[winner.TxID] {
			break // cycle detected
		}
		visited[winner.TxID] = true
		current = winner.TxID
		if winner.Status == manifest.StatusClosed {
			currentStatus = MergeRequestClosed
		} else if winner.Status == manifest.StatusOpen {
			currentStatus = MergeRequestOpen
		}
	}

	return currentStatus, current
}

// isValidUpdate checks authorization and tag matching for an update.
func isValidUpdate(update ResolvedUpdate, orig arweave.MergeRequestTx) bool {
	// Must have Parent-Tx.
	if update.ParentTx == "" {
		return false
	}

	// Signer must be target owner or source owner.
	if update.Signer != orig.TargetOwner && update.Signer != orig.SourceOwner {
		return false
	}

	// Denormalized tags must match.
	if update.TargetOwner != orig.TargetOwner ||
		update.TargetRepo != orig.TargetRepo ||
		update.SourceOwner != orig.SourceOwner ||
		update.SourceRepo != orig.SourceRepo ||
		update.SourceRefSha != orig.SourceRefSha {
		return false
	}

	return true
}

// resolveChainFork picks a winner when multiple updates share the same Parent-Tx.
// Target owner wins over source owner; same signer → later timestamp wins.
func resolveChainFork(candidates []ResolvedUpdate, targetOwner string) ResolvedUpdate {
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.Signer == targetOwner && best.Signer != targetOwner {
			best = c
		} else if c.Signer == best.Signer && c.Timestamp > best.Timestamp {
			best = c
		}
	}
	return best
}

// splitOriginalsAndUpdates separates originals (no Parent-Tx) from updates.
func splitOriginalsAndUpdates(txs []arweave.MergeRequestTx) (originals, updates []arweave.MergeRequestTx) {
	for _, tx := range txs {
		if tx.IsOriginal() {
			originals = append(originals, tx)
		} else {
			updates = append(updates, tx)
		}
	}
	return
}

// ResolveTargetFromFork looks up the Forked-From tag on the genesis manifest
// to determine the target owner and repo for a merge request.
// Returns ("", "", nil) if no fork metadata is found.
func ResolveTargetFromFork(
	ctx context.Context,
	ar *arweave.Client,
	genesisTxID string,
) (targetOwner, targetRepo string, err error) {
	if genesisTxID == "" {
		return "", "", nil
	}

	tags, err := ar.QueryTxTags(ctx, genesisTxID)
	if err != nil {
		return "", "", fmt.Errorf("ops: query genesis tags: %w", err)
	}
	if tags == nil {
		return "", "", nil
	}

	forkedFrom := tags[manifest.TagForkedFrom]
	if forkedFrom == "" {
		return "", "", nil
	}

	info, err := ar.QueryTxInfo(ctx, forkedFrom)
	if err != nil {
		return "", "", fmt.Errorf("ops: query source manifest info: %w", err)
	}
	if info == nil {
		return "", "", nil
	}

	return info.Owner, info.Tags[manifest.TagRepoName], nil
}

// ValidateOriginal checks that the MR original is not spoofed
// (signer must match Source-Owner). Returns an error if invalid.
func ValidateOriginal(orig arweave.MergeRequestTx) error {
	if orig.Signer != orig.SourceOwner {
		return fmt.Errorf("merge request %s is invalid: signer %s does not match Source-Owner %s",
			orig.TxID, orig.Signer, orig.SourceOwner)
	}
	return nil
}

// firstLine returns the first line of s (the title in git commit convention).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
