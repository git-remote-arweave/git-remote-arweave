package arweave

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/permadao/goar"
	"github.com/permadao/goar/schema"

	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/manifest"
)

// TurboUploader uploads data items to Arweave via the ArDrive Turbo bundler.
// Data items are signed with the AR JWK wallet (preserving repository identity)
// and submitted as ANS-104 binary to the Turbo upload endpoint.
const defaultPaymentGateway = "https://payment.ardrive.io"

type TurboUploader struct {
	bundler        *goar.Bundler
	gateway        string
	paymentGateway string
	http           *http.Client
}

// NewTurboUploader creates a TurboUploader from the given config.
// Requires a wallet to be configured.
func NewTurboUploader(cfg *config.Config) (*TurboUploader, error) {
	if cfg.Wallet == "" {
		return nil, fmt.Errorf("arweave: wallet not configured, cannot create turbo uploader")
	}

	signer, err := goar.NewSignerFromPath(cfg.Wallet)
	if err != nil {
		return nil, fmt.Errorf("arweave: load signer from %q: %w", cfg.Wallet, err)
	}

	bundler, err := goar.NewBundler(signer)
	if err != nil {
		return nil, fmt.Errorf("arweave: create bundler: %w", err)
	}

	return &TurboUploader{
		bundler:        bundler,
		gateway:        strings.TrimRight(cfg.TurboGateway, "/"),
		paymentGateway: defaultPaymentGateway,
		http:           &http.Client{Timeout: 120 * time.Second},
	}, nil
}

// Guaranteed implements Uploader. Turbo guarantees settlement to Arweave.
func (t *TurboUploader) Guaranteed() bool { return true }

// Upload creates a signed ANS-104 data item and submits it to Turbo.
// Returns the data item ID (base64url SHA-256 of the signature).
func (t *TurboUploader) Upload(ctx context.Context, data []byte, tags []manifest.Tag) (string, error) {
	goarTags := make([]schema.Tag, len(tags))
	for i, tg := range tags {
		goarTags[i] = schema.Tag{Name: tg.Name, Value: tg.Value}
	}

	item, err := t.bundler.CreateAndSignItem(data, "", "", goarTags)
	if err != nil {
		return "", fmt.Errorf("turbo: sign data item: %w", err)
	}

	endpoint := t.gateway + "/v1/tx"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(item.Binary))
	if err != nil {
		return "", fmt.Errorf("turbo: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := t.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("turbo: upload failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("turbo: upload returned %d: %s", resp.StatusCode, string(body))
	}

	// Turbo returns a JSON response with the data item ID.
	// Fall back to the locally computed ID if parsing fails.
	var turboResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &turboResp); err == nil && turboResp.ID != "" {
		return turboResp.ID, nil
	}

	return item.Id, nil
}

// GetPriceForBytes implements CostReporter.
func (t *TurboUploader) GetPriceForBytes(ctx context.Context, numBytes int) (int64, error) {
	url := fmt.Sprintf("%s/v1/price/bytes/%d", t.paymentGateway, numBytes)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("turbo: price query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("turbo: price query returned %d", resp.StatusCode)
	}
	var result struct {
		Winc string `json:"winc"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("turbo: parse price response: %w", err)
	}
	return parseWinc(result.Winc)
}

// GetBalance implements CostReporter.
func (t *TurboUploader) GetBalance(ctx context.Context, address string) (int64, error) {
	url := fmt.Sprintf("%s/v1/account/balance/arweave?address=%s", t.paymentGateway, address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := t.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("turbo: balance query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, nil // no account = zero balance
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("turbo: balance query returned %d", resp.StatusCode)
	}
	var result struct {
		Winc string `json:"winc"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, fmt.Errorf("turbo: parse balance response: %w", err)
	}
	return parseWinc(result.Winc)
}

func parseWinc(s string) (int64, error) {
	var v int64
	_, err := fmt.Sscanf(s, "%d", &v)
	return v, err
}
