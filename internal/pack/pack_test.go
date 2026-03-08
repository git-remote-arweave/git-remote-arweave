package pack

import (
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/go-git/go-billy/v5/memfs"
)

var testSig = &object.Signature{
	Name:  "test",
	Email: "test@test.com",
	When:  time.Unix(0, 0),
}

// makeRepo creates an in-memory repo with n commits and returns commit hashes oldest-first.
func makeRepo(t *testing.T, n int) (*git.Repository, []plumbing.Hash) {
	t.Helper()

	repo, err := git.Init(memory.NewStorage(), memfs.New())
	if err != nil {
		t.Fatalf("git.Init: %v", err)
	}

	w, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}

	hashes := make([]plumbing.Hash, 0, n)
	for i := 0; i < n; i++ {
		f, err := w.Filesystem.Create("file.txt")
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		_, _ = f.Write([]byte{byte(i)})
		f.Close()

		if _, err := w.Add("file.txt"); err != nil {
			t.Fatalf("Add: %v", err)
		}
		h, err := w.Commit("commit", &git.CommitOptions{
			Author:    testSig,
			Committer: testSig,
		})
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}
		hashes = append(hashes, h)
	}

	return repo, hashes
}

func TestGenerateAndApply(t *testing.T) {
	src, hashes := makeRepo(t, 3)
	tip := hashes[len(hashes)-1]

	data, err := Generate(src, nil, []plumbing.Hash{tip})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Generate returned empty packfile")
	}

	dst, err := git.Init(memory.NewStorage(), memfs.New())
	if err != nil {
		t.Fatalf("git.Init dst: %v", err)
	}
	if err := Apply(dst, data); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := dst.CommitObject(tip); err != nil {
		t.Errorf("tip commit not found in dst after Apply: %v", err)
	}
}

func TestGenerateDelta(t *testing.T) {
	src, hashes := makeRepo(t, 4)

	bases := []plumbing.Hash{hashes[1]}
	tips := []plumbing.Hash{hashes[3]}

	data, err := Generate(src, bases, tips)
	if err != nil {
		t.Fatalf("Generate delta: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("Generate returned empty packfile")
	}
}

func TestGenerateNoTips(t *testing.T) {
	src, _ := makeRepo(t, 1)
	_, err := Generate(src, nil, nil)
	if err == nil {
		t.Error("Generate with no tips should return error")
	}
}
