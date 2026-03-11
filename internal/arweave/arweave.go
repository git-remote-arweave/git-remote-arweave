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

// Gateway returns the L1 gateway URL (used for GraphQL and status checks).
func (c *Client) Gateway() string { return c.gateway }

// FetchGateway returns the data download gateway URL (Turbo CDN or same as Gateway).
func (c *Client) FetchGateway() string { return c.fetchGateway }

// HeadTx checks if transaction data is available at baseURL/{txID} via HEAD.
func (c *Client) HeadTx(ctx context.Context, baseURL, txID string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, baseURL+"/"+txID, nil)
	if err != nil {
		return false
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
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
// Uses the gateway's /{id} endpoint which resolves both L1 transactions
// and bundled data items (ANS-104), unlike goar's tx/{id}/data which
// only works for L1 transactions.
func (c *Client) Fetch(ctx context.Context, txID string) ([]byte, error) {
	return withRetry(ctx, 3, func() ([]byte, error) {
		return c.fetchOnce(ctx, txID)
	})
}

func (c *Client) fetchOnce(ctx context.Context, txID string) ([]byte, error) {
	return c.fetchFrom(ctx, c.fetchGateway, txID)
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

// QueryLatestManifest finds the most recent ref manifest for (owner, repoName).
// Fetches the first page of results (up to 50) and returns the node with the
// highest ISO 8601 Timestamp tag. Single GraphQL request.
// Returns nil, nil if no manifest exists (new repository).
func (c *Client) QueryLatestManifest(ctx context.Context, owner, repoName string) (*ManifestInfo, error) {
	page, err := c.fetchManifestPage(ctx, owner, repoName, "")
	if err != nil {
		return nil, err
	}
	if len(page.nodes) == 0 {
		return nil, nil
	}
	return findByTimestamp(page.nodes), nil
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

// QueryManifestChain returns the ordered manifest chain (head → genesis)
// for the given repository. Finds the head via Timestamp (single page),
// then fetches all pages and walks Parent-Tx links from head to genesis.
func (c *Client) QueryManifestChain(ctx context.Context, owner, repoName string) ([]ManifestInfo, error) {
	all, err := c.fetchAllManifestPages(ctx, owner, repoName)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, nil
	}

	head := findByTimestamp(all)

	// Build chain from head → genesis using Parent-Tx links.
	byID := make(map[string]*gqlNode, len(all))
	for i := range all {
		byID[all[i].id] = &all[i]
	}

	var chain []ManifestInfo
	cur := byID[head.TxID]
	for cur != nil {
		chain = append(chain, ManifestInfo{
			TxID:      cur.id,
			ParentTx:  cur.parentTx,
			IsGenesis: cur.isGenesis,
			KeyMapTx:  cur.keymapTx,
			Encrypted: cur.encrypted,
		})
		if cur.parentTx == "" {
			break
		}
		cur = byID[cur.parentTx]
	}

	return chain, nil
}

// QueryTxExistence checks which of the given tx-ids are indexed in GraphQL.
// Returns a map where present IDs map to true.
func (c *Client) QueryTxExistence(ctx context.Context, txIDs []string) (map[string]bool, error) {
	if len(txIDs) == 0 {
		return map[string]bool{}, nil
	}
	quoted := make([]string, len(txIDs))
	for i, id := range txIDs {
		quoted[i] = fmt.Sprintf("%q", id)
	}
	query := fmt.Sprintf(`{
  transactions(ids: [%s], first: %d) {
    edges { node { id } }
  }
}`, strings.Join(quoted, ", "), len(txIDs))

	body, err := withRetry(ctx, 3, func() ([]byte, error) {
		return c.goarClient.GraphQL(query)
	})
	if err != nil {
		return nil, fmt.Errorf("arweave: graphql query tx existence: %w", err)
	}

	var resp gqlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("arweave: parse tx existence response: %w", err)
	}

	result := make(map[string]bool, len(txIDs))
	for _, edge := range resp.Transactions.Edges {
		result[edge.Node.ID] = true
	}
	return result, nil
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
	timestamp string // ISO 8601 from Timestamp tag; empty if absent
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
				n.timestamp = tag.Value
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

// fetchManifestPage fetches a single page of manifest nodes from GraphQL.
func (c *Client) fetchManifestPage(ctx context.Context, owner, repoName, cursor string) (*manifestPage, error) {
	query := buildManifestPageQuery(owner, repoName, 50, cursor)
	body, err := withRetry(ctx, 3, func() ([]byte, error) {
		return c.goarClient.GraphQL(query)
	})
	if err != nil {
		return nil, fmt.Errorf("arweave: graphql failed: %w", err)
	}
	return parseManifestPage(body)
}

// fetchAllManifestPages fetches all manifest nodes across paginated GraphQL queries.
func (c *Client) fetchAllManifestPages(ctx context.Context, owner, repoName string) ([]gqlNode, error) {
	var all []gqlNode
	var cursor string
	for {
		page, err := c.fetchManifestPage(ctx, owner, repoName, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page.nodes...)
		if !page.hasNextPage || len(page.nodes) == 0 {
			break
		}
		cursor = page.nodes[len(page.nodes)-1].cursor
	}
	return all, nil
}

// findByTimestamp returns the manifest with the highest Timestamp tag value.
// ISO 8601 timestamps are lexicographically sortable.
// Returns the first node if none have a timestamp (all legacy).
func findByTimestamp(nodes []gqlNode) *ManifestInfo {
	best := &nodes[0]
	for i := range nodes {
		if nodes[i].timestamp > best.timestamp {
			best = &nodes[i]
		}
	}
	return &ManifestInfo{
		TxID:      best.id,
		ParentTx:  best.parentTx,
		IsGenesis: best.isGenesis,
		KeyMapTx:  best.keymapTx,
		Encrypted: best.encrypted,
	}
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

// QueryOwnerKey looks up the RSA public key (base64url modulus) for a wallet address
// by fetching any L1 transaction from that address. Returns "", nil if the address
// has no transactions (pubkey not yet revealed on-chain).
func (c *Client) QueryOwnerKey(ctx context.Context, address string) (string, error) {
	query := fmt.Sprintf(`{
  transactions(owners: [%q], first: 1) {
    edges { node { owner { key } } }
  }
}`, address)

	body, err := withRetry(ctx, 3, func() ([]byte, error) {
		return c.goarClient.GraphQL(query)
	})
	if err != nil {
		return "", fmt.Errorf("arweave: graphql owner key lookup: %w", err)
	}

	var resp struct {
		Transactions struct {
			Edges []struct {
				Node struct {
					Owner struct {
						Key string `json:"key"`
					} `json:"owner"`
				} `json:"node"`
			} `json:"edges"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("arweave: parse owner key response: %w", err)
	}
	if len(resp.Transactions.Edges) == 0 {
		return "", nil
	}
	return resp.Transactions.Edges[0].Node.Owner.Key, nil
}

// QueryTxTags fetches the tags for a single transaction by ID.
// Returns nil, nil if the transaction is not found in GraphQL.
func (c *Client) QueryTxTags(ctx context.Context, txID string) (map[string]string, error) {
	query := fmt.Sprintf(`{
  transactions(ids: [%q]) {
    edges { node { id tags { name value } } }
  }
}`, txID)

	body, err := withRetry(ctx, 3, func() ([]byte, error) {
		return c.goarClient.GraphQL(query)
	})
	if err != nil {
		return nil, fmt.Errorf("arweave: graphql tx tags lookup: %w", err)
	}

	var resp gqlResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("arweave: parse tx tags response: %w", err)
	}
	if len(resp.Transactions.Edges) == 0 {
		return nil, nil
	}

	tags := make(map[string]string)
	for _, t := range resp.Transactions.Edges[0].Node.Tags {
		tags[t.Name] = t.Value
	}
	return tags, nil
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
