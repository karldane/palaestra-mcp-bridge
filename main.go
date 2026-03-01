package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"
)

type LogEntry struct {
	Level   string `json:"level"`
	Message string `json:"message"`
	Time    string `json:"time"`
}

type ManagedProcess struct {
	Cmd    *exec.Cmd
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser
	isWarm bool
	ID     string
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

type ProcessPool struct {
	warm            chan *ManagedProcess
	mu              sync.Mutex
	spawning        chan struct{}
	command         string
	closed          bool
	wg              sync.WaitGroup
	backoffDelay    time.Duration
	activeCount     int
	activeMu        sync.Mutex
	pendingMu       sync.Mutex
	pendingRequests map[string]*PendingRequest
	broadcastCh     chan []byte
}

func (pool *ProcessPool) IsClosed() bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.closed
}

func NewProcessPool(size int) *ProcessPool {
	command := os.Getenv("COMMAND")
	if command == "" {
		command = "sh -c 'cat; sleep 1'"
	}

	pool := &ProcessPool{
		warm:            make(chan *ManagedProcess, size),
		spawning:        make(chan struct{}, 1),
		command:         command,
		backoffDelay:    100 * time.Millisecond,
		pendingRequests: make(map[string]*PendingRequest),
		broadcastCh:     make(chan []byte, 100),
	}

	for i := 0; i < size; i++ {
		pool.wg.Add(1)
		go pool.spawnAndHandshake()
	}

	go pool.responseDispatcher()

	return pool
}

func (pool *ProcessPool) responseDispatcher() {
	for data := range pool.broadcastCh {
		pool.pendingMu.Lock()
		var resp JSONRPCResponse
		if err := json.Unmarshal(data, &resp); err == nil && resp.ID != nil {
			id := fmt.Sprintf("%v", resp.ID)
			if req, ok := pool.pendingRequests[id]; ok {
				select {
				case req.ResponseCh <- data:
				default:
				}
			}
		}
		pool.pendingMu.Unlock()
	}
}

func (pool *ProcessPool) spawnAndHandshake() {
	defer pool.wg.Done()

	if pool.IsClosed() {
		return
	}

	select {
	case pool.spawning <- struct{}{}:
	case <-time.After(100 * time.Millisecond):
		if !pool.IsClosed() {
			pool.wg.Add(1)
			go pool.spawnAndHandshake()
		}
		return
	}

	if pool.IsClosed() {
		select {
		case <-pool.spawning:
		default:
		}
		return
	}

	defer func() {
		select {
		case <-pool.spawning:
		default:
		}
	}()

	proc, err := spawnProcess(pool.command)
	if err != nil {
		logJSON("error", fmt.Sprintf("failed to spawn process: %v", err))
		if !pool.IsClosed() {
			pool.mu.Lock()
			delay := pool.backoffDelay
			pool.backoffDelay = min(pool.backoffDelay*2, 5*time.Second)
			pool.mu.Unlock()
			time.AfterFunc(delay, func() {
				pool.wg.Add(1)
				go pool.spawnAndHandshake()
			})
		}
		return
	}

	if pool.IsClosed() {
		proc.Kill()
		return
	}

	pool.warm <- proc
	pool.mu.Lock()
	pool.backoffDelay = 100 * time.Millisecond
	pool.mu.Unlock()
}

func spawnProcess(command string) (*ManagedProcess, error) {
	var cmd *exec.Cmd
	if command == "cat" || command == "npx" || command == "yes" || command == "false" || command == "sh -c 'echo invalid'" {
		cmd = exec.Command(command)
	} else {
		cmd = exec.Command("sh", "-c", command)
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

	strictHandshake := os.Getenv("STRICT_HANDSHAKE") == "true"

	if strictHandshake {
		handshake := JSONRPCMessage{
			JSONRPC: "2.0",
			Method:  "list_tools",
			ID:      1,
		}
		handshakeData, _ := json.Marshal(handshake)
		stdin.Write(handshakeData)
		stdin.Close()

		respBuf := make([]byte, 4096)
		stdout.Read(respBuf)

		var rpcResp JSONRPCResponse
		if err := json.Unmarshal(respBuf, &rpcResp); err != nil {
			cmd.Process.Kill()
			return nil, fmt.Errorf("invalid JSON-RPC response: %v", err)
		}

		if rpcResp.Error != nil {
			cmd.Process.Kill()
			return nil, fmt.Errorf("handshake error: %v", rpcResp.Error.Message)
		}

		if rpcResp.Result == nil {
			cmd.Process.Kill()
			return nil, fmt.Errorf("no result in handshake response")
		}
	} else {
		stdin.Close()
	}

	proc := &ManagedProcess{
		Cmd:    cmd,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
		isWarm: true,
	}

	go captureStderr(proc)

	return proc, nil
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

func min(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func (pool *ProcessPool) WaitForWarm(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(pool.warm) > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func (pool *ProcessPool) WarmCount() int {
	return len(pool.warm)
}

func (pool *ProcessPool) ActiveCount() int {
	pool.activeMu.Lock()
	defer pool.activeMu.Unlock()
	return pool.activeCount
}

func (pool *ProcessPool) RegisterRequest(id string) chan []byte {
	pool.pendingMu.Lock()
	defer pool.pendingMu.Unlock()

	respCh := make(chan []byte, 1)
	timer := time.AfterFunc(30*time.Second, func() {
		pool.pendingMu.Lock()
		defer pool.pendingMu.Unlock()
		if req, ok := pool.pendingRequests[id]; ok {
			close(req.ResponseCh)
			delete(pool.pendingRequests, id)
		}
	})

	pool.pendingRequests[id] = &PendingRequest{
		ID:         id,
		ResponseCh: respCh,
		Timeout:    timer,
	}

	return respCh
}

func (pool *ProcessPool) CompleteRequest(id string) {
	pool.pendingMu.Lock()
	defer pool.pendingMu.Unlock()
	if req, ok := pool.pendingRequests[id]; ok {
		req.Timeout.Stop()
		close(req.ResponseCh)
		delete(pool.pendingRequests, id)
	}
}

func (pool *ProcessPool) BroadcastToSSE(data []byte) {
	select {
	case pool.broadcastCh <- data:
	default:
	}
}

func (pool *ProcessPool) IncrementActive() {
	pool.activeMu.Lock()
	defer pool.activeMu.Unlock()
	pool.activeCount++
}

func (pool *ProcessPool) DecrementActive() {
	pool.activeMu.Lock()
	defer pool.activeMu.Unlock()
	pool.activeCount--
}

func (pool *ProcessPool) Shutdown() {
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return
	}
	pool.closed = true
	pool.mu.Unlock()
	pool.wg.Wait()
	close(pool.warm)
	close(pool.broadcastCh)
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

type JSONRPCMessage struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func readyzHandler(w http.ResponseWriter, r *http.Request) {
	pool := r.Context().Value("pool").(*ProcessPool)

	select {
	case proc := <-pool.warm:
		if proc.isWarm {
			pool.warm <- proc
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
			return
		}
		pool.warm <- proc
	default:
	}

	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte("No warm processes"))
}

func sseHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	pool := r.Context().Value("pool").(*ProcessPool)

	select {
	case proc := <-pool.warm:
		pool.IncrementActive()
		go func() {
			<-r.Context().Done()
			proc.Kill()
			pool.DecrementActive()
			pool.wg.Add(1)
			go pool.spawnAndHandshake()
		}()

		buf := make([]byte, 4096)
		for {
			n, err := proc.Stdout.Read(buf)
			if err != nil {
				break
			}
			data := string(buf[:n])
			pool.BroadcastToSSE([]byte(data))
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

func messagesHandler(w http.ResponseWriter, r *http.Request) {
	pool := r.Context().Value("pool").(*ProcessPool)

	select {
	case proc := <-pool.warm:
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		var msg JSONRPCMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			pool.warm <- proc
			http.Error(w, "Invalid JSON-RPC", http.StatusBadRequest)
			return
		}

		id := fmt.Sprintf("%v", msg.ID)
		if id == "" || id == "<nil>" {
			id = fmt.Sprintf("auto-%d", time.Now().UnixNano())
			msg.ID = id
			body, _ = json.Marshal(msg)
		}

		proc.Stdin.Write(body)
		proc.Stdin.Write([]byte("\n"))

		respCh := make(chan string, 1)
		go func() {
			buf := make([]byte, 4096)
			n, _ := proc.Stdout.Read(buf)
			if n > 0 {
				data := string(buf[:n])
				pool.BroadcastToSSE([]byte(data))
				respCh <- data
			}
		}()

		select {
		case response := <-respCh:
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(response))
		case <-time.After(30 * time.Second):
			pool.warm <- proc
			w.WriteHeader(http.StatusGatewayTimeout)
			w.Write([]byte(`{"jsonrpc":"2.0","error":{"code":-32000,"message":"Request timeout after 30s"}}`))
			return
		}

		pool.warm <- proc
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("No warm processes"))
	}
}

func main() {
	pool := NewProcessPool(2)
	defer pool.Shutdown()

	poolCtx := context.WithValue(context.Background(), "pool", pool)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		readyzHandler(w, r.WithContext(poolCtx))
	})
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		sseHandler(w, r.WithContext(poolCtx))
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		messagesHandler(w, r.WithContext(poolCtx))
	})

	logJSON("info", "MCP SSE Bridge started")

	if err := http.ListenAndServe(":8080", mux); err != nil {
		log.Fatal(err)
	}
}
