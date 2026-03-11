package helper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/localstate"
	"git-remote-arweave/internal/ops"
)

// Run implements the git remote helper protocol.
// It reads commands from stdin and writes responses to stdout.
// remoteURL is the arweave://<owner>/<repo-name> URL passed by git.
func Run(ctx context.Context, remoteURL string, stdin io.Reader, stdout io.Writer) error {
	owner, repoName, err := ParseURL(remoteURL)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("helper: load config: %w", err)
	}

	ar, err := arweave.New(cfg)
	if err != nil {
		return fmt.Errorf("helper: create arweave client: %w", err)
	}

	gitDir := os.Getenv("GIT_DIR")
	if gitDir == "" {
		gitDir = ".git"
	}

	repo, err := git.PlainOpenWithOptions(gitDir, &git.PlainOpenOptions{EnableDotGitCommonDir: true})
	if err != nil {
		return fmt.Errorf("helper: open git repo at %q: %w", gitDir, err)
	}

	state, err := localstate.NewScoped(gitDir, owner, repoName)
	if err != nil {
		return fmt.Errorf("helper: init local state: %w", err)
	}

	h := &handler{
		ctx:      ctx,
		ar:       ar,
		repo:     repo,
		state:    state,
		cfg:      cfg,
		owner:    owner,
		repoName: repoName,
		out:      stdout,
	}

	return h.loop(stdin)
}

// ParseURL extracts owner and repo-name from arweave://<owner>/<repo-name>.
func ParseURL(rawURL string) (owner, repoName string, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("helper: parse URL %q: %w", rawURL, err)
	}
	if u.Scheme != "arweave" {
		return "", "", fmt.Errorf("helper: unsupported scheme %q, expected arweave://", u.Scheme)
	}
	owner = u.Host
	repoName = strings.TrimPrefix(u.Path, "/")
	if owner == "" || repoName == "" {
		return "", "", fmt.Errorf("helper: invalid URL %q, expected arweave://<owner>/<repo-name>", rawURL)
	}
	return owner, repoName, nil
}

type handler struct {
	ctx      context.Context
	ar       *arweave.Client
	uploader arweave.Uploader
	repo     *git.Repository
	state    *localstate.State
	cfg      *config.Config
	owner    string
	repoName string
	out      io.Writer

	// cached from list, reused by fetch
	remoteState *ops.RemoteState
}

// loop reads and dispatches commands from stdin.
// The git remote helper protocol sends batched commands
// (e.g., multiple "fetch" or "push" lines) terminated by a blank line.
func (h *handler) loop(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "capabilities":
			if err := h.cmdCapabilities(); err != nil {
				return err
			}
		case line == "list" || line == "list for-push":
			if err := h.cmdList(line == "list for-push"); err != nil {
				return err
			}
		case strings.HasPrefix(line, "fetch "):
			if err := h.cmdFetch(scanner); err != nil {
				return err
			}
		case strings.HasPrefix(line, "push "):
			if err := h.cmdPush(line, scanner); err != nil {
				return err
			}
		case line == "":
			// blank line — no-op between command batches
		default:
			return fmt.Errorf("helper: unknown command %q", line)
		}
	}
	return scanner.Err()
}

func (h *handler) cmdCapabilities() error {
	_, err := fmt.Fprint(h.out, "list\nfetch\npush\n\n")
	return err
}

func (h *handler) cmdList(forPush bool) error {
	pending, _, _ := h.state.LoadPending()

	rs, err := ops.LoadRemoteState(h.ctx, h.ar, h.owner, h.repoName)
	if err != nil {
		var mfe *ops.ManifestFetchError
		if errors.As(err, &mfe) {
			if pending != nil && (pending.ManifestTxID == mfe.TxID || pending.ParentTxID == mfe.TxID) {
				// The unfetchable manifest is either our pending push
				// or its parent (both Turbo bundles not settled yet).
				// This is expected — use pending refs, no warning needed.
				fmt.Fprintf(os.Stderr, "arweave: pending manifest %s not yet settled\n", mfe.TxID)
				rs = &ops.RemoteState{}
			} else if arweave.IsTransient(mfe.Err) {
				// Transient gateway error (502/503/504) — abort rather
				// than continuing with empty state which misleads the user.
				return fmt.Errorf("arweave: manifest %s temporarily unavailable (gateway error), try again later", mfe.TxID)
			} else {
				// Genuinely unreadable remote state (e.g. corrupt data, 404).
				fmt.Fprintf(os.Stderr, "arweave: warning: %v\n", err)
				fmt.Fprintf(os.Stderr, "arweave: remote state unreadable; use git push --force to overwrite\n")
				rs = &ops.RemoteState{}
			}
		} else {
			return err
		}
	}
	h.remoteState = rs

	// Save pack entries and manifest tx-id for fork support. When this repo
	// is later pushed to a different wallet, the genesis manifest can reference
	// these packs and include a Forked-From tag.
	// Only save during fetch (plain "list"), not during push ("list for-push"),
	// to avoid overwriting source-packs with current remote's own packs.
	if !forPush {
		if packs := rs.Packs(); len(packs) > 0 {
			_ = h.state.SaveSourcePacks(packs)
			if txID := rs.ManifestTxID(); txID != "" {
				_ = h.state.SaveSourceManifest(txID)
			}
			if kmTx := rs.KeyMapTx(); kmTx != "" {
				_ = h.state.SaveSourceKeymap(kmTx)
			}
		}
	}

	refs := ops.ListRefs(rs, pending)

	for ref, sha := range refs {
		if _, err := fmt.Fprintf(h.out, "%s %s\n", sha, ref); err != nil {
			return err
		}
	}

	// Advertise HEAD symref if refs/heads/main exists.
	if _, ok := refs["refs/heads/main"]; ok {
		if _, err := fmt.Fprintf(h.out, "@refs/heads/main HEAD\n"); err != nil {
			return err
		}
	}

	_, err = fmt.Fprint(h.out, "\n")
	return err
}

// cmdFetch consumes all "fetch <sha> <ref>" lines until a blank line,
// then performs a single Fetch operation (pack-based, not per-ref).
func (h *handler) cmdFetch(scanner *bufio.Scanner) error {
	// Consume remaining fetch lines until blank line.
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		// Additional "fetch <sha> <ref>" lines — we don't need to parse them
		// individually because ops.Fetch downloads all new packs.
	}

	rs := h.remoteState
	if rs == nil {
		var err error
		rs, err = ops.LoadRemoteState(h.ctx, h.ar, h.owner, h.repoName)
		if err != nil {
			return err
		}
	}
	h.remoteState = nil // consumed
	_, err := ops.Fetch(h.ctx, h.ar, h.repo, h.state, rs)
	if err != nil {
		return err
	}
	_, err = fmt.Fprint(h.out, "\n")
	return err
}

// cmdPush consumes all "push <refspec>" lines until a blank line,
// collects ref updates, and performs a single Push operation.
func (h *handler) cmdPush(firstLine string, scanner *bufio.Scanner) error {
	if err := h.cfg.RequireWallet(); err != nil {
		return err
	}

	// Collect all push refspecs.
	lines := []string{firstLine}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		lines = append(lines, line)
	}

	// Parse refspecs and resolve SHAs.
	refUpdates := make(map[string]string)
	var dstOrder []string // preserve order for response
	force := false
	for _, line := range lines {
		// line is "push <refspec>"
		spec := strings.TrimPrefix(line, "push ")
		src, dst, f, err := parseRefSpec(spec)
		if err != nil {
			return err
		}
		if f {
			force = true
		}
		sha, err := h.resolveRef(src)
		if err != nil {
			return err
		}
		refUpdates[dst] = sha
		dstOrder = append(dstOrder, dst)
	}

	// Lazily create the uploader on first push.
	if h.uploader == nil {
		u, err := arweave.NewUploader(h.cfg)
		if err != nil {
			return fmt.Errorf("helper: create uploader: %w", err)
		}
		h.uploader = u
	}

	result, err := ops.Push(h.ctx, h.ar, h.uploader, h.repo, h.state, h.cfg, h.owner, h.repoName, &ops.PushInput{
		RefUpdates:  refUpdates,
		Force:       force,
		ConfirmFunc: ttyConfirm,
		SkipConfirm: os.Getenv("ARWEAVE_CONVERT_TO_PUBLIC") == "yes",
	})
	if err != nil {
		for _, dst := range dstOrder {
			if _, werr := fmt.Fprintf(h.out, "error %s %s\n", dst, err.Error()); werr != nil {
				return werr
			}
		}
		_, _ = fmt.Fprint(h.out, "\n")
		return nil // report error per-ref, don't kill the helper
	}

	if result.PackTxID != "" {
		fmt.Fprintf(os.Stderr, "arweave: pack tx %s\n", result.PackTxID)
	}
	fmt.Fprintf(os.Stderr, "arweave: manifest tx %s\n", result.ManifestTxID)
	h.reportCost(result)
	for _, dst := range dstOrder {
		if _, err := fmt.Fprintf(h.out, "ok %s\n", dst); err != nil {
			return err
		}
	}
	_, err = fmt.Fprint(h.out, "\n")
	return err
}

// resolveRef resolves a local ref name to its SHA.
// An empty src means delete (zero hash).
func (h *handler) resolveRef(src string) (string, error) {
	if src == "" {
		return plumbing.ZeroHash.String(), nil
	}
	ref, err := h.repo.Reference(plumbing.ReferenceName(src), true)
	if err != nil {
		return "", fmt.Errorf("helper: resolve ref %q: %w", src, err)
	}
	return ref.Hash().String(), nil
}

// reportCost prints estimated upload cost and remaining balance to stderr
// if the uploader supports cost reporting (e.g., Turbo).
func (h *handler) reportCost(result *ops.PushResult) {
	cr, ok := h.uploader.(arweave.CostReporter)
	if !ok || result.BytesUploaded == 0 {
		return
	}

	cost, err := cr.GetPriceForBytes(h.ctx, result.BytesUploaded)
	if err == nil && cost > 0 {
		fmt.Fprintf(os.Stderr, "arweave: estimated cost %.6f credits (%d bytes)\n", wincToCredits(cost), result.BytesUploaded)
	}

	balance, err := cr.GetBalance(h.ctx, h.owner)
	if err == nil {
		fmt.Fprintf(os.Stderr, "arweave: balance %.6f credits\n", wincToCredits(balance))
	}
}

// wincToCredits converts Winston Credits to a human-readable credits value.
// 1 credit = 1e12 winc (same as 1 AR = 1e12 winston).
func wincToCredits(winc int64) float64 {
	return float64(winc) / 1e12
}

// ttyConfirm prompts the user for confirmation via /dev/tty (since stdin is
// used by the git remote helper protocol).
func ttyConfirm(prompt string) (string, error) {
	tty, err := os.Open("/dev/tty")
	if err != nil {
		return "", fmt.Errorf("cannot open terminal for confirmation: %w", err)
	}
	defer tty.Close()

	fmt.Fprintf(os.Stderr, "\n%s: ", prompt)
	scanner := bufio.NewScanner(tty)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no input received")
}

// parseRefSpec splits "src:dst" into source and destination refs.
// A leading "+" indicates a force push.
func parseRefSpec(spec string) (src, dst string, force bool, err error) {
	if strings.HasPrefix(spec, "+") {
		force = true
		spec = spec[1:]
	}
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 {
		return "", "", false, fmt.Errorf("helper: invalid refspec %q", spec)
	}
	return parts[0], parts[1], force, nil
}
