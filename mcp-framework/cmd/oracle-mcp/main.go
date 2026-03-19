package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mcp-bridge/mcp-framework/oracle"
)

func main() {
	// Define command-line flags
	writeEnabled := flag.Bool("write-enabled", false, "Enable write tools (disabled by default for safety)")
	flag.Parse()

	// Get connection string from environment
	connString := os.Getenv("ORACLE_CONNECTION_STRING")
	if connString == "" {
		fmt.Fprintln(os.Stderr, "Error: ORACLE_CONNECTION_STRING environment variable is required")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintln(os.Stderr, "  export ORACLE_CONNECTION_STRING=oracle://user:pass@host:port/service")
		fmt.Fprintln(os.Stderr, "  oracle-mcp")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Environment Variables:")
		fmt.Fprintln(os.Stderr, "  ORACLE_CONNECTION_STRING  Oracle connection string (required)")
		fmt.Fprintln(os.Stderr, "  ORACLE_READ_ONLY         Set to 'false' to enable write operations (default: true)")
		fmt.Fprintln(os.Stderr, "  CACHE_DIR                Directory for schema cache (default: .cache)")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Flags:")
		fmt.Fprintln(os.Stderr, "  -write-enabled           Enable write tools (INSERT, UPDATE, DELETE)")
		os.Exit(1)
	}

	// Check read-only mode
	readOnly := true
	if val := os.Getenv("ORACLE_READ_ONLY"); val != "" {
		readOnly = val != "false"
	}

	// Create server
	server, err := oracle.NewServer(connString, readOnly)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to create server: %v\n", err)
		os.Exit(1)
	}
	defer server.Close()

	// Set write enabled flag
	server.SetWriteEnabled(*writeEnabled)

	// Initialize schema cache
	fmt.Fprintln(os.Stderr, "Initializing schema cache...")
	if err := server.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to initialize schema cache: %v\n", err)
	}

	// Print status
	fmt.Fprintln(os.Stderr, "Oracle MCP Server initialized")
	if readOnly {
		fmt.Fprintln(os.Stderr, "Read-only mode: enabled")
	} else {
		fmt.Fprintln(os.Stderr, "Read-only mode: disabled")
	}
	if *writeEnabled {
		fmt.Fprintln(os.Stderr, "Write tools: ENABLED")
	} else {
		fmt.Fprintln(os.Stderr, "Write tools: disabled (use -write-enabled to enable)")
	}

	fmt.Fprintf(os.Stderr, "Registered tools: %v\n", server.ListTools())
	fmt.Fprintln(os.Stderr, "Ready to serve requests via stdio...")

	// Start serving MCP requests via stdio (blocking)
	if err := server.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}
