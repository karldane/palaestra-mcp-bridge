package poolmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type Pool struct {
	Warm            chan *ManagedProcess
	mu              sync.Mutex
	Spawning        chan struct{}
	Command         string
	Env             []string
	EnvHash         string // Hash of env for comparison
	backoffDelay    time.Duration
	wg              sync.WaitGroup
	pendingRequests map[string]*PendingRequest
	pendingMu       sync.Mutex
	broadcastCh     chan []byte
	broadcastMu     sync.Mutex
	closed          bool
	BackendID       string
	MinPoolSize     int // Minimum warm processes to maintain
	MaxPoolSize     int // Maximum warm processes (0 = unlimited)
	CurrentSize     int // Current number of warm + spawning processes
	activeCount     int // Number of processes currently in use
	activeMu        sync.Mutex
	dedicatedUser   string
	lastUsed        time.Time
	lastUsedMu      sync.Mutex
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
	// Ensure channel capacity is at least maxPoolSize
	channelSize := maxPoolSize
	if channelSize < 1 {
		channelSize = 1
	}
	pool := &Pool{
		Warm:            make(chan *ManagedProcess, channelSize),
		Spawning:        make(chan struct{}, 1),
		Command:         command,
		Env:             env,
		EnvHash:         hashEnv(env),
		backoffDelay:    100 * time.Millisecond,
		pendingRequests: make(map[string]*PendingRequest),
		broadcastCh:     make(chan []byte, 100),
		BackendID:       backendID,
		lastUsed:        time.Now(),
		MinPoolSize:     minPoolSize,
		MaxPoolSize:     maxPoolSize,
		CurrentSize:     0,
	}

	// Spawn initial processes up to minPoolSize
	for i := 0; i < minPoolSize; i++ {
		pool.wg.Add(1)
		go pool.spawnAndHandshake()
	}

	go pool.responseDispatcher()

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

// EnvMatches checks if the given env matches the pool's current env
func (pool *Pool) EnvMatches(env []string) bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.EnvHash == hashEnv(env)
}

func (pool *Pool) responseDispatcher() {
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
		pool.mu.Lock()
		pool.CurrentSize++
		pool.mu.Unlock()
		pool.Warm <- proc
	} else {
		// Handshake failed - don't add to warm pool, process will be cleaned up
		proc.Kill()
		// Retry spawning after a delay
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
	buf.WriteByte('\n')

	respCh := pool.RegisterRequest(initID)

	proc.Stdin.Write(buf.Bytes())

	select {
	case <-respCh:
		pool.UnregisterRequest(initID)
	case <-time.After(500 * time.Millisecond):
		pool.UnregisterRequest(initID)
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
	fmt.Printf("[DEBUG WaitForWarm] backend=%s, timeout=%v, current warm count=%d\n", pool.BackendID, timeout, len(pool.Warm))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if len(pool.Warm) > 0 {
			fmt.Printf("[DEBUG WaitForWarm] backend=%s, found warm process\n", pool.BackendID)
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Printf("[DEBUG WaitForWarm] backend=%s, timed out waiting for warm\n", pool.BackendID)
	return false
}

// WaitForWarmWithMax tries to acquire a warm process. If MaxPoolSize is set and all
// processes are currently in use (CurrentSize >= MaxPoolSize), it returns immediately
// with an error indicating max is reached. Otherwise it waits up to the timeout for
// a process to become available.
func (pool *Pool) WaitForWarmWithMax(timeout time.Duration) (*ManagedProcess, error) {
	maxPoolSize := pool.MaxPoolSize

	// If we have a max and we're at or above it, check if there's a process available
	if maxPoolSize > 0 {
		currentSize := pool.GetCurrentSize()
		if currentSize >= maxPoolSize {
			// All processes are busy - try to get one anyway (may have been released)
			select {
			case proc := <-pool.Warm:
				return proc, nil
			default:
				// All processes are definitely busy and we're at max - return error
				return nil, fmt.Errorf("max_pool_size reached: %d/%d processes busy", currentSize, maxPoolSize)
			}
		}
	}

	// Try to get a process without waiting
	select {
	case proc := <-pool.Warm:
		return proc, nil
	default:
	}

	// Wait for a process to become available
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case proc := <-pool.Warm:
			return proc, nil
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("timeout waiting for warm process")
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
		pool.Warm <- proc
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
