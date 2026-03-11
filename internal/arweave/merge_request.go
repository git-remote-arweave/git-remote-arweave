package arweave

import (
	"context"
	"encoding/json"
	"fmt"

	"git-remote-arweave/internal/manifest"
)

// MergeRequestTx represents a single merge-request transaction from GraphQL.
// Both originals and updates use the same Type: merge-request.
type MergeRequestTx struct {
	TxID         string
	Signer       string // owner.address (transaction signer)
	TargetOwner  string
	TargetRepo   string
	SourceOwner  string
	SourceRepo   string
	SourceRefSha string
	ParentTx     string // empty for originals
	Timestamp    string
	Encrypted    bool
}

// IsOriginal reports whether this is the original MR (not an update).
func (tx *MergeRequestTx) IsOriginal() bool {
	return tx.ParentTx == ""
}

// QueryMergeRequests returns all merge-request transactions for a target repo.
// Returns both originals and updates in a single query.
func (c *Client) QueryMergeRequests(ctx context.Context, targetOwner, targetRepo string) ([]MergeRequestTx, error) {
	query := fmt.Sprintf(`{
  transactions(
    tags: [
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
    ]
    first: 100
    sort: HEIGHT_DESC
  ) {
    edges { node { id owner { address } tags { name value } } }
  }
}`,
		manifest.TagAppName, manifest.AppName,
		manifest.TagProtocolVersion, manifest.ProtocolVersion,
		manifest.TagType, manifest.TypeMergeRequest,
		manifest.TagTargetOwner, targetOwner,
		manifest.TagTargetRepo, targetRepo,
	)
	body, err := withRetry(ctx, 3, func() ([]byte, error) {
		return c.goarClient.GraphQL(query)
	})
	if err != nil {
		return nil, fmt.Errorf("arweave: graphql merge requests: %w", err)
	}
	return parseMergeRequestTxs(body)
}

// QueryOutgoingMergeRequests returns all merge-request transactions where
// Source-Owner matches the given address.
func (c *Client) QueryOutgoingMergeRequests(ctx context.Context, sourceOwner string) ([]MergeRequestTx, error) {
	query := fmt.Sprintf(`{
  transactions(
    tags: [
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
    ]
    first: 100
    sort: HEIGHT_DESC
  ) {
    edges { node { id owner { address } tags { name value } } }
  }
}`,
		manifest.TagAppName, manifest.AppName,
		manifest.TagProtocolVersion, manifest.ProtocolVersion,
		manifest.TagType, manifest.TypeMergeRequest,
		manifest.TagSourceOwner, sourceOwner,
	)
	body, err := withRetry(ctx, 3, func() ([]byte, error) {
		return c.goarClient.GraphQL(query)
	})
	if err != nil {
		return nil, fmt.Errorf("arweave: graphql outgoing merge requests: %w", err)
	}
	return parseMergeRequestTxs(body)
}

// QueryMergeRequestsByRef returns all merge-request transactions matching
// source owner/repo and source ref SHA. Used for auto-update on push.
func (c *Client) QueryMergeRequestsByRef(ctx context.Context, sourceOwner, sourceRepo, sourceRefSha string) ([]MergeRequestTx, error) {
	query := fmt.Sprintf(`{
  transactions(
    tags: [
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
      { name: %q, values: [%q] }
    ]
    first: 100
    sort: HEIGHT_DESC
  ) {
    edges { node { id owner { address } tags { name value } } }
  }
}`,
		manifest.TagAppName, manifest.AppName,
		manifest.TagProtocolVersion, manifest.ProtocolVersion,
		manifest.TagType, manifest.TypeMergeRequest,
		manifest.TagSourceOwner, sourceOwner,
		manifest.TagSourceRepo, sourceRepo,
		manifest.TagSourceRefSha, sourceRefSha,
	)
	body, err := withRetry(ctx, 3, func() ([]byte, error) {
		return c.goarClient.GraphQL(query)
	})
	if err != nil {
		return nil, fmt.Errorf("arweave: graphql merge requests by ref: %w", err)
	}
	return parseMergeRequestTxs(body)
}

// mergeRequestGQLResponse is the GraphQL response shape for queries
// that include owner { address }.
type mergeRequestGQLResponse struct {
	Transactions struct {
		Edges []struct {
			Node struct {
				ID    string `json:"id"`
				Owner struct {
					Address string `json:"address"`
				} `json:"owner"`
				Tags []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"tags"`
			} `json:"node"`
		} `json:"edges"`
	} `json:"transactions"`
}

func parseMergeRequestTxs(body []byte) ([]MergeRequestTx, error) {
	var resp mergeRequestGQLResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("arweave: parse merge request response: %w", err)
	}
	var results []MergeRequestTx
	for _, edge := range resp.Transactions.Edges {
		tx := MergeRequestTx{
			TxID:   edge.Node.ID,
			Signer: edge.Node.Owner.Address,
		}
		for _, tag := range edge.Node.Tags {
			switch tag.Name {
			case manifest.TagTargetOwner:
				tx.TargetOwner = tag.Value
			case manifest.TagTargetRepo:
				tx.TargetRepo = tag.Value
			case manifest.TagSourceOwner:
				tx.SourceOwner = tag.Value
			case manifest.TagSourceRepo:
				tx.SourceRepo = tag.Value
			case manifest.TagSourceRefSha:
				tx.SourceRefSha = tag.Value
			case manifest.TagParentTx:
				tx.ParentTx = tag.Value
			case manifest.TagTimestamp:
				tx.Timestamp = tag.Value
			case manifest.TagEncrypted:
				tx.Encrypted = tag.Value == "true"
			}
		}
		results = append(results, tx)
	}
	return results, nil
}
