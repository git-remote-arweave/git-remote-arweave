package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReaderCanClone(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// Configure as private.
	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "config", "arweave.visibility", "private")

	// Add reader with pubkey.
	run(t, dir, env, binaryPath("arweave-git"),
		"readers", "add", readerAddr, "--pubkey", readerPubKey)

	// Push.
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Clone as reader — should succeed.
	cloned := gitClone(t, readerWallet, ownerAddr, repo)
	got := readFile(t, cloned, "README.md")
	if got != "# test repo\n" {
		t.Fatalf("reader clone content mismatch: %q", got)
	}
}

func TestRemovedReaderBlocked(t *testing.T) {
	repo := uniqueRepo(t)
	dir := gitInit(t)

	// Configure as private, add reader.
	env := gitEnv(ownerWallet)
	run(t, dir, env, "git", "config", "arweave.visibility", "private")
	run(t, dir, env, binaryPath("arweave-git"),
		"readers", "add", readerAddr, "--pubkey", readerPubKey)

	// First push — reader has access.
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Verify reader can clone.
	cloned := gitClone(t, readerWallet, ownerAddr, repo)
	_ = readFile(t, cloned, "README.md")

	// Remove reader.
	run(t, dir, env, binaryPath("arweave-git"), "readers", "remove", readerAddr)

	// Push new data — triggers key rotation.
	addCommit(t, dir, "secret2.txt", "post-removal\n", "after reader removed")
	gitPush(t, dir, ownerWallet, ownerAddr, repo)

	// Verify epoch increased.
	encData, err := os.ReadFile(filepath.Join(dir, ".git", "arweave", "encryption.json"))
	if err != nil {
		t.Fatal(err)
	}
	var encState struct {
		CurrentEpoch int `json:"current_epoch"`
	}
	if err := json.Unmarshal(encData, &encState); err != nil {
		t.Fatal(err)
	}
	if encState.CurrentEpoch < 1 {
		t.Fatalf("expected epoch >= 1 after reader removal, got %d", encState.CurrentEpoch)
	}

	// Ex-reader tries to clone — should fail on new epoch data.
	_, err = gitCloneMayFail(t, readerWallet, ownerAddr, repo)
	if err == nil {
		t.Fatal("expected removed reader clone to fail")
	}
}

func TestReaderList(t *testing.T) {
	dir := gitInit(t)
	env := gitEnv(ownerWallet)

	// List initially empty.
	out := run(t, dir, env, binaryPath("arweave-git"), "readers", "list")
	if out != "no readers configured" {
		t.Fatalf("expected empty list, got: %s", out)
	}

	// Add reader.
	run(t, dir, env, binaryPath("arweave-git"),
		"readers", "add", "test-addr-123", "--pubkey", "dGVzdC1wdWJrZXk")
	out = run(t, dir, env, binaryPath("arweave-git"), "readers", "list")
	if out == "no readers configured" {
		t.Fatal("reader not added")
	}

	// Remove reader.
	run(t, dir, env, binaryPath("arweave-git"),
		"readers", "remove", "test-addr-123")
	out = run(t, dir, env, binaryPath("arweave-git"), "readers", "list")
	if out != "no readers configured" {
		t.Fatalf("expected empty after removal, got: %s", out)
	}
}

// binaryPath returns the full path to a built binary.
func binaryPath(name string) string {
	return filepath.Join(binaryDir, name)
}
