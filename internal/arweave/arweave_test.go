package arweave

import (
	"strings"
	"testing"

	"git-remote-arweave/internal/manifest"
)

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

// TestFindChainHead_SimpleChain tests a linear chain: genesis → A → B.
// B is the head (not referenced as parent by anyone).
func TestFindChainHead_SimpleChain(t *testing.T) {
	nodes := []gqlNode{
		{id: "B", parentTx: "A"},            // head
		{id: "A", parentTx: "genesis"},
		{id: "genesis", isGenesis: true},
	}
	info := findChainHead(nodes)
	if info.TxID != "B" {
		t.Errorf("expected head B, got %q", info.TxID)
	}
}

// TestFindChainHead_SingleGenesis tests a single genesis node.
func TestFindChainHead_SingleGenesis(t *testing.T) {
	nodes := []gqlNode{
		{id: "genesis", isGenesis: true},
	}
	info := findChainHead(nodes)
	if info.TxID != "genesis" {
		t.Errorf("expected genesis, got %q", info.TxID)
	}
	if !info.IsGenesis {
		t.Error("expected IsGenesis = true")
	}
}

// TestFindChainHead_ForcePush tests the force push scenario:
// old chain: genesis-old → A → B (B is old head)
// new chain: genesis-new (force push, higher in HEIGHT_DESC)
// genesis-new should be selected because it's a genesis head.
func TestFindChainHead_ForcePush(t *testing.T) {
	nodes := []gqlNode{
		// HEIGHT_DESC order — new genesis may appear anywhere
		{id: "B", parentTx: "A"},                // old chain head
		{id: "genesis-new", isGenesis: true},      // force push genesis
		{id: "A", parentTx: "genesis-old"},
		{id: "genesis-old", isGenesis: true},
	}
	info := findChainHead(nodes)
	// Both B and genesis-new are heads (unreferenced as parent).
	// genesis-new should win because it's a genesis.
	if info.TxID != "genesis-new" {
		t.Errorf("expected genesis-new after force push, got %q", info.TxID)
	}
}

// TestFindChainHead_ForcePushWithChildren tests force push followed by
// normal pushes: genesis-new → C → D. Old chain also exists.
func TestFindChainHead_ForcePushWithChildren(t *testing.T) {
	nodes := []gqlNode{
		{id: "D", parentTx: "C"},                 // new chain head
		{id: "C", parentTx: "genesis-new"},
		{id: "B", parentTx: "A"},                  // old chain head
		{id: "genesis-new", isGenesis: true},
		{id: "A", parentTx: "genesis-old"},
		{id: "genesis-old", isGenesis: true},
	}
	info := findChainHead(nodes)
	// D and B are both heads. Neither is genesis.
	// D appears first in HEIGHT_DESC → selected.
	if info.TxID != "D" {
		t.Errorf("expected D, got %q", info.TxID)
	}
}

// TestFindChainHead_HeightMisordered tests the actual bug scenario:
// GraphQL returns old manifest at higher block height than newer ones
// due to ANS-104 settlement ordering.
func TestFindChainHead_HeightMisordered(t *testing.T) {
	// HEIGHT_DESC puts old-chain-head first (higher block),
	// but the chain walk should find the correct head by tracing
	// to the newest genesis.
	nodes := []gqlNode{
		{id: "old-head", parentTx: "old-genesis"},   // block 1872021
		{id: "new-child", parentTx: "new-genesis"},  // block 1872010
		{id: "new-genesis", isGenesis: true},         // block 1872006
		{id: "old-genesis", isGenesis: true},         // block 1872000
	}
	info := findChainHead(nodes)
	// Both old-head and new-child are heads.
	// new-genesis appears first among genesis nodes → it's the "newest".
	// new-child traces to new-genesis → it should be selected.
	if info.TxID != "new-child" {
		t.Errorf("expected new-child (traces to newest genesis), got %q", info.TxID)
	}
}

// TestFindChainHead_GenesisOutsideWindow tests the case where the genesis
// is not in the fetched page (chain too long). Falls back to first head.
func TestFindChainHead_GenesisOutsideWindow(t *testing.T) {
	nodes := []gqlNode{
		{id: "D", parentTx: "C"},
		{id: "C", parentTx: "B"},       // B is outside window
	}
	info := findChainHead(nodes)
	// D is the only head (C is referenced as D's parent).
	if info.TxID != "D" {
		t.Errorf("expected D, got %q", info.TxID)
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
