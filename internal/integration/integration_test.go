// Package integration contains end-to-end tests that run against arlocal.
// They exercise the real git-remote-arweave and arweave-git binaries via
// shell commands, testing the full push/clone/fetch cycle.
//
// These tests are skipped unless INTEGRATION=1 is set. They require Node.js
// (for arlocal) and Go (for building binaries).
package integration

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Global test state, initialized in TestMain.
var (
	gatewayURL   string
	binaryDir    string // directory containing built binaries
	ownerWallet  string // path to owner wallet JSON
	readerWallet string // path to reader wallet JSON
	ownerAddr    string
	readerAddr   string
	readerPubKey string // base64url RSA modulus of reader wallet
)

func TestMain(m *testing.M) {
	if os.Getenv("INTEGRATION") == "" {
		fmt.Println("skipping integration tests (set INTEGRATION=1)")
		os.Exit(0)
	}

	tmpDir, err := os.MkdirTemp("", "git-remote-arweave-integration-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	// 1. Build binaries.
	binaryDir = filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(binaryDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create bin dir: %v\n", err)
		os.Exit(1)
	}
	if err := buildBinaries(binaryDir); err != nil {
		fmt.Fprintf(os.Stderr, "build binaries: %v\n", err)
		os.Exit(1)
	}

	// 2. Start arlocal.
	port, err := freePort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "find free port: %v\n", err)
		os.Exit(1)
	}
	gatewayURL = fmt.Sprintf("http://localhost:%d", port)

	arlocal := exec.Command("arlocal", fmt.Sprintf("%d", port))
	arlocalLog, err := os.Create(filepath.Join(tmpDir, "arlocal.log"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "create arlocal log: %v\n", err)
		os.Exit(1)
	}
	arlocal.Stdout = arlocalLog
	arlocal.Stderr = arlocalLog
	if err := arlocal.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start arlocal: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "arlocal started on port %d\n", port)
	defer func() {
		_ = arlocal.Process.Kill()
		_ = arlocal.Wait()
		arlocalLog.Close()
	}()

	if err := waitHealthy(gatewayURL, 30*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "arlocal not healthy: %v\n", err)
		os.Exit(1)
	}

	// 3. Generate wallets and mint tokens.
	walletsDir := filepath.Join(tmpDir, "wallets")
	if err := os.MkdirAll(walletsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create wallets dir: %v\n", err)
		os.Exit(1)
	}

	ownerWallet, ownerAddr, _, err = generateWallet(walletsDir, "owner")
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate owner wallet: %v\n", err)
		os.Exit(1)
	}
	readerWallet, readerAddr, readerPubKey, err = generateWallet(walletsDir, "reader")
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate reader wallet: %v\n", err)
		os.Exit(1)
	}

	if err := mintTokens(gatewayURL, ownerAddr); err != nil {
		fmt.Fprintf(os.Stderr, "mint owner tokens: %v\n", err)
		os.Exit(1)
	}
	if err := mintTokens(gatewayURL, readerAddr); err != nil {
		fmt.Fprintf(os.Stderr, "mint reader tokens: %v\n", err)
		os.Exit(1)
	}
	mine(gatewayURL)

	fmt.Fprintf(os.Stderr, "integration: gateway=%s owner=%s reader=%s\n", gatewayURL, ownerAddr, readerAddr)

	os.Exit(m.Run())
}

// buildBinaries compiles both binaries into the given directory.
func buildBinaries(dir string) error {
	gobin := goPath()
	for _, target := range []struct{ name, pkg string }{
		{"git-remote-arweave", "./cmd/git-remote-arweave/"},
		{"arweave-git", "./cmd/arweave-git/"},
	} {
		out := filepath.Join(dir, target.name)
		cmd := exec.Command(gobin, "build", "-o", out, target.pkg)
		cmd.Dir = projectRoot()
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", target.name, err)
		}
	}
	return nil
}

// goPath returns the Go binary path.
func goPath() string {
	if p := os.Getenv("GOBIN"); p != "" {
		return filepath.Join(p, "go")
	}
	home, _ := os.UserHomeDir()
	// Try the SDK path from MEMORY.md.
	sdk := filepath.Join(home, "sdk", "go1.26.1", "bin", "go")
	if _, err := os.Stat(sdk); err == nil {
		return sdk
	}
	return "go" // fallback to PATH
}

// projectRoot returns the root of the git-remote-arweave project.
func projectRoot() string {
	// We're in internal/integration/, so go up two levels.
	dir, _ := filepath.Abs(filepath.Join("..", ".."))
	return dir
}

// freePort finds an available TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port, nil
}

// waitHealthy polls the gateway until it responds or the timeout expires.
func waitHealthy(gateway string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(gateway + "/info")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("gateway %s not ready after %v", gateway, timeout)
}

// arweaveJWK is the Arweave wallet JSON Web Key format.
type arweaveJWK struct {
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
	D   string `json:"d"`
	P   string `json:"p"`
	Q   string `json:"q"`
	Dp  string `json:"dp"`
	Dq  string `json:"dq"`
	Qi  string `json:"qi"`
}

// generateWallet creates an RSA 4096-bit Arweave JWK wallet file.
// Returns the file path, wallet address, and base64url RSA modulus (pubkey).
func generateWallet(dir, name string) (path, address, pubkey string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return "", "", "", fmt.Errorf("generate RSA key: %w", err)
	}

	nB64 := base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes())

	// Arweave address = base64url(SHA-256(n_bytes)).
	address = arweaveAddress(key.PublicKey.N.Bytes())

	jwk := arweaveJWK{
		Kty: "RSA",
		N:   nB64,
		E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		D:   base64.RawURLEncoding.EncodeToString(key.D.Bytes()),
		P:   base64.RawURLEncoding.EncodeToString(key.Primes[0].Bytes()),
		Q:   base64.RawURLEncoding.EncodeToString(key.Primes[1].Bytes()),
		Dp:  base64.RawURLEncoding.EncodeToString(key.Precomputed.Dp.Bytes()),
		Dq:  base64.RawURLEncoding.EncodeToString(key.Precomputed.Dq.Bytes()),
		Qi:  base64.RawURLEncoding.EncodeToString(key.Precomputed.Qinv.Bytes()),
	}

	data, err := json.Marshal(jwk)
	if err != nil {
		return "", "", "", err
	}

	path = filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", "", err
	}

	return path, address, nB64, nil
}

// arweaveAddress computes base64url(SHA-256(n_bytes)).
func arweaveAddress(nBytes []byte) string {
	h := sha256.Sum256(nBytes)
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// mintTokens mints AR tokens on arlocal for the given address.
func mintTokens(gateway, address string) error {
	resp, err := http.Get(fmt.Sprintf("%s/mint/%s/1000000000000", gateway, address))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// mine triggers block mining on arlocal.
// Multiple blocks are mined to ensure transactions are reported as confirmed
// by arlocal's status endpoint (NumberOfConfirmations > 0).
func mine(gateway string) {
	for range 3 {
		resp, err := http.Get(gateway + "/mine")
		if err == nil {
			resp.Body.Close()
		}
	}
}

// gitEnv returns the environment variables for running git commands with
// the given wallet against arlocal.
func gitEnv(wallet string) []string {
	env := os.Environ()
	env = append(env,
		"ARWEAVE_GATEWAY="+gatewayURL,
		"ARWEAVE_PAYMENT=native",
		"PATH="+binaryDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GIT_TERMINAL_PROMPT=0",
	)
	if wallet != "" {
		env = append(env, "ARWEAVE_WALLET="+wallet)
	}
	return env
}

// run executes a command in the given directory with the given env.
// Returns combined stdout+stderr. Fails the test on error.
func run(t *testing.T, dir string, env []string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %q %v failed in %s:\n%s\nerror: %v", name, args, dir, string(out), err)
	}
	return strings.TrimSpace(string(out))
}

// runMayFail executes a command and returns output + error without failing.
func runMayFail(t *testing.T, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// gitInit creates a new git repo in a temp directory with an initial commit.
func gitInit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	env := gitEnv("")
	run(t, dir, env, "git", "init")
	run(t, dir, env, "git", "config", "user.email", "test@test.com")
	run(t, dir, env, "git", "config", "user.name", "Test")
	writeFile(t, dir, "README.md", "# test repo\n")
	run(t, dir, env, "git", "add", ".")
	run(t, dir, env, "git", "commit", "-m", "initial")
	return dir
}

// writeFile creates or overwrites a file in the given directory.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readFile reads a file from the given directory.
func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// uniqueRepo returns a unique repo name for test isolation.
func uniqueRepo(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("test-%s-%d", strings.ReplaceAll(t.Name(), "/", "-"), time.Now().UnixNano())
}

// gitPush pushes main branch to arweave remote.
func gitPush(t *testing.T, dir, wallet, addr, repo string) {
	t.Helper()
	env := gitEnv(wallet)
	// Add remote only if not already added.
	remotes := run(t, dir, env, "git", "remote")
	if !strings.Contains(remotes, "origin") {
		run(t, dir, env, "git", "remote", "add", "origin",
			fmt.Sprintf("arweave://%s/%s", addr, repo))
	}
	run(t, dir, env, "git", "push", "origin", "main")
	mine(gatewayURL)
}

// gitClone clones a repo from arweave into a temp directory.
// Returns the path to the cloned repo.
func gitClone(t *testing.T, wallet, addr, repo string) string {
	t.Helper()
	dir := t.TempDir()
	env := gitEnv(wallet)
	run(t, dir, env, "git", "clone",
		fmt.Sprintf("arweave://%s/%s", addr, repo), "cloned")
	return filepath.Join(dir, "cloned")
}

// gitCloneMayFail attempts to clone and returns output + error.
func gitCloneMayFail(t *testing.T, wallet, addr, repo string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	env := gitEnv(wallet)
	return runMayFail(t, dir, env, "git", "clone",
		fmt.Sprintf("arweave://%s/%s", addr, repo), "cloned")
}

// gitFetch fetches from origin.
func gitFetch(t *testing.T, dir, wallet string) {
	t.Helper()
	env := gitEnv(wallet)
	run(t, dir, env, "git", "fetch", "origin")
}

// gitLog returns the oneline log for the current branch.
func gitLog(t *testing.T, dir string) string {
	t.Helper()
	env := gitEnv("")
	return run(t, dir, env, "git", "log", "--oneline")
}

// addCommit adds a file and creates a commit.
func addCommit(t *testing.T, dir, filename, content, message string) {
	t.Helper()
	env := gitEnv("")
	writeFile(t, dir, filename, content)
	run(t, dir, env, "git", "add", filename)
	run(t, dir, env, "git", "commit", "-m", message)
}
