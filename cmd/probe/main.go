package main

import (
	"fmt"
	"os"
)

const appVersion = "v1.6.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	sub := os.Args[1]
	args := os.Args[2:]

	switch sub {
	case "serve":
		runServe(args)
	case "connect":
		runConnect(args)
	case "relay":
		runRelay(args)
	case "--version", "-version", "version":
		fmt.Printf("PROBE %s\n", appVersion)
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n\n", sub)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `PROBE %s — Platform for Remote Operations & Bridge Environment

Usage:
  probe <subcommand> [flags]

Subcommands:
  serve     Start as server (top of tree, listens for agents + operators)
  connect   Start as client/agent (connects to server or relay)
  relay     Start as relay bridge (listens downstream, connects upstream)
  version   Print version
  help      Show this usage

Quick start:
  probe serve --addr :7700 --admin-password admin
  probe connect --config probe-client.json
  probe relay --upstream wss://server:7700/ws --token secret

`, appVersion)
}