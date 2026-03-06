package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"git-remote-arweave/internal/helper"
)

func main() {
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
