package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/crypto"
	"git-remote-arweave/internal/manifest"
)

func cmdDecrypt(args []string) {
	if len(args) < 1 {
		fatalf("usage: arweave-git decrypt <tx-id> [--keymap <keymap-tx>]")
	}
	txID := args[0]
	keymapTx := parseFlag(args[1:], "--keymap")

	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		fatalf("load config: %v", err)
	}

	ar, err := arweave.New(cfg)
	if err != nil {
		fatalf("create client: %v", err)
	}

	if ar.Owner() == "" {
		fatalf("no wallet configured (set arweave.wallet)")
	}

	// Auto-detect keymap from TX tags if not provided.
	if keymapTx == "" {
		fmt.Fprintf(os.Stderr, "looking up tags for %s...\n", txID)
		tags, err := ar.QueryTxTags(ctx, txID)
		if err != nil {
			fatalf("query tx tags: %v", err)
		}
		if tags == nil {
			fatalf("transaction %s not found in GraphQL", txID)
		}
		keymapTx = tags[manifest.TagKeyMap]
		if keymapTx == "" {
			fatalf("no Key-Map tag on %s; use --keymap to specify", txID)
		}
		fmt.Fprintf(os.Stderr, "found Key-Map: %s\n", keymapTx)
	}

	// Fetch and parse keymap.
	fmt.Fprintf(os.Stderr, "fetching keymap %s...\n", keymapTx)
	kmData, err := ar.Fetch(ctx, keymapTx)
	if err != nil {
		fatalf("fetch keymap: %v", err)
	}

	km, err := crypto.ParseKeyMap(kmData)
	if err != nil {
		fatalf("parse keymap: %v", err)
	}

	epoch := km.LatestEpoch()
	if epoch < 0 {
		fatalf("keymap has no epochs")
	}

	symKey, err := km.GetKey(epoch, ar.Owner(), ar.RSAPrivateKey())
	if err != nil {
		// Try all epochs — the data may be from an older epoch.
		found := false
		for e := epoch - 1; e >= 0; e-- {
			symKey, err = km.GetKey(e, ar.Owner(), ar.RSAPrivateKey())
			if err == nil {
				epoch = e
				found = true
				break
			}
		}
		if !found {
			fatalf("unwrap key: wallet not in any keymap epoch")
		}
	}
	fmt.Fprintf(os.Stderr, "unwrapped key from epoch %d\n", epoch)

	// Fetch and decrypt data.
	fmt.Fprintf(os.Stderr, "fetching %s...\n", txID)
	ciphertext, err := ar.Fetch(ctx, txID)
	if err != nil {
		fatalf("fetch data: %v", err)
	}

	plaintext, err := crypto.Open(ciphertext, &symKey)
	if err != nil {
		fatalf("decrypt: %v", err)
	}

	// Try to pretty-print JSON, otherwise output raw.
	var obj interface{}
	if json.Unmarshal(plaintext, &obj) == nil {
		pretty, _ := json.MarshalIndent(obj, "", "  ")
		fmt.Println(string(pretty))
	} else {
		os.Stdout.Write(plaintext)
	}
}
