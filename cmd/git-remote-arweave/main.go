package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"git-remote-arweave/internal/helper"
)

// Set at build time via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("git-remote-arweave", version)
		return
	}

	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: git-remote-arweave <remote-name> <url>\n")
		os.Exit(1)
	}

	remoteURL := os.Args[2]

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := helper.Run(ctx, remoteURL, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %s\n", err)
		os.Exit(1)
	}
}
