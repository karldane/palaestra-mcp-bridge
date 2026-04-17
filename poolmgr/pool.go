package poolmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mcp-bridge/mcp-bridge/internal/secret"
	"github.com/mcp-bridge/mcp-bridge/shared"
)

const (
	protocolVersion         = "2024-11-05"
	handshakeTimeout        = 30 * time.Second // Timeout for MCP handshake during process initialization (npx downloads can be slow)
	defaultSpawnChannel     = 10               // Default spawning concurrency per pool
	defaultMaxSpawnAttempts = 3                // Max failed spawn attempts before marking backend unavailable (0 = unlimited)
	DefaultWarmWaitTimeout  = 30 * time.Second // Default timeout for waiting for warm process when none available
)

type Pool struct {
	Warm             chan *ManagedProcess
	mu               sync.Mutex
	Spawning         chan struct{}
	Command          string
	Env              []string
	EnvHash          string // Hash of env for comparison
	backoffDelay     time.Duration
	wg               sync.WaitGroup
	pendingRequests  map[string]*PendingRequest
	pendingMu        sync.Mutex
	broadcastCh      chan []byte
	broadcastMu      sync.Mutex
	closed           bool
	BackendID        string
	MinPoolSize      int // Minimum warm processes to maintain
	MaxPoolSize      int // Maximum warm processes (0 = unlimited)
	CurrentSize      int // Current number of warm + spawning processes
	activeCount      int // Number of processes currently in use
	activeMu         sync.Mutex
	dedicatedUser    string
	lastUsed         time.Time
	lastUsedMu       sync.Mutex
	Instructions     string // Instructions from backend's initialize response
	instructionsMu   sync.Mutex
	spawnAttempts    int // Number of consecutive failed spawn attempts
	maxSpawnAttempts int // Max attempts before giving up (0 = unlimited)
	secretInjector   secret.SecretInjector
	secretsPath      string
}

func NewPool(backendID string, size int, command string) *Pool {
	// Use size as both min and max (0 means unlimited, so we use size for max)
	return NewPoolWithEnv(backendID, size, size, command, nil)
}

// NewPoolWithEnv creates a pool that spawns processes with the given explicit
// environment. If env is nil, spawned processes inherit the bridge process env.
// minPoolSize: minimum warm processes to maintain
// maxPoolSize: maximum warm processes (0 = unlimited)
func hashEnv(env []string) string {
	// Sort env vars to ensure consistent hashing regardless of order
	sorted := make([]string, len(env))
	copy(sorted, env)
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i] > sorted[j] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	// Simple hash of sorted env vars
	h := ""
	for _, e := range sorted {
		h += e + "|"
	}
	return h
}

func NewPoolWithEnv(backendID string, minPoolSize, maxPoolSize int, command string, env []string) *Pool {
	// If maxPoolSize is 0, set it to minPoolSize (no dynamic scaling)
	if maxPoolSize == 0 {
		maxPoolSize = minPoolSize
	}
	// Ensure channel capacity is at least maxPoolSize for warm pool
	warmChannelSize := maxPoolSize
	if warmChannelSize < 1 {
		warmChannelSize = 1
	}
	// Spawning channel controls concurrent spawn attempts (default 10)
	spawnChannelSize := defaultSpawnChannel
	pool := &Pool{
		Warm:             make(chan *ManagedProcess, warmChannelSize),
		Spawning:         make(chan struct{}, spawnChannelSize),
		Command:          command,
		Env:              env,
		EnvHash:          hashEnv(env),
		backoffDelay:     100 * time.Millisecond,
		pendingRequests:  make(map[string]*PendingRequest),
		broadcastCh:      make(chan []byte, 100),
		BackendID:        backendID,
		lastUsed:         time.Now(),
		MinPoolSize:      minPoolSize,
		MaxPoolSize:      maxPoolSize,
		CurrentSize:      0,
		spawnAttempts:    0,
		maxSpawnAttempts: defaultMaxSpawnAttempts,
	}

	// Spawn initial processes up to minPoolSize
	for i := 0; i < minPoolSize; i++ {
		pool.wg.Add(1)
		go pool.spawnAndHandshake()
	}

	go pool.responseDispatcher()

	return pool
}

func NewPoolWithSecrets(backendID string, minPoolSize, maxPoolSize int, command string, env []string, secretsPath string) *Pool {
	pool := NewPoolWithEnv(backendID, minPoolSize, maxPoolSize, command, env)
	pool.secretsPath = secretsPath
	pool.secretInjector = secret.NewFileSecretInjector(secretsPath)
	return pool
}

func (pool *Pool) IsClosed() bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.closed
}

func (pool *Pool) SetDedicated(userID string) {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.dedicatedUser = userID
}

func (pool *Pool) IsDedicated() bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.dedicatedUser != ""
}

func (pool *Pool) DedicatedUser() string {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.dedicatedUser
}

// IsUnavailable returns true if the pool has exhausted its spawn attempts
func (pool *Pool) IsUnavailable() bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.maxSpawnAttempts == 0 {
		return false // Unlimited retries means never unavailable
	}
	return pool.spawnAttempts >= pool.maxSpawnAttempts
}

// ResetSpawnAttempts resets the spawn attempt counter and triggers a new spawn attempt
// Use this to retry an unavailable backend
func (pool *Pool) ResetSpawnAttempts() {
	pool.mu.Lock()
	reset := pool.spawnAttempts >= pool.maxSpawnAttempts
	pool.spawnAttempts = 0
	pool.mu.Unlock()

	if reset {
		shared.Infof("Pool %s: reset spawn attempts, triggering retry", pool.BackendID)
		pool.wg.Add(1)
		go pool.spawnAndHandshake()
	}
}

// ForceReconnect resets spawn attempts and triggers a new spawn attempt.
// Use this when user tries to use an unavailable backend to attempt immediate reconnect.
func (pool *Pool) ForceReconnect() {
	pool.ResetSpawnAttempts()
}

// EnvMatches checks if the given env matches the pool's current env
func (pool *Pool) EnvMatches(env []string) bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.EnvHash == hashEnv(env)
}

func (pool *Pool) responseDispatcher() {
	for data := range pool.broadcastCh {
		// Log truncated version to avoid flooding logs with large responses
		const maxLogLen = 200
		logData := string(data)
		if len(logData) > maxLogLen {
			logData = logData[:maxLogLen] + "...[truncated]"
		}
		shared.Debugf("responseDispatcher received data: %s", logData)
		pool.pendingMu.Lock()
		var resp JSONRPCResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			shared.Debugf("responseDispatcher failed to unmarshal: %v", err)
		} else if resp.ID != nil {
			id := fmt.Sprintf("%v", resp.ID)
			shared.Debugf("responseDispatcher looking for request id=%s, pending=%d", id, len(pool.pendingRequests))
			if req, ok := pool.pendingRequests[id]; ok {
				shared.Debugf("responseDispatcher found request, sending to channel")
				select {
				case req.ResponseCh <- data:
					shared.Debugf("responseDispatcher sent response to channel for id=%s", id)
				default:
					shared.Debugf("responseDispatcher channel full for id=%s", id)
				}
			} else {
				shared.Debugf("responseDispatcher no pending request for id=%s", id)
			}
		} else {
			shared.Debugf("responseDispatcher received notification (no id)")
		}
		pool.pendingMu.Unlock()
	}
}

func (pool *Pool) spawnAndHandshake() {
	defer pool.wg.Done()

	if pool.IsClosed() {
		return
	}

	select {
	case pool.Spawning <- struct{}{}:
	case <-time.After(100 * time.Millisecond):
		if !pool.IsClosed() {
			pool.wg.Add(1)
			go pool.spawnAndHandshake()
		}
		return
	}

	if pool.IsClosed() {
		select {
		case <-pool.Spawning:
		default:
		}
		return
	}

	defer func() {
		select {
		case <-pool.Spawning:
		default:
		}
	}()

	proc, err := SpawnProcess(pool, pool.Command, pool.Env)
	if err != nil {
		shared.Errorf("failed to spawn process: %v", err)
		if !pool.IsClosed() {
			pool.mu.Lock()
			pool.spawnAttempts++
			delay := pool.backoffDelay
			pool.backoffDelay = min(pool.backoffDelay*2, 5*time.Second)
			if pool.maxSpawnAttempts > 0 && pool.spawnAttempts >= pool.maxSpawnAttempts {
				shared.Errorf("Pool %s: spawn failed %d times - marking backend unavailable", pool.BackendID, pool.spawnAttempts)
				pool.mu.Unlock()
				return
			}
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

	// Skip MCP handshake for simple commands that don't speak MCP
	if isSimpleCommand(pool.Command) {
		pool.mu.Lock()
		pool.CurrentSize++
		pool.mu.Unlock()
		pool.Warm <- proc
		pool.mu.Lock()
		pool.backoffDelay = 100 * time.Millisecond
		pool.mu.Unlock()
		return
	}

	// Run MCP handshake FIRST, before marking as warm
	// This ensures WaitForWarm only returns processes that have completed handshake
	if pool.performMCPHandshake(proc) {
		// Small delay to allow server to fully initialize after handshake
		time.Sleep(100 * time.Millisecond)
		pool.mu.Lock()
		pool.CurrentSize++
		pool.mu.Unlock()
		pool.Warm <- proc
	} else {
		// Handshake failed - capture stderr and don't add to warm pool
		proc.mu.Lock()
		stderrOutput := proc.StderrBuf.String()
		proc.mu.Unlock()

		// Check if process exited immediately (likely config error)
		if proc.Cmd.ProcessState != nil && proc.Cmd.ProcessState.Exited() {
			if stderrOutput != "" {
				shared.Errorf("MCP handshake failed for backend %s - process exited immediately with error: %s", pool.BackendID, strings.TrimSpace(stderrOutput))
			} else {
				shared.Errorf("MCP handshake failed for backend %s - process exited immediately (no stderr output)", pool.BackendID)
			}
			proc.Kill()
			// Don't retry indefinitely for immediate exit errors
			// Reset backoff for next successful startup
			pool.mu.Lock()
			pool.backoffDelay = 100 * time.Millisecond
			pool.mu.Unlock()
			return
		}

		// Process didn't exit immediately - handshake timeout (process hanging)
		proc.Kill()

		// Log stderr for debugging
		if stderrOutput != "" {
			shared.Errorf("MCP handshake timeout for backend %s - stderr: %s", pool.BackendID, strings.TrimSpace(stderrOutput))
		} else {
			shared.Errorf("MCP handshake timeout for backend %s - no stderr output (process may be downloading or waiting for auth)", pool.BackendID)
		}

		// Track failed attempts
		pool.mu.Lock()
		pool.spawnAttempts++
		if pool.maxSpawnAttempts > 0 && pool.spawnAttempts >= pool.maxSpawnAttempts {
			shared.Errorf("MCP handshake failed for backend %s after %d attempts - stopping retry (backend unavailable)", pool.BackendID, pool.spawnAttempts)
			pool.mu.Unlock()
			return
		}

		shared.Errorf("MCP handshake failed for backend %s (attempt %d), retrying...", pool.BackendID, pool.spawnAttempts)
		delay := pool.backoffDelay
		pool.backoffDelay = min(pool.backoffDelay*2, 5*time.Second)
		pool.mu.Unlock()

		if !pool.IsClosed() {
			time.AfterFunc(delay, func() {
				pool.wg.Add(1)
				go pool.spawnAndHandshake()
			})
		}
		return
	}

	pool.mu.Lock()
	// Reset counters on successful spawn
	pool.spawnAttempts = 0
	pool.backoffDelay = 100 * time.Millisecond
	pool.mu.Unlock()
	// Small delay to allow server to fully initialize after handshake
	time.Sleep(100 * time.Millisecond)

	pool.mu.Lock()
	pool.backoffDelay = 100 * time.Millisecond
	pool.mu.Unlock()
}

func (pool *Pool) performMCPHandshake(proc *ManagedProcess) bool {
	initID := fmt.Sprintf("init-%d", time.Now().UnixNano())

	initMsg := JSONRPCMessage{
		JSONRPC: "2.0",
		ID:      initID,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "mcp-bridge",
				"version": "1.0.0",
			},
		},
	}

	initBody, err := json.Marshal(initMsg)
	if err != nil {
		return false
	}

	buf := new(bytes.Buffer)
	if err := json.Compact(buf, initBody); err != nil {
		buf.Write(initBody)
	}

	respCh := pool.RegisterRequest(initID)

	buf.WriteByte('\n')
	proc.Stdin.Write(buf.Bytes())

	select {
	case resp := <-respCh:
		pool.UnregisterRequest(initID)
		// Extract instructions from initialize response
		if len(resp) > 0 {
			var initResult struct {
				Result struct {
					Instructions string `json:"instructions"`
				} `json:"result"`
			}
			if err := json.Unmarshal(resp, &initResult); err == nil {
				if initResult.Result.Instructions != "" {
					pool.instructionsMu.Lock()
					pool.Instructions = initResult.Result.Instructions
					pool.instructionsMu.Unlock()
					preview := initResult.Result.Instructions
					if len(preview) > 100 {
						preview = preview[:100] + "..."
					}
					shared.Debugf("Pool %s captured instructions: %s", pool.BackendID, preview)
				}
			}
		}
	case <-time.After(handshakeTimeout):
		pool.UnregisterRequest(initID)
		shared.Errorf("MCP handshake timeout for backend %s after %v", pool.BackendID, handshakeTimeout)
		return false
	}

	notifMsg := JSONRPCMessage{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}

	notifBody, _ := json.Marshal(notifMsg)
	buf.Reset()
	if err := json.Compact(buf, notifBody); err != nil {
		buf.Write(notifBody)
	}
	buf.WriteByte('\n')
	proc.Stdin.Write(buf.Bytes())

	return true
}

// GetInstructions returns the instructions captured from the backend's initialize response.
func (pool *Pool) GetInstructions() string {
	pool.instructionsMu.Lock()
	defer pool.instructionsMu.Unlock()
	return pool.Instructions
}

func (pool *Pool) IsReady() bool {
	select {
	case proc := <-pool.Warm:
		if proc.isWarm {
			pool.Warm <- proc
			return true
		}
		pool.Warm <- proc
	default:
	}
	return false
}

func (pool *Pool) WaitForWarm(timeout time.Duration) bool {
	shared.Debugf("WaitForWarm: backend=%s, timeout=%v, current warm count=%d", pool.BackendID, timeout, len(pool.Warm))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(pool.Warm) > 0 {
			shared.Debugf("WaitForWarm: backend=%s, found warm process", pool.BackendID)
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	shared.Debugf("WaitForWarm: backend=%s, timed out waiting for warm", pool.BackendID)
	return false
}

// WaitForWarmWithMax tries to acquire a warm process, blocking up to the timeout
// if none is immediately available. If the pool is unavailable (circuit breaker open),
// it returns immediately with a "backend unavailable" error.
// Unlike the previous implementation, this method BLOCKS when all processes are in
// use rather than returning an error — callers should wait for a process returned by
// another request completing and released back to the warm pool.
func (pool *Pool) WaitForWarmWithMax(timeout time.Duration) (*ManagedProcess, error) {
	if pool.IsUnavailable() {
		return nil, fmt.Errorf("backend unavailable: %s", pool.BackendID)
	}

	if proc := pool.TryAcquireWarm(); proc != nil {
		return proc, nil
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pool.IsUnavailable() {
			return nil, fmt.Errorf("backend unavailable: %s", pool.BackendID)
		}
		select {
		case proc := <-pool.Warm:
			if !proc.IsAlive() {
				shared.Warnf("WaitForWarmWithMax: process %d for %s died unexpectedly, discarding", proc.Cmd.Process.Pid, pool.BackendID)
				proc.Kill()
				continue
			}
			pool.mu.Lock()
			pool.CurrentSize--
			pool.mu.Unlock()
			return proc, nil
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}

	if pool.IsUnavailable() {
		return nil, fmt.Errorf("backend unavailable: %s", pool.BackendID)
	}
	return nil, fmt.Errorf("timeout waiting for warm process")
}

// GetWarmWithRetry attempts to get a warm process, falling back to spawning if none available.
// This is the preferred method for handling "no warm processes" scenarios - it waits for a
// process to become ready rather than immediately returning an error.
// Returns the process, or an error if timeout exceeded or backend is unavailable.
func (pool *Pool) GetWarmWithRetry(timeout time.Duration) (*ManagedProcess, error) {
	if timeout == 0 {
		timeout = DefaultWarmWaitTimeout
	}

	if proc := pool.TryAcquireWarm(); proc != nil {
		return proc, nil
	}

	if !pool.IsUnavailable() && !pool.IsSpawning() {
		shared.Debugf("GetWarmWithRetry: no warm processes, triggering spawn for %s", pool.BackendID)
		pool.wg.Add(1)
		go pool.spawnAndHandshake()
	}

	return pool.WaitForWarmWithMax(timeout)
}

func (pool *Pool) WarmCount() int {
	return len(pool.Warm)
}

func (pool *Pool) ActiveCount() int {
	pool.activeMu.Lock()
	defer pool.activeMu.Unlock()
	return pool.activeCount
}

func (pool *Pool) RegisterRequest(id string) chan []byte {
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

func (pool *Pool) UnregisterRequest(id string) {
	pool.pendingMu.Lock()
	defer pool.pendingMu.Unlock()
	if req, ok := pool.pendingRequests[id]; ok {
		req.Timeout.Stop()
		delete(pool.pendingRequests, id)
	}
}

func (pool *Pool) BroadcastToSSE(data []byte) {
	pool.broadcastMu.Lock()
	defer pool.broadcastMu.Unlock()

	select {
	case pool.broadcastCh <- data:
	default:
	}
}

func (pool *Pool) IncrementActive() {
	pool.activeMu.Lock()
	defer pool.activeMu.Unlock()
	pool.activeCount++
}

func (pool *Pool) DecrementActive() {
	pool.activeMu.Lock()
	defer pool.activeMu.Unlock()
	pool.activeCount--
}

func (pool *Pool) WarmChan() chan *ManagedProcess {
	return pool.Warm
}

func (pool *Pool) BroadcastChan() chan []byte {
	return pool.broadcastCh
}

func (pool *Pool) Wg() *sync.WaitGroup {
	return &pool.wg
}

func (pool *Pool) SpawnAndHandshake() {
	pool.spawnAndHandshake()
}

func (pool *Pool) TryAcquireWarm() *ManagedProcess {
	select {
	case proc := <-pool.Warm:
		if !proc.IsAlive() {
			shared.Warnf("TryAcquireWarm: process %d for %s died unexpectedly, discarding", proc.Cmd.Process.Pid, pool.BackendID)
			proc.Kill()
			return nil
		}
		pool.mu.Lock()
		pool.CurrentSize--
		pool.mu.Unlock()
		return proc
	default:
		return nil
	}
}

func (pool *Pool) ReleaseWarm(proc *ManagedProcess) {
	if proc != nil {
		pool.mu.Lock()
		pool.CurrentSize++
		pool.mu.Unlock()
		select {
		case pool.Warm <- proc:
		default:
			// Channel full - process is lost, but don't block
			shared.Debugf("ReleaseWarm: warm channel full, discarding process for %s", pool.BackendID)
			proc.Kill()
		}
	}
}

func (pool *Pool) SetWarm(proc *ManagedProcess) {
	pool.mu.Lock()
	pool.CurrentSize++
	pool.mu.Unlock()
	pool.Warm <- proc
}

func (pool *Pool) GetWarmCount() int {
	return len(pool.Warm)
}

func (pool *Pool) GetCurrentSize() int {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.CurrentSize
}

func (pool *Pool) IsSpawning() bool {
	select {
	case pool.Spawning <- struct{}{}:
		<-pool.Spawning
		return false
	default:
		return true
	}
}

func (pool *Pool) Shutdown() {
	pool.mu.Lock()
	if pool.closed {
		pool.mu.Unlock()
		return
	}
	pool.closed = true
	pool.mu.Unlock()
	pool.wg.Wait()
	close(pool.Warm)
	pool.broadcastMu.Lock()
	close(pool.broadcastCh)
	pool.broadcastMu.Unlock()
}

// TouchLastUsed updates the pool's last-used timestamp (for idle GC).
func (pool *Pool) TouchLastUsed() {
	pool.lastUsedMu.Lock()
	pool.lastUsed = time.Now()
	pool.lastUsedMu.Unlock()
}

// LastUsed returns the time this pool was last used.
func (pool *Pool) LastUsed() time.Time {
	pool.lastUsedMu.Lock()
	defer pool.lastUsedMu.Unlock()
	return pool.lastUsed
}

// GetProcessMemory returns memory usage for all warm processes in the pool
func (pool *Pool) GetProcessMemory() []uint64 {
	pool.mu.Lock()
	defer pool.mu.Unlock()

	var memory []uint64
	// We need to temporarily drain the channel to check all processes
	var processes []*ManagedProcess
	for {
		select {
		case proc := <-pool.Warm:
			processes = append(processes, proc)
		default:
			goto done
		}
	}
done:
	// Get memory for each process and put them back
	for _, proc := range processes {
		if mem, err := proc.GetMemoryUsage(); err == nil {
			memory = append(memory, mem)
		}
		pool.Warm <- proc
	}
	return memory
}
