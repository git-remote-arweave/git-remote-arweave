package integration

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivatePushCloneOwner(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// Configure as private and push.
	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "config", "arweave.visibility", "private")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Verify encryption state was created.
	encPath := filepath.Join(dir, ".git", "arweave", "encryption.json")
	if _, err := os.Stat(encPath); err != nil {
		t.Fatalf("encryption.json not created: %v", err)
	}

	// Clone as owner — should succeed.
	cloned := gitClone(t, ownerWallet, ownerAddr, repo)

	original := readFile(t, dir, "README.md")
	got := readFile(t, cloned, "README.md")
	if original != got {
		t.Fatalf("content mismatch:\n  original: %q\n  cloned:   %q", original, got)
	}
}

func TestPrivateCloneNoWallet(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "config", "arweave.visibility", "private")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Clone without wallet — should fail.
	_, err := gitCloneMayFail(t, "", ownerAddr, repo)
	if err == nil {
		t.Fatal("expected clone without wallet to fail for private repo")
	}
}

func TestPrivateCloneWrongWallet(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "config", "arweave.visibility", "private")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Clone with reader wallet (not authorized) — should fail.
	_, err := gitCloneMayFail(t, readerWallet, ownerAddr, repo)
	if err == nil {
		t.Fatal("expected clone with wrong wallet to fail for private repo")
	}
}

func TestPrivateIncrementalFetch(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// Push first commit as private.
	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "config", "arweave.visibility", "private")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Clone as owner.
	cloned := gitClone(t, ownerWallet, ownerAddr, repo)

	// Second commit and push.
	addCommit(t, dir, "secret.txt", "secret data\n", "add secret")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Fetch in clone.
	gitFetch(t, cloned, ownerWallet)
	cloneEnv := gitEnv(ownerWallet)
	run(t, cloned, cloneEnv, "git", "merge", "origin/main")

	got := readFile(t, cloned, "secret.txt")
	if got != "secret data\n" {
		t.Fatalf("expected 'secret data\\n', got %q", got)
	}
}

func TestPublicToPrivate(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// Push as public.
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Switch to private and push again.
	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "config", "arweave.visibility", "private")
	addCommit(t, dir, "private.txt", "private\n", "private commit")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Clone as owner — should get all data.
	cloned := gitClone(t, ownerWallet, ownerAddr, repo)

	got := readFile(t, cloned, "private.txt")
	if got != "private\n" {
		t.Fatalf("expected 'private\\n', got %q", got)
	}
	// Public data should also be there.
	got = readFile(t, cloned, "README.md")
	if got != "# test repo\n" {
		t.Fatalf("README.md mismatch: %q", got)
	}
}

func TestPrivateToPublic(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// Push as private.
	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "config", "arweave.visibility", "private")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Switch to public and push.
	run(t, dir, env, "git", "config", "arweave.visibility", "public")
	addCommit(t, dir, "public.txt", "now public\n", "go public")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Clone without wallet — should succeed (open keymap).
	cloned := gitClone(t, "", ownerAddr, repo)

	got := readFile(t, cloned, "public.txt")
	if got != "now public\n" {
		t.Fatalf("expected 'now public\\n', got %q", got)
	}
	// Historical encrypted data should also be accessible.
	got = readFile(t, cloned, "README.md")
	if got != "# test repo\n" {
		t.Fatalf("README.md mismatch: %q", got)
	}
}
