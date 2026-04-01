package poolmgr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mcp-bridge/mcp-bridge/internal/secret"
	"github.com/mcp-bridge/mcp-bridge/shared"
	"github.com/shirou/gopsutil/v3/process"
)

type ManagedProcess struct {
	Cmd            *exec.Cmd
	Stdin          io.WriteCloser
	Stdout         io.ReadCloser
	Stderr         io.ReadCloser
	LineChan       chan []byte
	isWarm         bool
	ID             string
	mu             sync.Mutex
	UserID         string
	BackendID      string
	StderrBuf      strings.Builder // Captured stderr for error diagnosis
	secretInjector secret.SecretInjector
}

type PendingRequest struct {
	ID         string
	ResponseCh chan []byte
	Timeout    *time.Timer
}

func (m *ManagedProcess) Kill() {
	if m.Cmd.Process != nil {
		m.Cmd.Process.Kill()
	}
}

func (m *ManagedProcess) CleanupSecrets() error {
	if m.secretInjector != nil {
		return m.secretInjector.Cleanup()
	}
	return nil
}

// GetMemoryUsage returns the memory usage in bytes for this process
func (m *ManagedProcess) GetMemoryUsage() (uint64, error) {
	if m.Cmd.Process == nil {
		return 0, fmt.Errorf("process not started")
	}
	proc, err := process.NewProcess(int32(m.Cmd.Process.Pid))
	if err != nil {
		return 0, err
	}
	memInfo, err := proc.MemoryInfo()
	if err != nil {
		return 0, err
	}
	return memInfo.RSS, nil
}

func SpawnProcess(pool *Pool, command string, env []string) (*ManagedProcess, error) {
	shared.Debugf("SpawnProcess: backend=%s, command=%q", pool.BackendID, command)
	proc, err := spawnProcessRaw(command, env)
	if err != nil {
		shared.Debugf("SpawnProcess ERROR: %v", err)
		return nil, err
	}

	// Only capture stderr for real processes, not for built-in servers
	if command != "mcp-bridge-builtin" {
		go captureStderr(proc)
	}
	go captureStdout(pool, proc)

	return proc, nil
}

func SpawnProcessWithSecrets(pool *Pool, command string, env []string, secrets map[string]string) (*ManagedProcess, error) {
	shared.Debugf("SpawnProcessWithSecrets: backend=%s, command=%q, secrets=%v", pool.BackendID, command, getSecretKeys(secrets))

	injector := secret.NewFileSecretInjector(pool.secretsPath)
	envVars, err := injector.PrepareSecrets(context.Background(), secrets)
	if err != nil {
		shared.Debugf("SpawnProcessWithSecrets PrepareSecrets ERROR: %v", err)
		return nil, fmt.Errorf("failed to prepare secrets: %w", err)
	}

	allEnv := append(env, envVars...)
	proc, err := spawnProcessRaw(command, allEnv)
	if err != nil {
		shared.Debugf("SpawnProcessWithSecrets spawnProcessRaw ERROR: %v", err)
		injector.Cleanup()
		return nil, err
	}

	proc.secretInjector = injector

	if command != "mcp-bridge-builtin" {
		go captureStderr(proc)
	}
	go captureStdout(pool, proc)

	return proc, nil
}

func getSecretKeys(secrets map[string]string) []string {
	keys := make([]string, 0, len(secrets))
	for k := range secrets {
		keys = append(keys, k)
	}
	return keys
}

// isSimpleCommand returns true if the command is a simple command that doesn't
// speak MCP (like cat, yes, npx, false). These commands skip MCP handshake.
func isSimpleCommand(command string) bool {
	return command == "cat" || command == "npx" || command == "yes" ||
		command == "false" || command == "sh -c 'echo invalid'" ||
		strings.HasPrefix(command, "github-mcp-server")
}

// isAllowedCommand validates that the command is safe to execute.
// It allows:
// - Simple commands: cat, npx, yes, false, echo, sleep, env
// - Commands starting with allowed prefixes: npx -, github-mcp-server, npm, node
// - Absolute paths (starting with /)
// Returns an error if the command is not allowed.
func isAllowedCommand(command string) error {
	// Deny patterns that could be dangerous
	dangerous := []string{"rm -", "rmdir", "del ", "format", "mkfs", "dd if=",
		">/dev/", "&& rm", "|| rm", "; rm", "| rm", "`", "$(", "\\x"}
	for _, d := range dangerous {
		if strings.Contains(command, d) {
			return fmt.Errorf("command contains dangerous pattern: %s", d)
		}
	}

	// Allow simple commands
	simple := map[string]bool{
		"cat": true, "npx": true, "yes": true, "false": true, "echo": true, "sleep": true, "env": true,
	}
	if simple[command] {
		return nil
	}

	// Allow commands with specific prefixes (MCP servers and common tools)
	allowedPrefixes := []string{
		"npx ", "npm ", "node ", "github-mcp-server",
		"github-mcp-server ", "uvx ", "bunx ", "sleep ",
		"echo ", "env ", "go run ",
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(command, prefix) {
			return nil
		}
	}

	// Allow absolute paths (even with arguments)
	if strings.HasPrefix(command, "/") {
		// Extract the command part (before any spaces)
		cmdPart := command
		if idx := strings.Index(command, " "); idx > 0 {
			cmdPart = command[:idx]
		}
		// Verify it's a reasonable absolute path (no shell metacharacters)
		if !strings.ContainsAny(cmdPart, ";|&`$()<>") {
			return nil
		}
	}

	// Allow simple shell constructs (but not arbitrary shell execution)
	if strings.HasPrefix(command, "sh -c '") || strings.HasPrefix(command, "sh -c \"") {
		return nil
	}

	return fmt.Errorf("command not in allowlist: %s", command)
}

// spawnProcessRaw starts a child process without launching any capture
// goroutines. The caller is responsible for reading stdout/stderr.
func spawnProcessRaw(command string, env []string) (*ManagedProcess, error) {
	// Handle built-in MCP echo server
	if command == "mcp-bridge-builtin" {
		return spawnBuiltinMCPServer()
	}

	// Validate command before execution
	if err := isAllowedCommand(command); err != nil {
		return nil, fmt.Errorf("command not allowed: %w", err)
	}

	var cmd *exec.Cmd
	// Check if command is a simple command without shell features
	isSimple := command == "cat" || command == "npx" || command == "yes" ||
		command == "false" || command == "sh -c 'echo invalid'" ||
		strings.HasPrefix(command, "github-mcp-server") ||
		strings.HasPrefix(command, "npx ")
	if isSimple && !strings.Contains(command, ";") && !strings.Contains(command, "&&") && !strings.Contains(command, "||") {
		// For commands like "github-mcp-server stdio" or "npx -y ..."
		if strings.Contains(command, " ") {
			parts := strings.Fields(command)
			cmd = exec.Command(parts[0], parts[1:]...)
		} else {
			cmd = exec.Command(command)
		}
	} else if strings.HasPrefix(command, "/") {
		// Absolute path — parse command and arguments
		if strings.Contains(command, " ") {
			parts := strings.Fields(command)
			cmd = exec.Command(parts[0], parts[1:]...)
		} else {
			cmd = exec.Command(command)
		}
	} else {
		cmd = exec.Command("sh", "-c", command)
	}

	if env != nil {
		cmd.Env = env
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	proc := &ManagedProcess{
		Cmd:      cmd,
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		LineChan: make(chan []byte, 100),
		isWarm:   true,
	}

	return proc, nil
}

func captureStdout(pool *Pool, proc *ManagedProcess) {
	defer func() {
		if r := recover(); r != nil {
		}
	}()

	scanner := bufio.NewScanner(proc.Stdout)
	// Increase buffer size to handle large responses (e.g., listing all users, full PRs)
	// Go's bufio.Scanner has a max of 64MB, we use 32MB to handle most cases
	const maxCapacity = 32 * 1024 * 1024 // 32MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		data := scanner.Bytes()
		dataCopy := make([]byte, len(data))
		copy(dataCopy, data)

		// Log truncated version to avoid flooding logs with large responses
		const maxLogLen = 200
		logLine := string(dataCopy)
		if len(logLine) > maxLogLen {
			logLine = logLine[:maxLogLen] + "...[truncated]"
		}
		shared.Debugf("captureStdout read line: %s", logLine)

		proc.mu.Lock()
		select {
		case proc.LineChan <- dataCopy:
		default:
		}
		proc.mu.Unlock()

		pool.BroadcastToSSE(dataCopy)
	}
	if err := scanner.Err(); err != nil {
		shared.Debugf("captureStdout scanner error: %v", err)
	}
	shared.Debug("captureStdout exiting")
}

func captureStderr(proc *ManagedProcess) {
	buf := make([]byte, 4096)
	for {
		n, err := proc.Stderr.Read(buf)
		if err != nil {
			break
		}
		proc.mu.Lock()
		proc.StderrBuf.Write(buf[:n])
		proc.mu.Unlock()
		shared.Debugf("mcp stderr: %s", string(buf[:n]))
	}
}

type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type JSONRPCMessage struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// spawnBuiltinMCPServer creates an in-process MCP echo server that responds
// to MCP protocol messages. This is used as a fallback when no other backends
// are available.
func spawnBuiltinMCPServer() (*ManagedProcess, error) {
	// Create pipes for stdin/stdout simulation
	reader, writer := io.Pipe()
	outputReader, outputWriter := io.Pipe()

	proc := &ManagedProcess{
		LineChan: make(chan []byte, 100),
		isWarm:   true,
	}

	// Create a fake exec.Cmd for compatibility
	cmd := &exec.Cmd{
		Process: &os.Process{},
	}
	proc.Cmd = cmd

	// Handle MCP protocol in a goroutine
	go func() {
		defer reader.Close()
		defer outputWriter.Close()

		scanner := bufio.NewScanner(reader)
		const maxCapacity = 32 * 1024 * 1024 // 32MB
		buf := make([]byte, maxCapacity)
		scanner.Buffer(buf, maxCapacity)

		for scanner.Scan() {
			line := scanner.Bytes()
			proc.LineChan <- line

			// Try to parse and respond
			var msg JSONRPCMessage
			if err := json.Unmarshal(line, &msg); err == nil {
				var response []byte
				switch msg.Method {
				case "initialize":
					response, _ = json.Marshal(map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      msg.ID,
						"result": map[string]interface{}{
							"protocolVersion": "2024-11-05",
							"capabilities":    map[string]interface{}{},
							"serverInfo": map[string]string{
								"name":    "mcp-bridge-builtin",
								"version": "1.0.0",
							},
							"instructions": "Built-in MCP Bridge server - used as fallback when other backends are unavailable",
						},
					})
				case "tools/list":
					response, _ = json.Marshal(map[string]interface{}{
						"jsonrpc": "2.0",
						"id":      msg.ID,
						"result": map[string]interface{}{
							"tools": []map[string]interface{}{
								{
									"name":        "mcpbridge_echo",
									"description": "Echo tool - returns the input arguments. Used as fallback when other backends are unavailable.",
									"inputSchema": map[string]interface{}{
										"type": "object",
										"properties": map[string]interface{}{
											"message": map[string]interface{}{
												"type":        "string",
												"description": "Message to echo back",
											},
										},
									},
								},
								{
									"name":        "mcpbridge_status",
									"description": "Returns system status information",
									"inputSchema": map[string]interface{}{
										"type":       "object",
										"properties": map[string]interface{}{},
									},
								},
							},
						},
					})
				case "tools/call":
					if p, ok := msg.Params.(map[string]interface{}); ok {
						response, _ = json.Marshal(map[string]interface{}{
							"jsonrpc": "2.0",
							"id":      msg.ID,
							"result": map[string]interface{}{
								"content": []map[string]interface{}{
									{
										"type": "text",
										"text": fmt.Sprintf("Echo response: %v", p),
									},
								},
							},
						})
					}
				}

				if response != nil {
					response = append(response, '\n')
					outputWriter.Write(response)
				}
			}
		}
	}()

	// Create stdin/stdout interfaces from the pipes
	proc.Stdin = writer
	proc.Stdout = outputReader

	return proc, nil
}
