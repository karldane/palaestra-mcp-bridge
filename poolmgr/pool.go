package poolmgr

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
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

type Pool struct {
	Warm            chan *ManagedProcess
	mu              sync.Mutex
	Spawning        chan struct{}
	Command         string
	Env             []string // explicit environment for spawned processes; nil = inherit bridge env
	closed          bool
	wg              sync.WaitGroup
	backoffDelay    time.Duration
	activeCount     int
	activeMu        sync.Mutex
	pendingMu       sync.Mutex
	pendingRequests map[string]*PendingRequest
	broadcastCh     chan []byte
	broadcastMu     sync.Mutex
	BackendID       string
	DedicatedFor    string
	lastUsed        time.Time
	lastUsedMu      sync.Mutex
	MinPoolSize     int // Minimum warm processes to maintain
	MaxPoolSize     int // Maximum warm processes (0 = unlimited)
	CurrentSize     int // Current number of warm processes
}

func NewPool(backendID string, size int, command string) *Pool {
	// Use size as both min and max (0 means unlimited, so we use size for max)
	return NewPoolWithEnv(backendID, size, size, command, nil)
}

// NewPoolWithEnv creates a pool that spawns processes with the given explicit
// environment. If env is nil, spawned processes inherit the bridge process env.
// minPoolSize: minimum warm processes to maintain
// maxPoolSize: maximum warm processes (0 = unlimited)
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
	pool.DedicatedFor = userID
}

func (pool *Pool) IsDedicated() bool {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.DedicatedFor != ""
}

func (pool *Pool) DedicatedUser() string {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	return pool.DedicatedFor
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

// SpawnProcess starts a new child process with the given command. If env is
// non-nil, cmd.Env is set explicitly (the child does NOT inherit the bridge
// environment); if env is nil, the child inherits the bridge's environment.
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

func logJSON(level, message string) {
	entry := LogEntry{
		Level:   level,
		Message: message,
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(entry)
	fmt.Println(string(data))
}

type PoolManager struct {
	mu          sync.RWMutex
	pools       map[string]*Pool
	command     string
	poolSize    int
	idleTimeout time.Duration // 0 = no idle GC
	gcStop      chan struct{}
}

// DefaultIdleTimeout is the default idle timeout for user pools.
const DefaultIdleTimeout = 15 * time.Minute

func NewPoolManager(command string, poolSize int) *PoolManager {
	return &PoolManager{
		pools:       make(map[string]*Pool),
		command:     command,
		poolSize:    poolSize,
		idleTimeout: 0, // disabled by default for backward compat
	}
}

// NewPoolManagerWithGC creates a PoolManager with idle garbage collection.
// Pools that have not been used for idleTimeout are shut down and removed.
func NewPoolManagerWithGC(command string, poolSize int, idleTimeout time.Duration) *PoolManager {
	pm := &PoolManager{
		pools:       make(map[string]*Pool),
		command:     command,
		poolSize:    poolSize,
		idleTimeout: idleTimeout,
		gcStop:      make(chan struct{}),
	}
	if idleTimeout > 0 {
		go pm.gcLoop()
	}
	return pm
}

func (pm *PoolManager) GetPool(backendID string) *Pool {
	pm.mu.RLock()
	if pool, ok := pm.pools[backendID]; ok {
		pm.mu.RUnlock()
		return pool
	}
	pm.mu.RUnlock()

	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pool, ok := pm.pools[backendID]; ok {
		return pool
	}

	pool := NewPool(backendID, pm.poolSize, pm.command)
	pm.pools[backendID] = pool
	return pool
}

func (pm *PoolManager) GetOrCreatePool(backendID, command string, poolSize int) *Pool {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pool, ok := pm.pools[backendID]; ok {
		return pool
	}

	pool := NewPool(backendID, poolSize, command)
	pm.pools[backendID] = pool
	return pool
}

// GetOrCreateUserPool returns an existing pool keyed by "backendID:userID",
// or creates a new one with the given command, min/max pool sizes, and environment.
func (pm *PoolManager) GetOrCreateUserPool(backendID, userID, command string, minPoolSize, maxPoolSize int, env []string) *Pool {
	key := backendID + ":" + userID

	pm.mu.RLock()
	if pool, ok := pm.pools[key]; ok {
		pm.mu.RUnlock()
		pool.TouchLastUsed()
		return pool
	}
	pm.mu.RUnlock()

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Double-check after upgrade.
	if pool, ok := pm.pools[key]; ok {
		pool.TouchLastUsed()
		return pool
	}

	// Ensure defaults
	if minPoolSize < 1 {
		minPoolSize = 1
	}
	if maxPoolSize < minPoolSize {
		maxPoolSize = minPoolSize
	}

	pool := NewPoolWithEnv(key, minPoolSize, maxPoolSize, command, env)
	pool.SetDedicated(userID)
	pm.pools[key] = pool
	pool.TouchLastUsed()
	logJSON("info", fmt.Sprintf("created user pool %s (command=%s, min=%d, max=%d)", key, command, minPoolSize, maxPoolSize))
	return pool
}

func (pm *PoolManager) RemovePool(backendID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pool, ok := pm.pools[backendID]; ok {
		pool.Shutdown()
		delete(pm.pools, backendID)
	}
}

// RemovePoolsByBackend shuts down and removes all pools whose key equals
// backendID or starts with "backendID:" (i.e. per-user pools keyed as
// "backendID:userID"). Call this when a backend is edited or deleted so
// stale pools are torn down and recreated with fresh config on next use.
func (pm *PoolManager) RemovePoolsByBackend(backendID string) int {
	prefix := backendID + ":"
	pm.mu.Lock()
	defer pm.mu.Unlock()

	removed := 0
	for key, pool := range pm.pools {
		if key == backendID || strings.HasPrefix(key, prefix) {
			pool.Shutdown()
			delete(pm.pools, key)
			removed++
		}
	}
	return removed
}

func (pm *PoolManager) ShutdownAll() {
	// Stop GC goroutine if running.
	if pm.gcStop != nil {
		select {
		case <-pm.gcStop:
			// Already closed.
		default:
			close(pm.gcStop)
		}
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	for _, pool := range pm.pools {
		pool.Shutdown()
	}
	pm.pools = make(map[string]*Pool)
}

// gcLoop periodically scans for idle user pools and shuts them down.
func (pm *PoolManager) gcLoop() {
	ticker := time.NewTicker(pm.idleTimeout / 2)
	defer ticker.Stop()

	for {
		select {
		case <-pm.gcStop:
			return
		case <-ticker.C:
			pm.collectIdlePools()
		}
	}
}

// collectIdlePools shuts down and removes pools that have been idle longer
// than pm.idleTimeout. Only dedicated (per-user) pools are collected;
// non-dedicated pools are left alone.
func (pm *PoolManager) collectIdlePools() {
	now := time.Now()
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for key, pool := range pm.pools {
		if !pool.IsDedicated() {
			continue
		}
		if now.Sub(pool.LastUsed()) > pm.idleTimeout {
			logJSON("info", fmt.Sprintf("idle GC: shutting down pool %s (idle %s)", key, now.Sub(pool.LastUsed())))
			pool.Shutdown()
			delete(pm.pools, key)
		}
	}
}

func (pm *PoolManager) PoolCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.pools)
}

type PoolStatus struct {
	BackendID   string
	UserID      string
	Command     string
	WarmCount   int
	CurrentSize int
	MinPoolSize int
	MaxPoolSize int
}

func (pm *PoolManager) GetPoolsForUser(userID string) []PoolStatus {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var statuses []PoolStatus
	for key, pool := range pm.pools {
		// Key format: backendID:userID
		if !strings.HasSuffix(key, ":"+userID) {
			continue
		}
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		statuses = append(statuses, PoolStatus{
			BackendID:   parts[0],
			UserID:      parts[1],
			Command:     pool.Command,
			WarmCount:   pool.GetWarmCount(),
			CurrentSize: pool.GetCurrentSize(),
			MinPoolSize: pool.MinPoolSize,
			MaxPoolSize: pool.MaxPoolSize,
		})
	}
	return statuses
}

type SafetyRecycler struct {
	mu            sync.RWMutex
	poolManager   *PoolManager
	dedicated     map[string]map[string]*ManagedProcess
	recyclePolicy RecyclePolicy
}

type RecyclePolicy string

const (
	RecyclePolicyAlways    RecyclePolicy = "always"
	RecyclePolicyDedicated RecyclePolicy = "dedicated"
	RecyclePolicyOnError   RecyclePolicy = "on-error"
)

func NewSafetyRecycler(pm *PoolManager) *SafetyRecycler {
	return &SafetyRecycler{
		poolManager:   pm,
		dedicated:     make(map[string]map[string]*ManagedProcess),
		recyclePolicy: RecyclePolicyDedicated,
	}
}

func (sr *SafetyRecycler) AcquireProcess(backendID, userID string) *ManagedProcess {
	pm := sr.poolManager
	pool := pm.GetPool(backendID)

	select {
	case proc := <-pool.Warm:
		proc.UserID = userID
		pool.IncrementActive()

		if sr.recyclePolicy == RecyclePolicyDedicated {
			sr.mu.Lock()
			if sr.dedicated[backendID] == nil {
				sr.dedicated[backendID] = make(map[string]*ManagedProcess)
			}
			sr.dedicated[backendID][userID] = proc
			sr.mu.Unlock()
		}

		return proc
	default:
		return nil
	}
}

func (sr *SafetyRecycler) ReleaseProcess(backendID, userID string, proc *ManagedProcess, err error) {
	pm := sr.poolManager
	pool := pm.GetPool(backendID)

	shouldRecycle := sr.shouldRecycle(err)

	if shouldRecycle {
		proc.Kill()
		proc.mu.Lock()
		close(proc.LineChan)
		proc.mu.Unlock()

		pool.wg.Add(1)
		go pool.spawnAndHandshake()
	} else {
		if sr.recyclePolicy == RecyclePolicyDedicated {
			sr.mu.Lock()
			if sr.dedicated[backendID] != nil {
				delete(sr.dedicated[backendID], userID)
			}
			sr.mu.Unlock()
		}

		pool.Warm <- proc
	}

	pool.DecrementActive()
}

func (sr *SafetyRecycler) shouldRecycle(err error) bool {
	switch sr.recyclePolicy {
	case RecyclePolicyAlways:
		return true
	case RecyclePolicyOnError:
		return err != nil
	case RecyclePolicyDedicated:
		return false
	default:
		return false
	}
}

func (sr *SafetyRecycler) SetRecyclePolicy(policy RecyclePolicy) {
	sr.mu.Lock()
	defer sr.mu.Unlock()
	sr.recyclePolicy = policy
}

func (sr *SafetyRecycler) GetDedicatedProcess(backendID, userID string) *ManagedProcess {
	sr.mu.RLock()
	defer sr.mu.RUnlock()

	if sr.dedicated[backendID] != nil {
		return sr.dedicated[backendID][userID]
	}
	return nil
}

func (sr *SafetyRecycler) ReleaseDedicated(backendID, userID string) {
	sr.mu.Lock()
	defer sr.mu.Unlock()

	if proc, ok := sr.dedicated[backendID][userID]; ok {
		proc.Kill()
		delete(sr.dedicated[backendID], userID)
	}
}

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
