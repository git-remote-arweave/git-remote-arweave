package arweave

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/permadao/goar"
	"github.com/permadao/goar/utils"

	"git-remote-arweave/internal/manifest"
)

func TestAddress(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := goar.NewSignerByPrivateKey(key)
	w := goar.NewWalletWithSigner(signer, "https://arweave.net")

	c := &Client{wallet: w}

	got := c.Address()
	// Verify it matches sha256(N.Bytes()).
	addr := sha256.Sum256(key.PublicKey.N.Bytes())
	want := utils.Base64Encode(addr[:])
	if got != want {
		t.Errorf("Address() = %q, want %q", got, want)
	}
	// Address should differ from Owner (pubkey).
	if got == c.Owner() {
		t.Error("Address() should differ from Owner()")
	}
}

func TestAddress_NilWallet(t *testing.T) {
	c := &Client{}
	if addr := c.Address(); addr != "" {
		t.Errorf("Address() without wallet = %q, want empty", addr)
	}
}

func TestIsLocal(t *testing.T) {
	tests := []struct {
		gateway string
		want    bool
	}{
		{"http://localhost:1984", true},
		{"http://127.0.0.1:1984", true},
		{"http://127.0.0.2:1984", true},
		{"http://[::1]:1984", true},
		{"https://arweave.net", false},
		{"https://notlocalhost.com", false},
		{"https://example.com/path/127.0.0.1", false},
		{"https://ar.io", false},
	}
	for _, tt := range tests {
		c := &Client{gateway: tt.gateway}
		if got := c.isLocal(); got != tt.want {
			t.Errorf("isLocal(%q) = %v, want %v", tt.gateway, got, tt.want)
		}
	}
}

func TestBuildManifestPageQuery(t *testing.T) {
	q := buildManifestPageQuery("owner-addr", "my-repo", 50, "")
	for _, want := range []string{
		"owner-addr",
		manifest.AppName,
		manifest.ProtocolVersion,
		manifest.TypeRefs,
		"my-repo",
		"HEIGHT_DESC",
		"first: 50",
		"pageInfo",
		"hasNextPage",
		"cursor",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q", want)
		}
	}
	// No "after" clause without cursor.
	if strings.Contains(q, "after") {
		t.Error("query should not contain 'after' without cursor")
	}
}

func TestBuildManifestPageQuery_WithCursor(t *testing.T) {
	q := buildManifestPageQuery("owner-addr", "my-repo", 50, "cursor-abc")
	if !strings.Contains(q, `after: "cursor-abc"`) {
		t.Error("query should contain after clause with cursor")
	}
}

func TestBuildRepoLookupQuery(t *testing.T) {
	q := buildRepoLookupQuery("owner-addr", "my-repo")
	for _, want := range []string{
		"owner-addr",
		manifest.AppName,
		manifest.TypeRefs,
		"my-repo",
		manifest.TagGenesis,
		"true",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q", want)
		}
	}
}

func TestParseManifestPage_found(t *testing.T) {
	body := []byte(`{
		"transactions": {
			"pageInfo": {"hasNextPage": false},
			"edges": [{
				"cursor": "c1",
				"node": {
					"id": "tx-abc",
					"tags": [
						{"name": "Parent-Tx", "value": "parent-xyz"},
						{"name": "App-Name", "value": "git-remote-arweave"}
					]
				}
			}]
		}
	}`)

	page, err := parseManifestPage(body)
	if err != nil {
		t.Fatalf("parseManifestPage: %v", err)
	}
	if len(page.nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(page.nodes))
	}
	n := page.nodes[0]
	if n.id != "tx-abc" {
		t.Errorf("id = %q, want tx-abc", n.id)
	}
	if n.parentTx != "parent-xyz" {
		t.Errorf("parentTx = %q, want parent-xyz", n.parentTx)
	}
	if n.isGenesis {
		t.Error("isGenesis should be false")
	}
	if n.cursor != "c1" {
		t.Errorf("cursor = %q, want c1", n.cursor)
	}
	if page.hasNextPage {
		t.Error("hasNextPage should be false")
	}
}

func TestParseManifestPage_timestamp(t *testing.T) {
	body := []byte(`{
		"transactions": {
			"pageInfo": {"hasNextPage": false},
			"edges": [{
				"cursor": "c1",
				"node": {
					"id": "tx-ts",
					"tags": [
						{"name": "Timestamp", "value": "2024-03-08T12:00:00.000Z"},
						{"name": "Genesis", "value": "true"}
					]
				}
			}]
		}
	}`)

	page, err := parseManifestPage(body)
	if err != nil {
		t.Fatalf("parseManifestPage: %v", err)
	}
	n := page.nodes[0]
	if n.timestamp != "2024-03-08T12:00:00.000Z" {
		t.Errorf("timestamp = %q, want 2024-03-08T12:00:00.000Z", n.timestamp)
	}
}

func TestParseManifestPage_genesis(t *testing.T) {
	body := []byte(`{
		"transactions": {
			"pageInfo": {"hasNextPage": false},
			"edges": [{
				"cursor": "c1",
				"node": {
					"id": "genesis-tx",
					"tags": [{"name": "Genesis", "value": "true"}]
				}
			}]
		}
	}`)

	page, err := parseManifestPage(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !page.nodes[0].isGenesis {
		t.Error("isGenesis should be true")
	}
	if page.nodes[0].parentTx != "" {
		t.Errorf("parentTx should be empty for genesis, got %q", page.nodes[0].parentTx)
	}
}

func TestParseManifestPage_empty(t *testing.T) {
	body := []byte(`{"transactions":{"pageInfo":{"hasNextPage":false},"edges":[]}}`)
	page, err := parseManifestPage(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page.nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(page.nodes))
	}
}

// TestFindByTimestamp_SimpleChain tests a linear chain: genesis → A → B.
// B has the highest timestamp.
func TestFindByTimestamp_SimpleChain(t *testing.T) {
	nodes := []gqlNode{
		{id: "B", parentTx: "A", timestamp: "2025-01-03T00:00:00.000Z"},
		{id: "A", parentTx: "genesis", timestamp: "2025-01-02T00:00:00.000Z"},
		{id: "genesis", isGenesis: true, timestamp: "2025-01-01T00:00:00.000Z"},
	}
	info := findByTimestamp(nodes)
	if info.TxID != "B" {
		t.Errorf("expected head B, got %q", info.TxID)
	}
}

// TestFindByTimestamp_SingleGenesis tests a single genesis node.
func TestFindByTimestamp_SingleGenesis(t *testing.T) {
	nodes := []gqlNode{
		{id: "genesis", isGenesis: true, timestamp: "2025-01-01T00:00:00.000Z"},
	}
	info := findByTimestamp(nodes)
	if info.TxID != "genesis" {
		t.Errorf("expected genesis, got %q", info.TxID)
	}
	if !info.IsGenesis {
		t.Error("expected IsGenesis = true")
	}
}

// TestFindByTimestamp_ForcePush tests the force push scenario:
// old chain: genesis-old → A → B, new chain: genesis-new.
// genesis-new has the highest timestamp (most recent push).
func TestFindByTimestamp_ForcePush(t *testing.T) {
	nodes := []gqlNode{
		{id: "B", parentTx: "A"},
		{id: "genesis-new", isGenesis: true, timestamp: "2025-01-02T00:00:00.000Z"},
		{id: "A", parentTx: "genesis-old"},
		{id: "genesis-old", isGenesis: true, timestamp: "2025-01-01T00:00:00.000Z"},
	}
	info := findByTimestamp(nodes)
	if info.TxID != "genesis-new" {
		t.Errorf("expected genesis-new after force push, got %q", info.TxID)
	}
}

// TestFindByTimestamp_ForcePushWithChildren tests force push followed by
// normal pushes. D has the highest timestamp.
func TestFindByTimestamp_ForcePushWithChildren(t *testing.T) {
	nodes := []gqlNode{
		{id: "D", parentTx: "C", timestamp: "2025-01-04T00:00:00.000Z"},
		{id: "C", parentTx: "genesis-new", timestamp: "2025-01-03T00:00:00.000Z"},
		{id: "B", parentTx: "A"},
		{id: "genesis-new", isGenesis: true, timestamp: "2025-01-02T00:00:00.000Z"},
		{id: "A", parentTx: "genesis-old"},
		{id: "genesis-old", isGenesis: true, timestamp: "2025-01-01T00:00:00.000Z"},
	}
	info := findByTimestamp(nodes)
	if info.TxID != "D" {
		t.Errorf("expected D, got %q", info.TxID)
	}
}

// TestFindByTimestamp_HeightMisordered tests the scenario where
// GraphQL returns old manifest at higher block height than newer ones
// due to ANS-104 settlement ordering. Timestamp disambiguates.
func TestFindByTimestamp_HeightMisordered(t *testing.T) {
	nodes := []gqlNode{
		{id: "old-head", parentTx: "old-genesis"},                                          // block 1872021
		{id: "new-child", parentTx: "new-genesis", timestamp: "2025-01-03T00:00:00.000Z"}, // block 1872010
		{id: "new-genesis", isGenesis: true, timestamp: "2025-01-02T00:00:00.000Z"},       // block 1872006
		{id: "old-genesis", isGenesis: true, timestamp: "2025-01-01T00:00:00.000Z"},       // block 1872000
	}
	info := findByTimestamp(nodes)
	if info.TxID != "new-child" {
		t.Errorf("expected new-child (highest timestamp), got %q", info.TxID)
	}
}

// TestFindByTimestamp_NoTimestamps tests fallback when no node has a timestamp.
// Returns the first node in the slice.
func TestFindByTimestamp_NoTimestamps(t *testing.T) {
	nodes := []gqlNode{
		{id: "D", parentTx: "C"},
		{id: "C", parentTx: "B"},
	}
	info := findByTimestamp(nodes)
	if info.TxID != "D" {
		t.Errorf("expected D (first node), got %q", info.TxID)
	}
}

// TestFetchOnce_Success verifies that fetchOnce returns data from the fetch gateway.
func TestFetchOnce_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("pack-data"))
	}))
	defer srv.Close()

	c := &Client{
		gateway:      srv.URL,
		fetchGateway: srv.URL,
		http:         &http.Client{},
	}

	data, err := c.fetchOnce(context.Background(), "test-tx")
	if err != nil {
		t.Fatalf("fetchOnce: %v", err)
	}
	if string(data) != "pack-data" {
		t.Errorf("data = %q, want pack-data", data)
	}
}

// TestFetchOnce_Error verifies that fetchOnce propagates errors without fallback.
func TestFetchOnce_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := &Client{
		gateway:      "http://other-gateway",
		fetchGateway: srv.URL,
		http:         &http.Client{},
	}

	_, err := c.fetchOnce(context.Background(), "test-tx")
	if err == nil {
		t.Fatal("fetchOnce should fail on 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("expected 503 error, got: %v", err)
	}
}

func TestQueryOwnerKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// goar unwraps "data" field, so we must wrap the response.
		_, _ = w.Write([]byte(`{"data":{"transactions":{"edges":[{"node":{"owner":{"key":"RSA-MODULUS-BASE64"}}}]}}}`))
	}))
	defer srv.Close()

	goarC := goar.NewClient(srv.URL)
	c := &Client{
		gateway:    srv.URL,
		goarClient: goarC,
		http:       &http.Client{},
	}

	key, err := c.QueryOwnerKey(context.Background(), "some-address")
	if err != nil {
		t.Fatalf("QueryOwnerKey: %v", err)
	}
	if key != "RSA-MODULUS-BASE64" {
		t.Errorf("key = %q, want RSA-MODULUS-BASE64", key)
	}
}

func TestQueryOwnerKey_NoTransactions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"transactions":{"edges":[]}}}`))
	}))
	defer srv.Close()

	goarC := goar.NewClient(srv.URL)
	c := &Client{
		gateway:    srv.URL,
		goarClient: goarC,
		http:       &http.Client{},
	}

	key, err := c.QueryOwnerKey(context.Background(), "unknown-address")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "" {
		t.Errorf("expected empty key, got %q", key)
	}
}

func TestParseFirstTxID(t *testing.T) {
	body := []byte(`{"transactions":{"edges":[{"node":{"id":"found-id","tags":[]}}]}}`)
	id, err := parseFirstTxID(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "found-id" {
		t.Errorf("id = %q, want found-id", id)
	}
}

func TestParseFirstTxID_empty(t *testing.T) {
	body := []byte(`{"transactions":{"edges":[]}}`)
	id, err := parseFirstTxID(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id, got %q", id)
	}
}
