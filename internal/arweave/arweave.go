package arweave

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	KeyMapTx  string // from Key-Map tag, non-empty for private repos
	Encrypted bool   // from Encrypted tag, true if manifest body is encrypted
}

// Uploader abstracts data upload to Arweave (L1 or via Turbo bundler).
type Uploader interface {
	Upload(ctx context.Context, data []byte, tags []manifest.Tag) (txID string, err error)
	// Guaranteed reports whether the uploader guarantees delivery.
	// Turbo guarantees settlement; L1 transactions can be dropped from mempool.
	Guaranteed() bool
}

// CostReporter is optionally implemented by uploaders that can report
// upload cost and remaining balance (e.g., TurboUploader).
type CostReporter interface {
	// GetPriceForBytes returns the cost in winc (Winston Credits) for uploading the given number of bytes.
	GetPriceForBytes(ctx context.Context, numBytes int) (winc int64, err error)
	// GetBalance returns the current balance in winc for the given wallet address.
	GetBalance(ctx context.Context, address string) (winc int64, err error)
}

// NewUploader creates the appropriate Uploader based on cfg.Payment.
func NewUploader(cfg *config.Config) (Uploader, error) {
	switch cfg.Payment {
	case config.PaymentTurbo:
		return NewTurboUploader(cfg)
	case config.PaymentNative:
		c, err := New(cfg)
		if err != nil {
			return nil, err
		}
		return c, nil
	default:
		return nil, fmt.Errorf("arweave: unknown payment method %q", cfg.Payment)
	}
}

// Client wraps goar for upload/fetch/query operations.
// wallet may be nil for read-only (fetch/clone) use.
type Client struct {
	gateway      string // GraphQL + TxStatus (arweave.net — indexes ANS-104 data items)
	fetchGateway string // data download (turbo-gateway.com — serves bundled items fast)
	goarClient   *goar.Client
	wallet       *goar.Wallet
	http         *http.Client
}

// New creates a Client. If cfg.Wallet is set, loads the wallet for push support.
// If cfg.Wallet is empty, the client is read-only.
func New(cfg *config.Config) (*Client, error) {
	gw := strings.TrimRight(cfg.Gateway, "/")
	fetchGW := cfg.FetchGateway
	if fetchGW == "" {
		if cfg.Payment == config.PaymentTurbo {
			// turbo-gateway.com serves Turbo-uploaded bundled data items faster
			// than arweave.net, which may return 404 until the bundle is indexed.
			fetchGW = config.DefaultFetchGateway
		} else {
			fetchGW = gw
		}
	}
	fetchGW = strings.TrimRight(fetchGW, "/")

	c := &Client{
		gateway:      gw,
		fetchGateway: fetchGW,
		goarClient:   goar.NewClient(cfg.Gateway),
		http:         &http.Client{Timeout: 30 * time.Second},
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

// NewWithWallet creates a Client with an explicit wallet. Intended for testing.
func NewWithWallet(w *goar.Wallet) *Client {
	return &Client{wallet: w}
}

// isLocal reports whether the gateway points to a loopback address (e.g. arlocal).
func (c *Client) isLocal() bool {
	u, err := url.Parse(c.gateway)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Owner returns the wallet's base64url-encoded public key (the "owner" field
// in Arweave transactions). Empty string if no wallet is configured.
func (c *Client) Owner() string {
	if c.wallet == nil {
		return ""
	}
	return c.wallet.Owner()
}

// Address returns the wallet address (base64url SHA-256 of the public key).
// This is the short identifier used in arweave:// URLs and GraphQL queries.
// Empty string if no wallet is configured.
func (c *Client) Address() string {
	if c.wallet == nil || c.wallet.Signer == nil {
		return ""
	}
	return c.wallet.Signer.Address
}

// RSAPublicKey returns the wallet's RSA public key, or nil if no wallet.
func (c *Client) RSAPublicKey() *rsa.PublicKey {
	if c.wallet == nil || c.wallet.Signer == nil {
		return nil
	}
	return c.wallet.Signer.PubKey
}

// RSAPrivateKey returns the wallet's RSA private key, or nil if no wallet.
func (c *Client) RSAPrivateKey() *rsa.PrivateKey {
	if c.wallet == nil || c.wallet.Signer == nil {
		return nil
	}
	return c.wallet.Signer.PrvKey
}

// Guaranteed implements Uploader. L1 transactions can be dropped from mempool.
func (c *Client) Guaranteed() bool { return false }

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
// Uses the gateway's /{id} endpoint which resolves both L1 transactions
// and bundled data items (ANS-104), unlike goar's tx/{id}/data which
// only works for L1 transactions.
func (c *Client) Fetch(ctx context.Context, txID string) ([]byte, error) {
	return withRetry(ctx, 3, func() ([]byte, error) {
		return c.fetchOnce(ctx, txID)
	})
}

func (c *Client) fetchOnce(ctx context.Context, txID string) ([]byte, error) {
	data, err := c.fetchFrom(ctx, c.fetchGateway, txID)
	if err != nil && c.fetchGateway != c.gateway && isTransientError(err) {
		// Primary fetch gateway (turbo-gateway.com) returned a transient
		// error — fall back to the main gateway (arweave.net).
		if fallbackData, fallbackErr := c.fetchFrom(ctx, c.gateway, txID); fallbackErr == nil {
			return fallbackData, nil
		}
	}
	return data, err
}

func (c *Client) fetchFrom(ctx context.Context, gateway, txID string) ([]byte, error) {
	fetchURL := gateway + "/" + txID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("arweave: fetch %q: %w", txID, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arweave: fetch %q: %w", txID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arweave: fetch %q: %s", txID, resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("arweave: fetch %q: read body: %w", txID, err)
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

// QueryLatestManifest finds the most recent ref manifest for (owner, repoName)
// by walking the Parent-Tx chain. GraphQL's HEIGHT_DESC sort is unreliable for
// ANS-104 data items (Turbo) because block height does not reflect creation order.
// Instead, we fetch manifests in pages, build a parent→child graph from tags,
// and return the chain head (the manifest no other manifest references as parent).
// Returns nil, nil if no manifest exists (new repository).
func (c *Client) QueryLatestManifest(ctx context.Context, owner, repoName string) (*ManifestInfo, error) {
	const pageSize = 50

	// Collect all manifest nodes across pages.
	var all []gqlNode
	var cursor string
	for {
		query := buildManifestPageQuery(owner, repoName, pageSize, cursor)
		body, err := withRetry(ctx, 3, func() ([]byte, error) {
			return c.goarClient.GraphQL(query)
		})
		if err != nil {
			return nil, fmt.Errorf("arweave: graphql failed: %w", err)
		}
		page, err := parseManifestPage(body)
		if err != nil {
			return nil, err
		}
		all = append(all, page.nodes...)
		if !page.hasNextPage || len(page.nodes) == 0 {
			break
		}
		cursor = page.nodes[len(page.nodes)-1].cursor
	}

	if len(all) == 0 {
		return nil, nil
	}

	return findChainHead(all), nil
}

// RepoExists reports whether a repository identified by (owner, repoName) exists on Arweave.
func (c *Client) RepoExists(ctx context.Context, owner, repoName string) (bool, error) {
	query := buildRepoLookupQuery(owner, repoName)
	body, err := withRetry(ctx, 3, func() ([]byte, error) {
		return c.goarClient.GraphQL(query)
	})
	if err != nil {
		return false, fmt.Errorf("arweave: graphql failed: %w", err)
	}
	id, err := parseFirstTxID(body)
	return id != "", err
}

// --- GraphQL query builders ---

func buildManifestPageQuery(owner, repoName string, first int, after string) string {
	afterClause := ""
	if after != "" {
		afterClause = fmt.Sprintf("\n    after: %q", after)
	}
	return fmt.Sprintf(`{
  transactions(
    owners: [%q]
    tags: [
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
    ]
    first: %d%s
    sort: HEIGHT_DESC
  ) {
    pageInfo { hasNextPage }
    edges { cursor node { id tags { name value } } }
  }
}`,
		owner,
		manifest.TagAppName, manifest.AppName,
		manifest.TagProtocolVersion, manifest.ProtocolVersion,
		manifest.TagType, manifest.TypeRefs,
		manifest.TagRepoName, repoName,
		first, afterClause,
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
		PageInfo struct {
			HasNextPage bool `json:"hasNextPage"`
		} `json:"pageInfo"`
		Edges []struct {
			Cursor string `json:"cursor"`
			Node   struct {
				ID   string `json:"id"`
				Tags []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"tags"`
			} `json:"node"`
		} `json:"edges"`
	} `json:"transactions"`
}

// gqlNode is an intermediate representation of a manifest from GraphQL.
type gqlNode struct {
	id        string
	cursor    string
	parentTx  string
	isGenesis bool
	timestamp int64  // unix epoch from Timestamp tag; 0 if absent
	keymapTx  string // from Key-Map tag; non-empty for private repos
	encrypted bool   // from Encrypted tag
}

type manifestPage struct {
	nodes       []gqlNode
	hasNextPage bool
}

func parseManifestPage(body []byte) (*manifestPage, error) {
	var resp gqlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("arweave: parse graphql response: %w", err)
	}
	page := &manifestPage{
		hasNextPage: resp.Transactions.PageInfo.HasNextPage,
	}
	for _, edge := range resp.Transactions.Edges {
		n := gqlNode{
			id:     edge.Node.ID,
			cursor: edge.Cursor,
		}
		for _, tag := range edge.Node.Tags {
			switch tag.Name {
			case manifest.TagParentTx:
				n.parentTx = tag.Value
			case manifest.TagGenesis:
				n.isGenesis = tag.Value == "true"
			case manifest.TagTimestamp:
				n.timestamp, _ = strconv.ParseInt(tag.Value, 10, 64)
			case manifest.TagKeyMap:
				n.keymapTx = tag.Value
			case manifest.TagEncrypted:
				n.encrypted = tag.Value == "true"
			}
		}
		page.nodes = append(page.nodes, n)
	}
	return page, nil
}

// findChainHead finds the manifest that no other manifest references as
// its Parent-Tx. When multiple heads exist (e.g., after force push creates
// a new genesis while old chain still exists), we trace each head back to
// its genesis and pick the head belonging to the genesis with the highest
// Timestamp tag.
func findChainHead(nodes []gqlNode) *ManifestInfo {
	byID := make(map[string]*gqlNode, len(nodes))
	for i := range nodes {
		byID[nodes[i].id] = &nodes[i]
	}

	// Build set of all IDs that are referenced as a parent.
	isParent := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.parentTx != "" {
			isParent[n.parentTx] = true
		}
	}

	// Find heads: nodes whose ID is not in isParent.
	var heads []gqlNode
	for _, n := range nodes {
		if !isParent[n.id] {
			heads = append(heads, n)
		}
	}

	if len(heads) == 0 {
		heads = nodes[:1]
	}

	if len(heads) == 1 {
		h := heads[0]
		return &ManifestInfo{TxID: h.id, ParentTx: h.parentTx, IsGenesis: h.isGenesis, KeyMapTx: h.keymapTx, Encrypted: h.encrypted}
	}

	// Multiple heads — trace each to its genesis and pick the one
	// with the highest Timestamp. Old manifests without Timestamp
	// have timestamp=0, so any tagged genesis wins automatically.
	type candidate struct {
		head    gqlNode
		genesis *gqlNode
	}
	var best *candidate
	for _, h := range heads {
		cur := byID[h.id]
		for cur != nil && !cur.isGenesis {
			cur = byID[cur.parentTx]
		}
		c := &candidate{head: h, genesis: cur}
		if best == nil {
			best = c
		} else if c.genesis != nil && (best.genesis == nil || c.genesis.timestamp > best.genesis.timestamp) {
			best = c
		}
	}

	h := best.head
	return &ManifestInfo{TxID: h.id, ParentTx: h.parentTx, IsGenesis: h.isGenesis, KeyMapTx: h.keymapTx, Encrypted: h.encrypted}
}

// --- Retry logic for transient gateway errors ---

// IsTransient reports whether err indicates a transient gateway problem
// (502, 503, 504) that may resolve on retry.
func IsTransient(err error) bool {
	return isTransientError(err)
}

// isTransientError checks whether an error indicates a transient gateway
// problem (502/503/504, timeouts, connection resets) worth retrying.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "502") ||
		strings.Contains(s, "503") ||
		strings.Contains(s, "504") ||
		strings.Contains(s, "Bad Gateway") ||
		strings.Contains(s, "Service Unavailable") ||
		strings.Contains(s, "Gateway Timeout") ||
		strings.Contains(s, "Client.Timeout") ||
		strings.Contains(s, "context deadline exceeded") ||
		strings.Contains(s, "connection reset") ||
		strings.Contains(s, "connection refused")
}

// withRetry retries fn up to maxRetries times with exponential backoff
// when the returned error is transient. Respects context cancellation.
func withRetry[T any](ctx context.Context, maxRetries int, fn func() (T, error)) (T, error) {
	delay := 1 * time.Second
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isTransientError(err) || attempt == maxRetries {
			return result, err
		}
		fmt.Fprintf(os.Stderr, "arweave: gateway error, retry %d/%d in %v\n",
			attempt+1, maxRetries, delay)
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
	var zero T
	return zero, lastErr
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
