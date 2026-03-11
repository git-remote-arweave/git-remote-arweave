package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/helper"
	"git-remote-arweave/internal/manifest"
	"git-remote-arweave/internal/ops"
)

func cmdMergeRequest(args []string) {
	if len(args) == 0 {
		mergeRequestUsage()
	}

	switch args[0] {
	case "create":
		cmdMergeRequestCreate(args[1:])
	case "list":
		cmdMergeRequestList(args[1:])
	case "view":
		cmdMergeRequestView(args[1:])
	case "merge":
		cmdMergeRequestMerge(args[1:])
	case "update":
		cmdMergeRequestUpdate(args[1:])
	case "close":
		cmdMergeRequestClose(args[1:])
	case "reopen":
		cmdMergeRequestReopen(args[1:])
	default:
		fatalf("unknown merge-request subcommand: %s", args[0])
	}
}

func cmdMergeRequestCreate(args []string) {
	ctx := context.Background()

	cfg, ar, uploader := loadArweaveClient()

	target := parseFlag(args, "--target")
	targetRef := parseFlag(args, "--target-ref")
	sourceRef := parseFlag(args, "--source-ref")
	message := parseFlag(args, "-m")

	if targetRef == "" {
		targetRef = "main"
	}
	if sourceRef == "" {
		sourceRef = currentBranch()
	}

	// Resolve source owner/repo from remote URL.
	remoteURL := findArweaveRemote()
	sourceOwner, sourceRepo, err := helper.ParseURL(remoteURL)
	if err != nil {
		fatalf("%v", err)
	}

	// Resolve target from Forked-From if not specified.
	var targetOwner, targetRepo string
	if target != "" {
		targetOwner, targetRepo, err = parseOwnerRepo(target)
		if err != nil {
			fatalf("invalid --target %q: %v", target, err)
		}
	} else {
		state, err := openState()
		if err != nil {
			fatalf("%v", err)
		}
		genesisTx, _ := state.LoadGenesisManifest()
		targetOwner, targetRepo, err = ops.ResolveTargetFromFork(ctx, ar, genesisTx)
		if err != nil {
			fatalf("resolve target from fork: %v", err)
		}
		if targetOwner == "" {
			fatalf("--target required (not a fork, no Forked-From metadata)")
		}
		fmt.Fprintf(os.Stderr, "resolved target: %s/%s (from Forked-From)\n", targetOwner, targetRepo)
	}

	// Get message from $EDITOR if -m not provided.
	if message == "" {
		message = editMessage("")
		if message == "" {
			fatalf("empty message, aborting")
		}
	}

	// Get source manifest tx-id.
	sourceManifest, err := ar.QueryLatestManifest(ctx, sourceOwner, sourceRepo)
	if err != nil {
		fatalf("query source manifest: %v", err)
	}
	if sourceManifest == nil {
		fatalf("source repository has no manifests — push first")
	}

	// Get target's current manifest for base_manifest.
	targetManifest, err := ar.QueryLatestManifest(ctx, targetOwner, targetRepo)
	if err != nil {
		fatalf("query target manifest: %v", err)
	}
	baseManifestTx := ""
	if targetManifest != nil {
		baseManifestTx = targetManifest.TxID
	}

	_ = cfg // used for future encryption support

	txID, err := ops.CreateMergeRequest(ctx, uploader, &ops.CreateMergeRequestOpts{
		TargetOwner:    targetOwner,
		TargetRepo:     targetRepo,
		TargetRef:      targetRef,
		SourceOwner:    sourceOwner,
		SourceRepo:     sourceRepo,
		SourceRef:      sourceRef,
		SourceManifest: sourceManifest.TxID,
		BaseManifest:   baseManifestTx,
		Message:        message,
	})
	if err != nil {
		fatalf("%v", err)
	}

	fmt.Printf("merge request created: %s\n", txID)
	fmt.Printf("  %s/%s:%s → %s/%s:%s\n", sourceOwner, sourceRepo, sourceRef, targetOwner, targetRepo, targetRef)
}

func cmdMergeRequestList(args []string) {
	ctx := context.Background()

	_, ar, _ := loadArweaveClient()

	direction := "incoming"
	for _, a := range args {
		if a == "--outgoing" {
			direction = "outgoing"
		}
	}

	remoteURL := findArweaveRemote()
	owner, repoName, err := helper.ParseURL(remoteURL)
	if err != nil {
		fatalf("%v", err)
	}

	results, err := ops.ListMergeRequests(ctx, ar, owner, repoName, direction)
	if err != nil {
		fatalf("list merge requests: %v", err)
	}

	if len(results) == 0 {
		fmt.Printf("no %s merge requests\n", direction)
		return
	}

	fmt.Printf("%-43s  %-8s  %-20s  %s\n", "TX", "STATUS", "FROM", "TITLE")
	fmt.Printf("%-43s  %-8s  %-20s  %s\n", strings.Repeat("─", 43), "────────", strings.Repeat("─", 20), strings.Repeat("─", 30))
	for _, r := range results {
		from := truncate(r.SourceOwner, 8) + "/" + r.SourceRepo
		if len(from) > 20 {
			from = from[:20]
		}
		title := r.Title
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		fmt.Printf("%s  %-8s  %-20s  %s\n", r.TxID, r.ResolvedStatus, from, title)
	}
}

func cmdMergeRequestView(args []string) {
	if len(args) == 0 {
		fatalf("usage: arweave-git mr view <tx-id>")
	}
	ctx := context.Background()

	_, ar, _ := loadArweaveClient()

	txID := args[0]

	// Fetch body.
	data, err := ar.Fetch(ctx, txID)
	if err != nil {
		fatalf("fetch merge request %s: %v", txID, err)
	}
	mr, err := manifest.ParseMergeRequestBody(data)
	if err != nil {
		fatalf("parse merge request: %v", err)
	}

	// Get tags for source/target info.
	tags, err := ar.QueryTxTags(ctx, txID)
	if err != nil {
		fatalf("query merge request tags: %v", err)
	}

	sourceOwner := tags[manifest.TagSourceOwner]
	targetOwner := tags[manifest.TagTargetOwner]
	targetRepo := tags[manifest.TagTargetRepo]

	// Resolve status from all repo transactions.
	allTxs, err := ar.QueryMergeRequests(ctx, targetOwner, targetRepo)
	if err != nil {
		fatalf("query merge requests: %v", err)
	}

	// Find the original in the list.
	var orig arweave.MergeRequestTx
	found := false
	for _, tx := range allTxs {
		if tx.TxID == txID {
			orig = tx
			found = true
			break
		}
	}
	if !found {
		fatalf("merge request %s not found", txID)
	}
	if err := ops.ValidateOriginal(orig); err != nil {
		fatalf("%v", err)
	}
	resolved := ops.FetchAndResolveUpdates(ctx, ar, allTxs)
	status, _ := ops.ResolveMergeRequestStatus(orig, resolved)

	// Find the latest source_manifest from the update chain.
	sourceManifest := mr.SourceManifest
	if latest := ops.LatestSourceManifest(orig, resolved); latest != "" {
		sourceManifest = latest
	}

	title, body := splitMessage(mr.Message)

	fmt.Printf("merge request %s\n\n", txID)
	fmt.Printf("  title:   %s\n", title)
	fmt.Printf("  status:  %s\n", status)
	fmt.Printf("  source:  %s/%s:%s\n", sourceOwner, tags[manifest.TagSourceRepo], mr.SourceRef)
	fmt.Printf("  target:  %s/%s:%s\n", targetOwner, targetRepo, mr.TargetRef)
	fmt.Printf("  source manifest: %s\n", sourceManifest)
	fmt.Printf("  base manifest:   %s\n", mr.BaseManifest)
	if body != "" {
		fmt.Printf("\n%s\n", body)
	}
}

func cmdMergeRequestMerge(args []string) {
	if len(args) == 0 {
		fatalf("usage: arweave-git mr merge <tx-id> [-m \"message\"] [--no-edit] [--no-ff] [--squash] [--no-merge]")
	}
	ctx := context.Background()

	_, ar, uploader := loadArweaveClient()

	// Verify the local wallet is the target repo owner.
	walletAddr := ar.Address()
	if walletAddr == "" {
		fatalf("no wallet configured — only the target repo owner can merge")
	}

	txID := args[0]
	squash := hasFlag(args[1:], "--squash")
	noMerge := hasFlag(args[1:], "--no-merge")
	noEdit := hasFlag(args[1:], "--no-edit")
	noFF := hasFlag(args[1:], "--no-ff")
	mergeMessage := parseFlag(args[1:], "-m")

	// Get original MR info for tags.
	tags, err := ar.QueryTxTags(ctx, txID)
	if err != nil {
		fatalf("query merge request tags: %v", err)
	}
	targetOwner := tags[manifest.TagTargetOwner]
	targetRepo := tags[manifest.TagTargetRepo]
	sourceOwner := tags[manifest.TagSourceOwner]
	sourceRepo := tags[manifest.TagSourceRepo]
	sourceRefSha := tags[manifest.TagSourceRefSha]

	if walletAddr != targetOwner {
		fatalf("only the target repo owner can merge (your wallet: %s, target owner: %s)", walletAddr, targetOwner)
	}

	// Resolve chain head.
	allTxs, err := ar.QueryMergeRequests(ctx, targetOwner, targetRepo)
	if err != nil {
		fatalf("query merge requests: %v", err)
	}
	var orig arweave.MergeRequestTx
	found := false
	for _, tx := range allTxs {
		if tx.TxID == txID {
			orig = tx
			found = true
			break
		}
	}
	if !found {
		fatalf("merge request %s not found", txID)
	}
	if err := ops.ValidateOriginal(orig); err != nil {
		fatalf("%v", err)
	}
	resolved := ops.FetchAndResolveUpdates(ctx, ar, allTxs)
	currentStatus, chainHead := ops.ResolveMergeRequestStatus(orig, resolved)
	if currentStatus == ops.MergeRequestMerged {
		fatalf("merge request is already merged")
	}
	if currentStatus == ops.MergeRequestClosed {
		fatalf("merge request is closed — reopen it first")
	}

	if !noMerge {
		// Fetch body for source ref info.
		data, err := ar.Fetch(ctx, txID)
		if err != nil {
			fatalf("fetch merge request %s: %v", txID, err)
		}
		mr, err := manifest.ParseMergeRequestBody(data)
		if err != nil {
			fatalf("parse merge request: %v", err)
		}

		// Checkout target ref.
		checkoutCmd := exec.CommandContext(ctx, "git", "checkout", mr.TargetRef)
		checkoutCmd.Stdout = os.Stderr
		checkoutCmd.Stderr = os.Stderr
		if err := checkoutCmd.Run(); err != nil {
			fatalf("git checkout %s failed: %v", mr.TargetRef, err)
		}

		// Fetch the fork.
		sourceURL := fmt.Sprintf("arweave://%s/%s", sourceOwner, sourceRepo)
		fmt.Fprintf(os.Stderr, "fetching %s %s...\n", sourceURL, mr.SourceRef)
		fetchCmd := exec.CommandContext(ctx, "git", "fetch", sourceURL, mr.SourceRef)
		fetchCmd.Stdout = os.Stderr
		fetchCmd.Stderr = os.Stderr
		if err := fetchCmd.Run(); err != nil {
			fatalf("git fetch failed: %v", err)
		}

		// Merge.
		var mergeArgs []string
		if squash {
			fmt.Fprintf(os.Stderr, "squash merging FETCH_HEAD into %s...\n", mr.TargetRef)
			mergeArgs = []string{"merge", "--squash", "FETCH_HEAD"}
		} else {
			fmt.Fprintf(os.Stderr, "merging FETCH_HEAD into %s...\n", mr.TargetRef)
			mergeArgs = []string{"merge", "FETCH_HEAD"}
			if noFF {
				mergeArgs = append(mergeArgs, "--no-ff")
			}
			if mergeMessage != "" {
				mergeArgs = append(mergeArgs, "-m", mergeMessage)
			} else if noEdit {
				mergeArgs = append(mergeArgs, "--no-edit")
			}
		}
		mergeCmd := exec.CommandContext(ctx, "git", mergeArgs...)
		mergeCmd.Stdin = os.Stdin
		mergeCmd.Stdout = os.Stderr
		mergeCmd.Stderr = os.Stderr
		if err := mergeCmd.Run(); err != nil {
			fatalf("git merge failed (resolve conflicts manually, then run: arweave-git mr merge %s --no-merge): %v", txID, err)
		}

		// For squash, commit.
		if squash {
			var commitArgs []string
			if mergeMessage != "" {
				commitArgs = []string{"commit", "-m", mergeMessage}
			} else if noEdit {
				commitArgs = []string{"commit", "--no-edit"}
			} else {
				commitArgs = []string{"commit"}
			}
			commitCmd := exec.CommandContext(ctx, "git", commitArgs...)
			commitCmd.Stdin = os.Stdin
			commitCmd.Stdout = os.Stderr
			commitCmd.Stderr = os.Stderr
			if err := commitCmd.Run(); err != nil {
				fatalf("git commit failed: %v", err)
			}
		}

		// Push.
		remoteURL := findArweaveRemote()
		remoteName := findRemoteName(remoteURL)
		fmt.Fprintf(os.Stderr, "pushing %s to %s...\n", mr.TargetRef, remoteName)
		pushCmd := exec.CommandContext(ctx, "git", "push", remoteName, mr.TargetRef)
		pushCmd.Stdout = os.Stderr
		pushCmd.Stderr = os.Stderr
		if err := pushCmd.Run(); err != nil {
			fatalf("git push failed: %v", err)
		}
	}

	// Post merge update.
	mergeCommit := ""
	if !noMerge {
		out, err := exec.Command("git", "rev-parse", "HEAD").Output()
		if err == nil {
			mergeCommit = strings.TrimSpace(string(out))
		}
	}

	updateTxID, err := ops.PostMergeUpdate(ctx, uploader, &ops.PostMergeUpdateOpts{
		TargetOwner:  targetOwner,
		TargetRepo:   targetRepo,
		SourceOwner:  sourceOwner,
		SourceRepo:   sourceRepo,
		SourceRefSha: sourceRefSha,
		ParentTx:     chainHead,
		MergeCommit:  mergeCommit,
	})
	if err != nil {
		fatalf("post merge status: %v", err)
	}

	fmt.Printf("merge request %s marked as merged (update tx: %s)\n", txID, updateTxID)
}

func cmdMergeRequestUpdate(args []string) {
	if len(args) == 0 {
		fatalf("usage: arweave-git mr update <tx-id> [-m \"message\"]")
	}
	ctx := context.Background()

	_, ar, uploader := loadArweaveClient()

	txID := args[0]
	message := parseFlag(args[1:], "-m")
	chainHead, orig, resolved := resolveChainHeadForUpdate(ctx, ar, txID)

	status, _ := ops.ResolveMergeRequestStatus(*orig, resolved)
	if status == ops.MergeRequestMerged {
		fatalf("cannot update a merged merge request")
	}
	if status == ops.MergeRequestClosed {
		fatalf("merge request is closed — reopen it first")
	}

	// Get current source manifest.
	remoteURL := findArweaveRemote()
	sourceOwner, sourceRepo, err := helper.ParseURL(remoteURL)
	if err != nil {
		fatalf("%v", err)
	}
	sourceManifest, err := ar.QueryLatestManifest(ctx, sourceOwner, sourceRepo)
	if err != nil {
		fatalf("query source manifest: %v", err)
	}
	sourceManifestTx := ""
	if sourceManifest != nil {
		sourceManifestTx = sourceManifest.TxID
	}

	updateTxID, err := ops.PostStatusUpdate(ctx, uploader, &ops.PostStatusUpdateOpts{
		TargetOwner:    orig.TargetOwner,
		TargetRepo:     orig.TargetRepo,
		SourceOwner:    orig.SourceOwner,
		SourceRepo:     orig.SourceRepo,
		SourceRefSha:   orig.SourceRefSha,
		ParentTx:       chainHead,
		Status:         manifest.StatusOpen,
		Message:        message,
		SourceManifest: sourceManifestTx,
	})
	if err != nil {
		fatalf("post update: %v", err)
	}

	fmt.Printf("merge request %s updated (update tx: %s)\n", txID, updateTxID)
}

func cmdMergeRequestClose(args []string) {
	if len(args) == 0 {
		fatalf("usage: arweave-git mr close <tx-id>")
	}
	ctx := context.Background()

	_, ar, uploader := loadArweaveClient()

	txID := args[0]
	chainHead, orig, resolved := resolveChainHeadForUpdate(ctx, ar, txID)

	status, _ := ops.ResolveMergeRequestStatus(*orig, resolved)
	if status == ops.MergeRequestMerged {
		fatalf("cannot close a merged merge request")
	}
	if status == ops.MergeRequestClosed {
		fatalf("merge request is already closed")
	}

	updateTxID, err := ops.PostStatusUpdate(ctx, uploader, &ops.PostStatusUpdateOpts{
		TargetOwner:  orig.TargetOwner,
		TargetRepo:   orig.TargetRepo,
		SourceOwner:  orig.SourceOwner,
		SourceRepo:   orig.SourceRepo,
		SourceRefSha: orig.SourceRefSha,
		ParentTx:     chainHead,
		Status:       manifest.StatusClosed,
	})
	if err != nil {
		fatalf("post close status: %v", err)
	}

	fmt.Printf("merge request %s closed (update tx: %s)\n", txID, updateTxID)
}

func cmdMergeRequestReopen(args []string) {
	if len(args) == 0 {
		fatalf("usage: arweave-git mr reopen <tx-id> [-m \"message\"]")
	}
	ctx := context.Background()

	_, ar, uploader := loadArweaveClient()

	txID := args[0]
	message := parseFlag(args[1:], "-m")
	chainHead, orig, resolved := resolveChainHeadForUpdate(ctx, ar, txID)

	// Verify current status is closed (not merged).
	status, _ := ops.ResolveMergeRequestStatus(*orig, resolved)
	if status == ops.MergeRequestMerged {
		fatalf("cannot reopen a merged merge request")
	}
	if status == ops.MergeRequestOpen {
		fatalf("merge request is already open")
	}

	// Get current source manifest.
	remoteURL := findArweaveRemote()
	sourceOwner, sourceRepo, err := helper.ParseURL(remoteURL)
	if err != nil {
		fatalf("%v", err)
	}
	sourceManifest, err := ar.QueryLatestManifest(ctx, sourceOwner, sourceRepo)
	if err != nil {
		fatalf("query source manifest: %v", err)
	}
	sourceManifestTx := ""
	if sourceManifest != nil {
		sourceManifestTx = sourceManifest.TxID
	}

	updateTxID, err := ops.PostStatusUpdate(ctx, uploader, &ops.PostStatusUpdateOpts{
		TargetOwner:    orig.TargetOwner,
		TargetRepo:     orig.TargetRepo,
		SourceOwner:    orig.SourceOwner,
		SourceRepo:     orig.SourceRepo,
		SourceRefSha:   orig.SourceRefSha,
		ParentTx:       chainHead,
		Status:         manifest.StatusOpen,
		Message:        message,
		SourceManifest: sourceManifestTx,
	})
	if err != nil {
		fatalf("post reopen status: %v", err)
	}

	fmt.Printf("merge request %s reopened (update tx: %s)\n", txID, updateTxID)
}

// resolveChainHeadForUpdate queries the MR's repo, finds the original, and
// resolves the chain head for posting an update.
func resolveChainHeadForUpdate(ctx context.Context, ar *arweave.Client, mrTxID string) (string, *arweave.MergeRequestTx, []ops.ResolvedUpdate) {
	tags, err := ar.QueryTxTags(ctx, mrTxID)
	if err != nil {
		fatalf("query merge request tags: %v", err)
	}

	targetOwner := tags[manifest.TagTargetOwner]
	targetRepo := tags[manifest.TagTargetRepo]

	allTxs, err := ar.QueryMergeRequests(ctx, targetOwner, targetRepo)
	if err != nil {
		fatalf("query merge requests: %v", err)
	}

	var orig *arweave.MergeRequestTx
	for i, tx := range allTxs {
		if tx.TxID == mrTxID {
			orig = &allTxs[i]
			break
		}
	}
	if orig == nil {
		fatalf("merge request %s not found", mrTxID)
	}
	if err := ops.ValidateOriginal(*orig); err != nil {
		fatalf("%v", err)
	}

	resolved := ops.FetchAndResolveUpdates(ctx, ar, allTxs)
	_, chainHead := ops.ResolveMergeRequestStatus(*orig, resolved)
	return chainHead, orig, resolved
}

// --- helpers ---

// loadArweaveClient creates a config, arweave client, and uploader.
func loadArweaveClient() (*config.Config, *arweave.Client, arweave.Uploader) {
	cfg, err := config.Load()
	if err != nil {
		fatalf("load config: %v", err)
	}
	ar, err := arweave.New(cfg)
	if err != nil {
		fatalf("create client: %v", err)
	}
	uploader, err := arweave.NewUploader(cfg)
	if err != nil {
		fatalf("create uploader: %v", err)
	}
	return cfg, ar, uploader
}

// parseOwnerRepo splits "owner/repo" into components.
func parseOwnerRepo(s string) (owner, repo string, err error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected owner/repo format")
	}
	return parts[0], parts[1], nil
}

// currentBranch returns the current git branch name.
func currentBranch() string {
	out, err := exec.Command("git", "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		fatalf("cannot determine current branch: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// findRemoteName returns the git remote name for the given URL.
func findRemoteName(targetURL string) string {
	out, err := exec.Command("git", "remote", "-v").Output()
	if err != nil {
		return "origin"
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == targetURL {
			return fields[0]
		}
	}
	return "origin"
}

// hasFlag checks if a flag is present in args.
func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// editMessage opens $EDITOR for the user to write a message.
func editMessage(initial string) string {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	tmpFile, err := os.CreateTemp("", "arweave-git-mr-*.txt")
	if err != nil {
		fatalf("create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if initial != "" {
		if _, err := tmpFile.WriteString(initial); err != nil {
			fatalf("write temp file: %v", err)
		}
	}
	tmpFile.Close()

	cmd := exec.Command(editor, tmpFile.Name())
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fatalf("editor failed: %v", err)
	}

	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		fatalf("read temp file: %v", err)
	}

	return strings.TrimSpace(string(data))
}

// splitMessage splits a message into title (first line) and body (rest).
func splitMessage(msg string) (title, body string) {
	parts := strings.SplitN(msg, "\n", 2)
	title = strings.TrimSpace(parts[0])
	if len(parts) > 1 {
		body = strings.TrimSpace(parts[1])
	}
	return title, body
}

func mergeRequestUsage() {
	fmt.Fprintln(os.Stderr, "usage: arweave-git merge-request <command> [args]")
	fmt.Fprintln(os.Stderr, "       arweave-git mr <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  create [-m \"message\"] [--target owner/repo] [--target-ref ref] [--source-ref ref]")
	fmt.Fprintln(os.Stderr, "  list [--outgoing]          list incoming (default) or outgoing merge requests")
	fmt.Fprintln(os.Stderr, "  view <tx-id>               show merge request details")
	fmt.Fprintln(os.Stderr, "  merge <tx-id> [-m \"msg\"] [--no-edit] [--no-ff] [--squash] [--no-merge]")
	fmt.Fprintln(os.Stderr, "  update <tx-id> [-m \"msg\"]  update an open merge request")
	fmt.Fprintln(os.Stderr, "  close <tx-id>              close without merging")
	fmt.Fprintln(os.Stderr, "  reopen <tx-id> [-m \"msg\"]  reopen a closed merge request")
	os.Exit(1)
}
