package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mcp-bridge/mcp-framework/newrelic"
)

func main() {
	// Define command-line flags
	writeEnabled := flag.Bool("write-enabled", false, "Enable write tools (disabled by default for safety)")
	flag.Parse()

	// Get API key from environment
	apiKey := os.Getenv("NEWRELIC_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "Error: NEWRELIC_API_KEY environment variable is required")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  export NEWRELIC_API_KEY=your_api_key_here")
		fmt.Fprintln(os.Stderr, "  export NEWRELIC_REGION=us  # or 'eu' for EU region (optional, defaults to us)")
		fmt.Fprintln(os.Stderr, "  /path/to/newrelic-mcp")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Note: EU region accounts must use NEWRELIC_REGION=eu")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fmt.Fprintln(os.Stderr, "  -write-enabled    Enable write tools (acknowledge violations, create alerts, etc.)")
		os.Exit(1)
	}

	// Get region from environment (defaults to us)
	region := strings.ToLower(os.Getenv("NEWRELIC_REGION"))
	if region == "" {
		region = "us"
	}

	// Create server with appropriate region and write-enabled flag
	var server *newrelic.Server
	if region == "eu" {
		server = newrelic.NewServerWithRegion(apiKey, "eu", *writeEnabled)
		fmt.Fprintln(os.Stderr, "New Relic MCP Server initialized (EU region)")
	} else {
		server = newrelic.NewServer(apiKey, *writeEnabled)
		fmt.Fprintln(os.Stderr, "New Relic MCP Server initialized (US region)")
	}

	if *writeEnabled {
		fmt.Fprintln(os.Stderr, "Write tools ENABLED")
	} else {
		fmt.Fprintln(os.Stderr, "Write tools disabled (use -write-enabled to enable)")
	}

	server.Initialize()

	fmt.Fprintf(os.Stderr, "Registered tools: %v\n", server.ListTools())
	fmt.Fprintln(os.Stderr, "Ready to serve requests via stdio...")

	// Start serving MCP requests via stdio (blocking)
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
