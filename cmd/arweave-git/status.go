package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"git-remote-arweave/internal/arweave"
	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/helper"
	"git-remote-arweave/internal/manifest"
)

type txEntry struct {
	txID    string
	txType  string // "manifest" or "pack"
	note    string // "head", "genesis", "pending", etc.
	gw      bool   // HEAD 200 on L1 gateway (arweave.net)
	cdn     bool   // HEAD 200 on Turbo CDN
	graphql bool   // exists in GraphQL index
}

func cmdStatus(args []string) {
	limit := 10
	showAll := false
	for _, a := range args {
		if a == "--all" {
			showAll = true
			limit = 0
		}
	}

	ctx := context.Background()

	remoteURL := findArweaveRemote()
	owner, repoName, err := helper.ParseURL(remoteURL)
	if err != nil {
		fatalf("%v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		fatalf("load config: %v", err)
	}

	ar, err := arweave.New(cfg)
	if err != nil {
		fatalf("create client: %v", err)
	}

	state, err := openState()
	if err != nil {
		fatalf("%v", err)
	}

	// 1. Get manifest chain from GraphQL.
	fmt.Fprintf(os.Stderr, "querying manifest chain...\n")
	chain, err := ar.QueryManifestChain(ctx, owner, repoName)
	if err != nil {
		fatalf("query chain: %v", err)
	}

	totalManifests := len(chain)

	// 2. Check for pending manifest not yet in GraphQL.
	pending, _, _ := state.LoadPending()
	hasPendingNotInChain := false
	if pending != nil {
		inChain := false
		for _, m := range chain {
			if m.TxID == pending.ManifestTxID {
				inChain = true
				break
			}
		}
		hasPendingNotInChain = !inChain
	}

	// 3. Apply limit to chain.
	limited := chain
	if limit > 0 && len(limited) > limit {
		limited = limited[:limit]
	}

	// 4. Collect pack tx-ids from the most complete source.
	// Priority: pending state packs (most complete) > manifest body fetch.
	var packTxIDs []string
	if pending != nil && len(pending.Packs) > 0 {
		// Pending state has the full packs list (works for encrypted repos too).
		for _, p := range pending.Packs {
			packTxIDs = append(packTxIDs, p.TX)
		}
	} else if len(chain) > 0 && !chain[0].Encrypted {
		// Fetch head manifest body to get packs.
		data, fetchErr := ar.Fetch(ctx, chain[0].TxID)
		if fetchErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not fetch head manifest: %v\n", fetchErr)
		} else if m, parseErr := manifest.Parse(data); parseErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not parse head manifest: %v\n", parseErr)
		} else {
			for _, p := range m.Packs {
				packTxIDs = append(packTxIDs, p.TX)
			}
		}
	} else if len(chain) > 0 && chain[0].Encrypted {
		fmt.Fprintf(os.Stderr, "note: encrypted repo, pack list unavailable (no local state)\n")
	}

	// 5. Build entry list.
	var entries []txEntry

	if hasPendingNotInChain {
		entries = append(entries, txEntry{txID: pending.ManifestTxID, txType: "manifest", note: "pending"})
		if pending.PackTxID != "" {
			entries = append(entries, txEntry{txID: pending.PackTxID, txType: "pack"})
		}
		totalManifests++
	}

	keymapSeen := make(map[string]bool)
	for i, m := range limited {
		note := ""
		if i == 0 && !hasPendingNotInChain {
			note = "head"
		} else if i == 0 {
			note = "head (gql)"
		}
		if m.IsGenesis {
			if note != "" {
				note += ", genesis"
			} else {
				note = "genesis"
			}
		}
		entries = append(entries, txEntry{txID: m.TxID, txType: "manifest", note: note})
		if m.KeyMapTx != "" && !keymapSeen[m.KeyMapTx] {
			keymapSeen[m.KeyMapTx] = true
			entries = append(entries, txEntry{txID: m.KeyMapTx, txType: "keymap"})
		}
	}

	// Packs (newest first), skipping any already shown as pending.
	pendingPackID := ""
	if hasPendingNotInChain && pending != nil {
		pendingPackID = pending.PackTxID
	}
	for i := len(packTxIDs) - 1; i >= 0; i-- {
		if packTxIDs[i] == pendingPackID {
			continue
		}
		entries = append(entries, txEntry{txID: packTxIDs[i], txType: "pack"})
	}

	if len(entries) == 0 {
		fmt.Println("no transactions found for this repository")
		return
	}

	// 6. Check availability (parallel).
	fmt.Fprintf(os.Stderr, "checking %d transactions...\n", len(entries))
	checkAvailability(ctx, ar, entries)

	// 7. Display.
	dualGateway := ar.Gateway() != ar.FetchGateway()
	shownManifests := 0
	packCount := 0
	keymapCount := 0
	for _, e := range entries {
		switch e.txType {
		case "manifest":
			shownManifests++
		case "pack":
			packCount++
		case "keymap":
			keymapCount++
		}
	}

	summary := fmt.Sprintf("arweave://%s/%s (%d manifests, %d packs", owner, repoName, totalManifests, packCount)
	if keymapCount > 0 {
		summary += fmt.Sprintf(", %d keymaps", keymapCount)
	}
	summary += ")"
	fmt.Println(summary)
	fmt.Println()

	if dualGateway {
		fmt.Printf("  %-9s %-43s  %-3s %-3s %-3s\n", "TYPE", "TX", "GW", "CDN", "GQL")
		fmt.Printf("  %-9s %-43s  %-3s %-3s %-3s\n", "─────────", strings.Repeat("─", 43), "───", "───", "───")
	} else {
		fmt.Printf("  %-9s %-43s  %-3s %-3s\n", "TYPE", "TX", "GW", "GQL")
		fmt.Printf("  %-9s %-43s  %-3s %-3s\n", "─────────", strings.Repeat("─", 43), "───", "───")
	}

	for _, e := range entries {
		suffix := ""
		if e.note != "" {
			suffix = "  ← " + e.note
		}
		if dualGateway {
			fmt.Printf("  %-9s %s  %s %s %s%s\n",
				e.txType, e.txID,
				mark(e.gw), mark(e.cdn), mark(e.graphql),
				suffix)
		} else {
			fmt.Printf("  %-9s %s  %s %s%s\n",
				e.txType, e.txID,
				mark(e.gw), mark(e.graphql),
				suffix)
		}
	}

	remaining := totalManifests - shownManifests
	if !showAll && remaining > 0 {
		fmt.Printf("\n  %d earlier manifests not shown (use --all)\n", remaining)
	}
}

func mark(ok bool) string {
	if ok {
		return " ✓ "
	}
	return " ✗ "
}

func checkAvailability(ctx context.Context, ar *arweave.Client, entries []txEntry) {
	// Collect unique tx-ids.
	var txIDs []string
	seen := make(map[string]bool)
	for _, e := range entries {
		if !seen[e.txID] {
			txIDs = append(txIDs, e.txID)
			seen[e.txID] = true
		}
	}

	// GraphQL batch check.
	gqlResult, err := ar.QueryTxExistence(ctx, txIDs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: GraphQL check failed: %v\n", err)
		gqlResult = map[string]bool{}
	}

	// HEAD checks in parallel.
	gwURL := ar.Gateway()
	cdnGateway := ar.FetchGateway()

	type headResult struct {
		txID string
		gw   string // "gw" or "cdn"
		ok   bool
	}

	var wg sync.WaitGroup
	results := make(chan headResult, len(txIDs)*2)
	sem := make(chan struct{}, 8) // concurrency limit

	for _, txID := range txIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ok := ar.HeadTx(ctx, gwURL, id)
			results <- headResult{id, "gw", ok}
		}(txID)

		if cdnGateway != gwURL {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				ok := ar.HeadTx(ctx, cdnGateway, id)
				results <- headResult{id, "cdn", ok}
			}(txID)
		}
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	gwResults := make(map[string]bool, len(txIDs))
	cdnResults := make(map[string]bool, len(txIDs))
	for r := range results {
		if r.gw == "gw" {
			gwResults[r.txID] = r.ok
		} else {
			cdnResults[r.txID] = r.ok
		}
	}

	// Apply results.
	for i := range entries {
		id := entries[i].txID
		entries[i].graphql = gqlResult[id]
		entries[i].gw = gwResults[id]
		if cdnGateway != gwURL {
			entries[i].cdn = cdnResults[id]
		} else {
			entries[i].cdn = entries[i].gw
		}
	}
}

// findArweaveRemote finds the first arweave:// remote URL in git config.
func findArweaveRemote() string {
	out, err := exec.Command("git", "remote", "-v").Output()
	if err != nil {
		fatalf("not a git repository or no remotes configured")
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.HasPrefix(fields[1], "arweave://") {
			return fields[1]
		}
	}
	fatalf("no arweave:// remote found")
	return ""
}
