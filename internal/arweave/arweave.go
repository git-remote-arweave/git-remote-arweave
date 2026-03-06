package arweave

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/permadao/goar"
	"github.com/permadao/goar/schema"

	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/manifest"
)

// Status represents the confirmation state of a submitted transaction.
type Status int

const (
	// StatusPending means the transaction is in the mempool but not yet mined.
	StatusPending Status = iota
	// StatusConfirmed means the transaction has been included in a block.
	StatusConfirmed
	// StatusNotFound means the gateway has no record of the transaction.
	// May indicate a dropped transaction; caller should apply DropTimeout logic.
	StatusNotFound
)

// ManifestInfo is returned by QueryLatestManifest.
type ManifestInfo struct {
	TxID      string
	ParentTx  string // from Parent-Tx tag, used for conflict detection
	IsGenesis bool
}

// Client wraps goar for upload/fetch/query operations.
// wallet may be nil for read-only (fetch/clone) use.
type Client struct {
	gateway    string
	goarClient *goar.Client
	wallet     *goar.Wallet
	http       *http.Client
}

// New creates a Client. If cfg.Wallet is set, loads the wallet for push support.
// If cfg.Wallet is empty, the client is read-only.
func New(cfg *config.Config) (*Client, error) {
	c := &Client{
		gateway:    strings.TrimRight(cfg.Gateway, "/"),
		goarClient: goar.NewClient(cfg.Gateway),
		http:       &http.Client{Timeout: 30 * time.Second},
	}

	if cfg.Wallet != "" {
		w, err := goar.NewWalletFromPath(cfg.Wallet, cfg.Gateway)
		if err != nil {
			return nil, fmt.Errorf("arweave: load wallet %q: %w", cfg.Wallet, err)
		}
		c.wallet = w
	}

	return c, nil
}

// Owner returns the wallet address. Empty string if no wallet is configured.
func (c *Client) Owner() string {
	if c.wallet == nil {
		return ""
	}
	return c.wallet.Owner()
}

// Upload signs and submits a data transaction to Arweave.
// Requires a wallet to be configured.
func (c *Client) Upload(ctx context.Context, data []byte, tags []manifest.Tag) (string, error) {
	if c.wallet == nil {
		return "", fmt.Errorf("arweave: wallet not configured, cannot upload")
	}

	goarTags := make([]schema.Tag, len(tags))
	for i, t := range tags {
		goarTags[i] = schema.Tag{Name: t.Name, Value: t.Value}
	}

	tx, err := c.wallet.SendData(data, goarTags)
	if err != nil {
		return "", fmt.Errorf("arweave: upload failed: %w", err)
	}
	return tx.ID, nil
}

// Fetch downloads raw transaction data by tx-id.
func (c *Client) Fetch(ctx context.Context, txID string) ([]byte, error) {
	// goar prints download progress to stdout via fmt.Printf, which
	// corrupts the git remote helper protocol. Redirect stdout to
	// stderr for the duration of the call.
	orig := os.Stdout
	os.Stdout = os.Stderr
	data, err := c.goarClient.GetTransactionData(txID)
	os.Stdout = orig
	if err != nil {
		return nil, fmt.Errorf("arweave: fetch %q: %w", txID, err)
	}
	return data, nil
}

// TxStatus queries the confirmation status of a transaction.
func (c *Client) TxStatus(ctx context.Context, txID string) (Status, error) {
	status, err := c.goarClient.GetTransactionStatus(txID)
	if err != nil {
		// goar returns an error for 404 — treat as NotFound.
		// Caller applies DropTimeout to distinguish mempool from dropped.
		return StatusNotFound, nil
	}
	if status.NumberOfConfirmations > 0 {
		return StatusConfirmed, nil
	}
	return StatusPending, nil
}

// QueryLatestManifest finds the most recent confirmed ref manifest for (owner, repoName).
// Returns nil, nil if no manifest exists (new repository).
func (c *Client) QueryLatestManifest(ctx context.Context, owner, repoName string) (*ManifestInfo, error) {
	query := buildLatestManifestQuery(owner, repoName)
	body, err := c.goarClient.GraphQL(query)
	if err != nil {
		return nil, fmt.Errorf("arweave: graphql failed: %w", err)
	}
	return parseManifestQueryResult(body)
}

// RepoExists reports whether a repository identified by (owner, repoName) exists on Arweave.
func (c *Client) RepoExists(ctx context.Context, owner, repoName string) (bool, error) {
	query := buildRepoLookupQuery(owner, repoName)
	body, err := c.goarClient.GraphQL(query)
	if err != nil {
		return false, fmt.Errorf("arweave: graphql failed: %w", err)
	}
	id, err := parseFirstTxID(body)
	return id != "", err
}

// --- GraphQL query builders ---

func buildLatestManifestQuery(owner, repoName string) string {
	return fmt.Sprintf(`{
  transactions(
    owners: [%q]
    tags: [
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
    ]
    first: 1
    sort: HEIGHT_DESC
  ) {
    edges { node { id tags { name value } } }
  }
}`,
		owner,
		manifest.TagAppName, manifest.AppName,
		manifest.TagProtocolVersion, manifest.ProtocolVersion,
		manifest.TagType, manifest.TypeRefs,
		manifest.TagRepoName, repoName,
	)
}

func buildRepoLookupQuery(owner, repoName string) string {
	return fmt.Sprintf(`{
  transactions(
    owners: [%q]
    tags: [
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
    ]
    first: 1
  ) {
    edges { node { id } }
  }
}`,
		owner,
		manifest.TagAppName, manifest.AppName,
		manifest.TagProtocolVersion, manifest.ProtocolVersion,
		manifest.TagType, manifest.TypeRefs,
		manifest.TagRepoName, repoName,
		manifest.TagGenesis, "true",
	)
}

// --- GraphQL response parsing ---

type gqlResponse struct {
	Transactions struct {
		Edges []struct {
			Node struct {
				ID   string `json:"id"`
				Tags []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"tags"`
			} `json:"node"`
		} `json:"edges"`
	} `json:"transactions"`
}

func parseManifestQueryResult(body []byte) (*ManifestInfo, error) {
	var resp gqlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("arweave: parse graphql response: %w", err)
	}
	if len(resp.Transactions.Edges) == 0 {
		return nil, nil
	}
	node := resp.Transactions.Edges[0].Node
	info := &ManifestInfo{TxID: node.ID}
	for _, tag := range node.Tags {
		switch tag.Name {
		case manifest.TagParentTx:
			info.ParentTx = tag.Value
		case manifest.TagGenesis:
			info.IsGenesis = tag.Value == "true"
		}
	}
	return info, nil
}

func parseFirstTxID(body []byte) (string, error) {
	var resp gqlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("arweave: parse graphql response: %w", err)
	}
	if len(resp.Transactions.Edges) == 0 {
		return "", nil
	}
	return resp.Transactions.Edges[0].Node.ID, nil
}
