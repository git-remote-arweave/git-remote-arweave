package integration

import (
	"testing"
)

func TestPublicPushClone(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// Push to arweave.
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Clone into a new directory.
	cloned := gitClone(t, "", ownerAddr, repo)

	// Verify contents match.
	original := readFile(t, dir, "README.md")
	got := readFile(t, cloned, "README.md")
	if original != got {
		t.Fatalf("content mismatch:\n  original: %q\n  cloned:   %q", original, got)
	}
}

func TestPublicIncrementalFetch(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// First push.
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Clone.
	cloned := gitClone(t, "", ownerAddr, repo)

	// Second commit and push.
	addCommit(t, dir, "file2.txt", "second file\n", "second commit")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Fetch in clone.
	gitFetch(t, cloned, "")
	env := gitEnv("")
	run(t, cloned, env, "git", "merge", "origin/main")

	// Verify new file exists.
	got := readFile(t, cloned, "file2.txt")
	if got != "second file\n" {
		t.Fatalf("expected 'second file\\n', got %q", got)
	}
}

func TestPublicForcePush(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// Push two commits.
	addCommit(t, dir, "file2.txt", "v1\n", "second")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Force push with reset.
	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "reset", "--hard", "HEAD~1")
	run(t, dir, env, "git", "push", "origin", "main", "--force")
	mine(gatewayURL)

	// Clone should have only the initial commit.
	cloned := gitClone(t, "", ownerAddr, repo)
	log := gitLog(t, cloned)
	if lines := countLines(log); lines != 1 {
		t.Fatalf("expected 1 commit after force push, got %d:\n%s", lines, log)
	}
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}
