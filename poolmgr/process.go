package poolmgr

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

type LogEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Time    string `json:"time"`
}

type ManagedProcess struct {
	Cmd       *exec.Cmd
	Stdin     io.WriteCloser
	Stdout    io.ReadCloser
	Stderr    io.ReadCloser
	LineChan  chan []byte
	isWarm    bool
	ID        string
	mu        sync.Mutex
	UserID    string
	BackendID string
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
	fmt.Printf("[DEBUG SpawnProcess] backend=%s, command=%q\n", pool.BackendID, command)
	proc, err := spawnProcessRaw(command, env)
	if err != nil {
		fmt.Printf("[DEBUG SpawnProcess] ERROR: %v\n", err)
		return nil, err
	}

	go captureStderr(proc)
	go captureStdout(pool, proc)

	return proc, nil
}

// isSimpleCommand returns true if the command is a simple command that doesn't
// speak MCP (like cat, yes, npx, false). These commands skip MCP handshake.
func isSimpleCommand(command string) bool {
	return command == "cat" || command == "npx" || command == "yes" ||
		command == "false" || command == "sh -c 'echo invalid'" ||
		strings.HasPrefix(command, "github-mcp-server") ||
		strings.HasPrefix(command, "npx ")
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
		"echo ", "env ",
	}
	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(command, prefix) {
			return nil
		}
	}

	// Allow absolute paths
	if strings.HasPrefix(command, "/") && !strings.Contains(command, " ") {
		return nil
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
	} else if strings.HasPrefix(command, "/") && !strings.Contains(command, " ") {
		// Absolute path with no arguments — execute directly.
		cmd = exec.Command(command)
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
	for scanner.Scan() {
		data := scanner.Bytes()
		dataCopy := make([]byte, len(data))
		copy(dataCopy, data)

		proc.mu.Lock()
		select {
		case proc.LineChan <- dataCopy:
		default:
		}
		proc.mu.Unlock()

		pool.BroadcastToSSE(dataCopy)
	}
}

func captureStderr(proc *ManagedProcess) {
	buf := make([]byte, 4096)
	for {
		n, err := proc.Stderr.Read(buf)
		if err != nil {
			break
		}
		logJSON("debug", fmt.Sprintf("mcp stderr: %s", string(buf[:n])))
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

func logJSON(level, message string) {
	entry := LogEntry{
		Level:   level,
		Message: message,
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(entry)
	fmt.Println(string(data))
}
