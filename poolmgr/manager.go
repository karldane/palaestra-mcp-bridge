package poolmgr

import (
	"strings"
	"sync"
	"time"

	"github.com/mcp-bridge/mcp-bridge/shared"
)

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
// If the env has changed, the existing pool is shut down and recreated.
func (pm *PoolManager) GetOrCreateUserPool(backendID, userID, command string, minPoolSize, maxPoolSize int, env []string) *Pool {
	return pm.GetOrCreateUserPoolWithSecrets(backendID, userID, command, minPoolSize, maxPoolSize, env, nil)
}

// GetOrCreateUserPoolWithSecrets returns an existing pool keyed by "backendID:userID",
// or creates a new one with the given command, min/max pool sizes, environment, and secrets.
// If the env or secrets have changed, the existing pool is shut down and recreated.
func (pm *PoolManager) GetOrCreateUserPoolWithSecrets(backendID, userID, command string, minPoolSize, maxPoolSize int, env []string, secrets map[string]string) *Pool {
	key := backendID + ":" + userID

	pm.mu.RLock()
	if pool, ok := pm.pools[key]; ok {
		// Check if env has changed
		if pool.EnvMatches(env) {
			// For secrets, we need to check if the injector matches (for now, we'll recreate if secrets provided)
			if secrets == nil || pool.secretInjector != nil {
				pm.mu.RUnlock()
				pool.TouchLastUsed()
				return pool
			}
		}
		// Env changed or secrets mismatch - need to recreate
		pm.mu.RUnlock()
		shared.Infof("env/secrets changed for pool %s, shutting down and recreating", key)
		pool.Shutdown()

		pm.mu.Lock()
		delete(pm.pools, key)
		pm.mu.Unlock()
	} else {
		pm.mu.RUnlock()
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Double-check after upgrade.
	if pool, ok := pm.pools[key]; ok {
		if pool.EnvMatches(env) {
			// Check secrets compatibility
			if secrets == nil || pool.secretInjector != nil {
				pool.TouchLastUsed()
				return pool
			}
			// Another thread recreated with wrong env/secrets - shut it down
			pool.Shutdown()
			delete(pm.pools, key)
		}
	}

	// Ensure defaults
	if minPoolSize < 1 {
		minPoolSize = 1
	}
	if maxPoolSize < minPoolSize {
		maxPoolSize = minPoolSize
	}

	var pool *Pool
	if secrets != nil {
		// Create pool with secrets support
		pool = NewPoolWithSecrets(key, minPoolSize, maxPoolSize, command, env, "/run/secrets/mcp-bridge")
	} else {
		pool = NewPoolWithEnv(key, minPoolSize, maxPoolSize, command, env)
	}
	pool.SetDedicated(userID)
	pm.pools[key] = pool
	pool.TouchLastUsed()
	if secrets != nil {
		shared.Infof("created user pool with secrets %s (command=%s, min=%d, max=%d)", key, command, minPoolSize, maxPoolSize)
	} else {
		shared.Infof("created user pool %s (command=%s, min=%d, max=%d)", key, command, minPoolSize, maxPoolSize)
	}
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
			shared.Infof("idle GC: shutting down pool %s (idle %s)", key, now.Sub(pool.LastUsed()))
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
	BackendID     string
	UserID        string
	Command       string
	WarmCount     int
	CurrentSize   int
	MinPoolSize   int
	MaxPoolSize   int
	Unavailable   bool     // True if backend has exhausted spawn attempts
	MemoryBytes   uint64   // Total memory usage in bytes
	ProcessMemory []uint64 // Per-process memory in bytes
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
		status := PoolStatus{
			BackendID:   parts[0],
			UserID:      parts[1],
			Command:     pool.Command,
			WarmCount:   pool.GetWarmCount(),
			CurrentSize: pool.GetCurrentSize(),
			MinPoolSize: pool.MinPoolSize,
			MaxPoolSize: pool.MaxPoolSize,
			Unavailable: pool.IsUnavailable(),
		}
		// Get memory usage for all warm processes
		status.ProcessMemory = pool.GetProcessMemory()
		for _, mem := range status.ProcessMemory {
			status.MemoryBytes += mem
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// GetAllPools returns pool status for all pools (admin view)
func (pm *PoolManager) GetAllPools() []PoolStatus {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	var statuses []PoolStatus
	for key, pool := range pm.pools {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			continue
		}
		status := PoolStatus{
			BackendID:   parts[0],
			UserID:      parts[1],
			Command:     pool.Command,
			WarmCount:   pool.GetWarmCount(),
			CurrentSize: pool.GetCurrentSize(),
			MinPoolSize: pool.MinPoolSize,
			MaxPoolSize: pool.MaxPoolSize,
			Unavailable: pool.IsUnavailable(),
		}
		// Get memory usage for all warm processes
		status.ProcessMemory = pool.GetProcessMemory()
		for _, mem := range status.ProcessMemory {
			status.MemoryBytes += mem
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// ResetPool marks a pool as available again and triggers a new spawn attempt
func (pm *PoolManager) ResetPool(backendID, userID string) {
	key := backendID + ":" + userID
	pm.mu.RLock()
	pool, ok := pm.pools[key]
	pm.mu.RUnlock()

	if ok {
		pool.ResetSpawnAttempts()
	}
}
