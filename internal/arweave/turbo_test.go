package arweave

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/permadao/goar"

	"git-remote-arweave/internal/manifest"
)

func TestTurboUploader_Guaranteed(t *testing.T) {
	tu := &TurboUploader{}
	if !tu.Guaranteed() {
		t.Error("TurboUploader.Guaranteed() should be true")
	}
}

func TestClientUploader_NotGuaranteed(t *testing.T) {
	c := &Client{}
	if c.Guaranteed() {
		t.Error("Client.Guaranteed() should be false")
	}
}

func TestTurboUploader_Upload(t *testing.T) {
	var receivedBody []byte
	var receivedContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/tx" {
			t.Errorf("expected /v1/tx, got %s", r.URL.Path)
		}
		receivedContentType = r.Header.Get("Content-Type")
		receivedBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"id": "turbo-item-id"})
	}))
	defer server.Close()

	tu := newTestTurboUploader(t, server.URL)

	tags := []manifest.Tag{
		{Name: "App-Name", Value: "git-remote-arweave"},
		{Name: "Type", Value: "pack"},
	}

	txID, err := tu.Upload(context.Background(), []byte("test-data"), tags)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if txID != "turbo-item-id" {
		t.Errorf("txID = %q, want turbo-item-id", txID)
	}
	if receivedContentType != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", receivedContentType)
	}
	if len(receivedBody) == 0 {
		t.Error("expected non-empty body")
	}
}

func TestTurboUploader_Upload_FallbackID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	tu := newTestTurboUploader(t, server.URL)

	txID, err := tu.Upload(context.Background(), []byte("data"), nil)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if txID == "" {
		t.Error("expected non-empty fallback txID")
	}
}

func TestTurboUploader_Upload_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("insufficient credits"))
	}))
	defer server.Close()

	tu := newTestTurboUploader(t, server.URL)

	_, err := tu.Upload(context.Background(), []byte("data"), nil)
	if err == nil {
		t.Error("expected error for 402 response")
	}
}

func TestTurboUploader_GetPriceForBytes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/price/bytes/1000" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"winc":"500000000","adjustments":[]}`))
	}))
	defer server.Close()

	tu := &TurboUploader{
		paymentGateway: server.URL,
		http:           &http.Client{Timeout: 10 * time.Second},
	}

	winc, err := tu.GetPriceForBytes(context.Background(), 1000)
	if err != nil {
		t.Fatalf("GetPriceForBytes: %v", err)
	}
	if winc != 500000000 {
		t.Errorf("winc = %d, want 500000000", winc)
	}
}

func TestTurboUploader_GetBalance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/account/balance/arweave" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		addr := r.URL.Query().Get("address")
		if addr != "test-addr" {
			t.Errorf("address = %q, want test-addr", addr)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"winc":"1000000000000"}`))
	}))
	defer server.Close()

	tu := &TurboUploader{
		paymentGateway: server.URL,
		http:           &http.Client{Timeout: 10 * time.Second},
	}

	winc, err := tu.GetBalance(context.Background(), "test-addr")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if winc != 1000000000000 {
		t.Errorf("winc = %d, want 1000000000000", winc)
	}
}

func TestTurboUploader_GetBalance_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("User Not Found"))
	}))
	defer server.Close()

	tu := &TurboUploader{
		paymentGateway: server.URL,
		http:           &http.Client{Timeout: 10 * time.Second},
	}

	winc, err := tu.GetBalance(context.Background(), "unknown")
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if winc != 0 {
		t.Errorf("winc = %d, want 0 for unknown user", winc)
	}
}

func TestTurboUploader_ImplementsCostReporter(t *testing.T) {
	tu := &TurboUploader{}
	var _ CostReporter = tu // compile-time check
}

func newTestTurboUploader(t *testing.T, gateway string) *TurboUploader {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	signer := goar.NewSignerByPrivateKey(key)
	bundler, err := goar.NewBundler(signer)
	if err != nil {
		t.Fatalf("NewBundler: %v", err)
	}
	return &TurboUploader{
		bundler: bundler,
		gateway: gateway,
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}
