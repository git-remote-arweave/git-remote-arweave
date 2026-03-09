package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git-remote-arweave/internal/config"
	"git-remote-arweave/internal/localstate"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	switch os.Args[1] {
	case "env":
		cmdEnv()
	case "readers":
		cmdReaders(os.Args[2:])
	default:
		fatalf("unknown command: %s", os.Args[1])
	}
}

func cmdEnv() {
	entries := config.Env()
	for _, e := range entries {
		if e.Source == config.SourceUnset {
			fmt.Printf("%-25s (not set)\n", e.Key)
		} else {
			fmt.Printf("%-25s %s\t(%s)\n", e.Key, e.Value, e.Source)
		}
	}
}

func cmdReaders(args []string) {
	if len(args) == 0 {
		fatalf("usage: arweave-git readers <add|remove|list> [address] [--pubkey <n>]")
	}

	state, err := openState()
	if err != nil {
		fatalf("%v", err)
	}

	switch args[0] {
	case "add":
		if len(args) < 2 {
			fatalf("usage: arweave-git readers add <wallet-address> [--pubkey <base64url-modulus>]")
		}
		address := args[1]
		pubkey := parseFlag(args[2:], "--pubkey")
		added, err := state.AddReader(address, pubkey)
		if err != nil {
			fatalf("add reader: %v", err)
		}
		if added {
			if pubkey != "" {
				fmt.Printf("added reader %s (with public key)\n", address)
			} else {
				fmt.Printf("added reader %s\n", address)
			}
		} else {
			fmt.Printf("reader %s already exists\n", address)
		}

	case "remove":
		if len(args) != 2 {
			fatalf("usage: arweave-git readers remove <wallet-address>")
		}
		removed, err := state.RemoveReader(args[1])
		if err != nil {
			fatalf("remove reader: %v", err)
		}
		if removed {
			fmt.Printf("removed reader %s\n", args[1])
		} else {
			fmt.Printf("reader %s not found\n", args[1])
		}

	case "list":
		readers, err := state.LoadReaders()
		if err != nil {
			fatalf("list readers: %v", err)
		}
		if len(readers) == 0 {
			fmt.Println("no readers configured")
			return
		}
		for _, r := range readers {
			if r.PubKey != "" {
				fmt.Printf("%s\t(pubkey: %s...)\n", r.Address, truncate(r.PubKey, 16))
			} else {
				fmt.Printf("%s\t(no pubkey)\n", r.Address)
			}
		}

	default:
		fatalf("unknown readers subcommand: %s", args[0])
	}
}

// parseFlag extracts the value of a named flag from args.
// Returns empty string if the flag is not present.
func parseFlag(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// truncate returns the first n characters of s, or s if shorter.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// openState locates the git directory and opens the arweave local state.
func openState() (*localstate.State, error) {
	gitDir, err := findGitDir()
	if err != nil {
		return nil, fmt.Errorf("not a git repository (or any parent): %w", err)
	}
	return localstate.New(gitDir)
}

// findGitDir returns the .git directory path for the current repository.
func findGitDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--git-dir").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: arweave-git <command> [args]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  env                                   show resolved config and sources")
	fmt.Fprintln(os.Stderr, "  readers add <address> [--pubkey <n>]  add a reader wallet")
	fmt.Fprintln(os.Stderr, "  readers remove <address>              remove a reader wallet")
	fmt.Fprintln(os.Stderr, "  readers list                          list reader wallets")
	os.Exit(1)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "arweave-git: "+format+"\n", args...)
	os.Exit(1)
}
