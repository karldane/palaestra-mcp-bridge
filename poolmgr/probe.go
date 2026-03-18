package poolmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// ---------- Backend health probing ----------

// ProbeResult holds the outcome of probing a backend command.
type ProbeResult struct {
	Status     string `json:"status"`  // "ok", "spawn_error", "handshake_error", "handshake_timeout"
	Message    string `json:"message"` // human-readable summary
	Stderr     string `json:"stderr"`  // captured stderr output
	DurationMs int64  `json:"duration_ms"`
}

// ProbeBackend spawns a temporary process with the given command, attempts
// the MCP initialize + notifications/initialized handshake, captures stderr,
// and returns a ProbeResult. The process is always killed before returning.
// The timeout controls how long to wait for the handshake response.
func ProbeBackend(command string, env []string, timeout time.Duration) *ProbeResult {
	start := time.Now()

	// Create a minimal pool for the response dispatcher (size 0, not added
	// to any manager).
	pool := &Pool{
		Warm:            make(chan *ManagedProcess, 1),
		Spawning:        make(chan struct{}, 1),
		Command:         command,
		Env:             env,
		backoffDelay:    100 * time.Millisecond,
		pendingRequests: make(map[string]*PendingRequest),
		broadcastCh:     make(chan []byte, 100),
		BackendID:       "__probe__",
		lastUsed:        time.Now(),
	}
	go pool.responseDispatcher()

	// Use spawnProcessRaw so we control stdout/stderr capture ourselves.
	proc, err := spawnProcessRaw(command, env)
	if err != nil {
		return &ProbeResult{
			Status:     "spawn_error",
			Message:    fmt.Sprintf("Failed to spawn: %v", err),
			DurationMs: time.Since(start).Nanoseconds() / 1e6,
		}
	}
	defer proc.Kill()

	// Capture stderr in the background.
	stderrBuf := &bytes.Buffer{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := proc.Stderr.Read(buf)
			if n > 0 {
				stderrBuf.Write(buf[:n])
			}
			if readErr != nil {
				return
			}
		}
	}()

	// Capture stdout and broadcast to pool's response dispatcher.
	go captureStdout(pool, proc)

	// Attempt MCP handshake.
	initID := fmt.Sprintf("probe-%d", time.Now().UnixNano())

	initMsg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      initID,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "mcp-bridge-probe",
				"version": "1.0.0",
			},
		},
	}

	initBody, _ := json.Marshal(initMsg)
	buf := new(bytes.Buffer)
	if compErr := json.Compact(buf, initBody); compErr != nil {
		buf.Write(initBody)
	}
	buf.WriteByte('\n')

	respCh := pool.RegisterRequest(initID)
	proc.Stdin.Write(buf.Bytes())

	select {
	case resp, ok := <-respCh:
		pool.UnregisterRequest(initID)
		if !ok || len(resp) == 0 {
			// Give stderr a moment to flush.
			time.Sleep(50 * time.Millisecond)
			return &ProbeResult{
				Status:     "handshake_error",
				Message:    "Initialize response channel closed with no data",
				Stderr:     stderrBuf.String(),
				DurationMs: time.Since(start).Nanoseconds() / 1e6,
			}
		}

		// Send notifications/initialized.
		notifMsg := JSONRPCMessage{
			JSONRPC: "2.0",
			Method:  "notifications/initialized",
		}
		notifBody, _ := json.Marshal(notifMsg)
		buf.Reset()
		if compErr := json.Compact(buf, notifBody); compErr != nil {
			buf.Write(notifBody)
		}
		buf.WriteByte('\n')
		proc.Stdin.Write(buf.Bytes())

		// Give stderr a moment to flush.
		time.Sleep(50 * time.Millisecond)

		return &ProbeResult{
			Status:     "ok",
			Message:    "MCP handshake succeeded",
			Stderr:     stderrBuf.String(),
			DurationMs: time.Since(start).Nanoseconds() / 1e6,
		}
	case <-time.After(timeout):
		pool.UnregisterRequest(initID)
		time.Sleep(50 * time.Millisecond)
		return &ProbeResult{
			Status:     "handshake_timeout",
			Message:    fmt.Sprintf("No initialize response within %s", timeout),
			Stderr:     stderrBuf.String(),
			DurationMs: time.Since(start).Nanoseconds() / 1e6,
		}
	}
}
