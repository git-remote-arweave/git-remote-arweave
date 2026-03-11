package integration

import (
	"fmt"
	"strings"
	"testing"
)

// mrSetup creates a target repo (owner) and a fork (reader), returning
// the directories and repo names. The fork has one extra commit.
func mrSetup(t *testing.T) (targetDir, forkDir, targetRepo, forkRepo string) {
	t.Helper()

	targetRepo = uniqueRepo(t)
	forkRepo = targetRepo + "-fork"

	// Owner creates and pushes the target repo.
	targetDir = gitInit(t)
	gitPush(t, targetDir, ownerWallet, ownerAddr, targetRepo)

	// Reader clones and pushes as a fork.
	forkDir = gitClone(t, readerWallet, ownerAddr, targetRepo)
	env := gitEnv(readerWallet)
	run(t, forkDir, env, "git", "remote", "set-url", "origin",
		fmt.Sprintf("arweave://%s/%s", readerAddr, forkRepo))
	addCommit(t, forkDir, "feature.txt", "new feature\n", "add feature")
	run(t, forkDir, env, "git", "push", "origin", "main")
	mine(gatewayURL)

	return targetDir, forkDir, targetRepo, forkRepo
}

// mrCreate creates a merge request from forkDir and returns the MR tx-id.
func mrCreate(t *testing.T, forkDir, targetRepo, message string) string {
	t.Helper()
	env := gitEnv(readerWallet)
	out := run(t, forkDir, env, binaryPath("arweave-git"),
		"mr", "create",
		"-m", message,
		"--target", ownerAddr+"/"+targetRepo,
		"--source-ref", "main",
		"--target-ref", "main")
	mine(gatewayURL)
	// Output: "merge request created: <tx-id>\n  ..."
	return extractTxID(t, out, "merge request created:")
}

// extractTxID finds a tx-id after a prefix in command output.
func extractTxID(t *testing.T, output, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("could not find %q in output:\n%s", prefix, output)
	return ""
}

// extractField finds a line containing the prefix and returns the trimmed value after it.
func extractField(t *testing.T, output, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("could not find field %q in output:\n%s", prefix, output)
	return ""
}

func TestMRCreateAndList(t *testing.T) {
	targetDir, forkDir, targetRepo, _ := mrSetup(t)

	mrTxID := mrCreate(t, forkDir, targetRepo, "Add feature X")

	// Owner lists incoming MRs from target repo dir.
	env := gitEnv(ownerWallet)
	out := run(t, targetDir, env, binaryPath("arweave-git"), "mr", "list")
	if !strings.Contains(out, mrTxID) {
		t.Fatalf("mr list output should contain tx-id %s:\n%s", mrTxID, out)
	}
	if !strings.Contains(out, "open") {
		t.Fatalf("mr list should show open status:\n%s", out)
	}
	if !strings.Contains(out, "Add feature X") {
		t.Fatalf("mr list should show title:\n%s", out)
	}

	// Reader lists outgoing MRs from fork dir.
	env = gitEnv(readerWallet)
	out = run(t, forkDir, env, binaryPath("arweave-git"), "mr", "list", "--outgoing")
	if !strings.Contains(out, mrTxID) {
		t.Fatalf("outgoing mr list should contain tx-id %s:\n%s", mrTxID, out)
	}
}

func TestMRView(t *testing.T) {
	_, forkDir, targetRepo, _ := mrSetup(t)

	mrTxID := mrCreate(t, forkDir, targetRepo, "View test\n\nDetailed description")

	// View from fork dir (any dir with arweave remote would work, but we need
	// the arweave-git binary to be able to query the gateway).
	env := gitEnv(readerWallet)
	out := run(t, forkDir, env, binaryPath("arweave-git"), "mr", "view", mrTxID)

	for _, want := range []string{"View test", "open", ownerAddr, readerAddr, "main"} {
		if !strings.Contains(out, want) {
			t.Errorf("mr view output should contain %q:\n%s", want, out)
		}
	}
}

func TestMRClose(t *testing.T) {
	targetDir, forkDir, targetRepo, _ := mrSetup(t)

	mrTxID := mrCreate(t, forkDir, targetRepo, "Close test")

	// Owner closes the MR.
	env := gitEnv(ownerWallet)
	out := run(t, targetDir, env, binaryPath("arweave-git"), "mr", "close", mrTxID)
	mine(gatewayURL)
	if !strings.Contains(out, "closed") {
		t.Fatalf("close output should confirm closure:\n%s", out)
	}

	// Verify list shows closed.
	out = run(t, targetDir, env, binaryPath("arweave-git"), "mr", "list")
	if !strings.Contains(out, "closed") {
		t.Fatalf("mr list should show closed status:\n%s", out)
	}
}

func TestMRCloseAndReopen(t *testing.T) {
	targetDir, forkDir, targetRepo, _ := mrSetup(t)

	mrTxID := mrCreate(t, forkDir, targetRepo, "Reopen test")

	// Close.
	env := gitEnv(ownerWallet)
	run(t, targetDir, env, binaryPath("arweave-git"), "mr", "close", mrTxID)
	mine(gatewayURL)

	// Reopen with message (by reader from fork dir).
	env = gitEnv(readerWallet)
	out := run(t, forkDir, env, binaryPath("arweave-git"), "mr", "reopen", mrTxID,
		"-m", "Fixed the issues, rebased on latest main")
	mine(gatewayURL)
	if !strings.Contains(out, "reopened") {
		t.Fatalf("reopen output should confirm reopening:\n%s", out)
	}

	// Remember source manifest before update.
	env = gitEnv(readerWallet)
	viewBefore := run(t, forkDir, env, binaryPath("arweave-git"), "mr", "view", mrTxID)
	manifestBefore := extractField(t, viewBefore, "source manifest:")

	// Push new commit to fork, then update MR.
	addCommit(t, forkDir, "fix.txt", "bugfix\n", "fix issue from review")
	run(t, forkDir, env, "git", "push", "origin", "main")
	mine(gatewayURL)

	// Update with message (by reader from fork dir).
	out = run(t, forkDir, env, binaryPath("arweave-git"), "mr", "update", mrTxID,
		"-m", "Pushed new commits")
	mine(gatewayURL)
	if !strings.Contains(out, "updated") {
		t.Fatalf("update output should confirm update:\n%s", out)
	}

	// Verify source manifest changed after update.
	viewAfter := run(t, forkDir, env, binaryPath("arweave-git"), "mr", "view", mrTxID)
	manifestAfter := extractField(t, viewAfter, "source manifest:")
	if manifestBefore == manifestAfter {
		t.Fatalf("source manifest should change after push+update, got %q both times", manifestBefore)
	}

	// Verify list still shows open.
	env = gitEnv(ownerWallet)
	out = run(t, targetDir, env, binaryPath("arweave-git"), "mr", "list")
	if !strings.Contains(out, "open") {
		t.Fatalf("mr list should show open status after update:\n%s", out)
	}
}

func TestMRMergeWithMessage(t *testing.T) {
	targetDir, forkDir, targetRepo, _ := mrSetup(t)

	mrTxID := mrCreate(t, forkDir, targetRepo, "Merge with message")

	// Owner merges with --no-ff -m (full merge: fetch + merge commit + post status).
	env := gitEnv(ownerWallet)
	out := run(t, targetDir, env, binaryPath("arweave-git"), "mr", "merge", mrTxID,
		"--no-ff", "-m", "Merge feature: accept MR")
	mine(gatewayURL)
	if !strings.Contains(out, "merged") {
		t.Fatalf("merge output should confirm merge:\n%s", out)
	}

	// Verify the merge commit message.
	logOut := run(t, targetDir, env, "git", "log", "--oneline", "-1")
	if !strings.Contains(logOut, "Merge feature") {
		t.Fatalf("merge commit should contain custom message:\n%s", logOut)
	}

	// Verify list shows merged.
	out = run(t, targetDir, env, binaryPath("arweave-git"), "mr", "list")
	if !strings.Contains(out, "merged") {
		t.Fatalf("mr list should show merged status:\n%s", out)
	}
}

func TestMRMergeNoMerge(t *testing.T) {
	targetDir, forkDir, targetRepo, _ := mrSetup(t)

	mrTxID := mrCreate(t, forkDir, targetRepo, "Merge test")

	// Owner merges with --no-merge (just posts the merge update).
	env := gitEnv(ownerWallet)
	out := run(t, targetDir, env, binaryPath("arweave-git"), "mr", "merge", mrTxID, "--no-merge")
	mine(gatewayURL)
	if !strings.Contains(out, "merged") {
		t.Fatalf("merge output should confirm merge:\n%s", out)
	}

	// Verify list shows merged.
	out = run(t, targetDir, env, binaryPath("arweave-git"), "mr", "list")
	if !strings.Contains(out, "merged") {
		t.Fatalf("mr list should show merged status:\n%s", out)
	}
}
