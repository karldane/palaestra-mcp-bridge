package poolmgr

import (
	"sync"
	"testing"
	"time"
)

func TestNewPool_CreatesPool(t *testing.T) {
	pool := NewPool("test-backend", 1, "cat")
	defer pool.Shutdown()

	if pool.Command != "cat" {
		t.Errorf("expected command cat, got %s", pool.Command)
	}
	if pool.BackendID != "test-backend" {
		t.Errorf("expected backendID test-backend, got %s", pool.BackendID)
	}
}

func TestPool_WaitForWarm(t *testing.T) {
	pool := NewPool("test", 1, "cat")
	defer pool.Shutdown()

	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	if pool.WarmCount() < 1 {
		t.Errorf("expected at least 1 warm process, got %d", pool.WarmCount())
	}
}

func TestPool_RegisterAndUnregisterRequest(t *testing.T) {
	pool := NewPool("test", 1, "cat")
	defer pool.Shutdown()

	respCh := pool.RegisterRequest("req-1")
	if respCh == nil {
		t.Fatal("expected non-nil response channel")
	}

	pool.UnregisterRequest("req-1")

	// Unregister again should be safe
	pool.UnregisterRequest("req-1")
}

func TestPool_RegisterRequest_Timeout(t *testing.T) {
	// We can't easily test the 30s timeout, but we can verify the channel
	// is created and can receive data
	pool := NewPool("test", 1, "cat")
	defer pool.Shutdown()

	respCh := pool.RegisterRequest("req-timeout")
	defer pool.UnregisterRequest("req-timeout")

	// Simulate a response via BroadcastToSSE
	go func() {
		pool.BroadcastToSSE([]byte(`{"jsonrpc":"2.0","id":"req-timeout","result":{}}`))
	}()

	select {
	case data := <-respCh:
		if data == nil {
			t.Error("expected non-nil data")
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for response")
	}
}

func TestPool_IsClosed(t *testing.T) {
	pool := NewPool("test", 1, "cat")

	if pool.IsClosed() {
		t.Error("expected pool to not be closed")
	}

	pool.Shutdown()

	if !pool.IsClosed() {
		t.Error("expected pool to be closed after shutdown")
	}
}

func TestPool_SetDedicated(t *testing.T) {
	pool := NewPool("test", 1, "cat")
	defer pool.Shutdown()

	if pool.IsDedicated() {
		t.Error("expected pool to not be dedicated initially")
	}

	pool.SetDedicated("user-123")

	if !pool.IsDedicated() {
		t.Error("expected pool to be dedicated after SetDedicated")
	}

	if pool.DedicatedUser() != "user-123" {
		t.Errorf("expected dedicated user user-123, got %s", pool.DedicatedUser())
	}
}

func TestPool_IncrementDecrementActive(t *testing.T) {
	pool := NewPool("test", 1, "cat")
	defer pool.Shutdown()

	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active, got %d", pool.ActiveCount())
	}

	pool.IncrementActive()
	if pool.ActiveCount() != 1 {
		t.Errorf("expected 1 active, got %d", pool.ActiveCount())
	}

	pool.IncrementActive()
	if pool.ActiveCount() != 2 {
		t.Errorf("expected 2 active, got %d", pool.ActiveCount())
	}

	pool.DecrementActive()
	if pool.ActiveCount() != 1 {
		t.Errorf("expected 1 active after decrement, got %d", pool.ActiveCount())
	}
}

func TestPool_TryAcquireAndRelease(t *testing.T) {
	pool := NewPool("test", 1, "cat")
	defer pool.Shutdown()

	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	proc := pool.TryAcquireWarm()
	if proc == nil {
		t.Fatal("expected to acquire a warm process")
	}

	// Pool should now be empty
	proc2 := pool.TryAcquireWarm()
	if proc2 != nil {
		t.Error("expected no warm process available after acquiring the only one")
		pool.ReleaseWarm(proc2)
	}

	pool.ReleaseWarm(proc)

	// Should be available again
	proc3 := pool.TryAcquireWarm()
	if proc3 == nil {
		t.Error("expected warm process to be available after release")
	}
	pool.ReleaseWarm(proc3)
}

func TestPool_BroadcastToSSE(t *testing.T) {
	pool := NewPool("test", 1, "cat")
	defer pool.Shutdown()

	// Should not block or panic
	pool.BroadcastToSSE([]byte("test data"))
}

func TestPool_ShutdownIdempotent(t *testing.T) {
	pool := NewPool("test", 1, "cat")

	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	pool.Shutdown()
	pool.Shutdown() // Second call should be safe
}

func TestNewPoolManager(t *testing.T) {
	pm := NewPoolManager("cat", 1)

	if pm.PoolCount() != 0 {
		t.Errorf("expected 0 pools, got %d", pm.PoolCount())
	}
}

func TestPoolManager_GetPool(t *testing.T) {
	pm := NewPoolManager("cat", 1)

	pool1 := pm.GetPool("backend-1")
	defer pm.ShutdownAll()

	if pool1 == nil {
		t.Fatal("expected non-nil pool")
	}

	// Getting the same pool again should return the same instance
	pool1b := pm.GetPool("backend-1")
	if pool1 != pool1b {
		t.Error("expected same pool instance for same backendID")
	}

	if pm.PoolCount() != 1 {
		t.Errorf("expected 1 pool, got %d", pm.PoolCount())
	}
}

func TestPoolManager_GetOrCreatePool(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	pool := pm.GetOrCreatePool("jira", "npx jira", 2)
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}

	pool2 := pm.GetOrCreatePool("jira", "npx jira", 2)
	if pool != pool2 {
		t.Error("expected same pool instance for same backendID")
	}
}

func TestPoolManager_RemovePool(t *testing.T) {
	pm := NewPoolManager("cat", 1)

	pool := pm.GetPool("backend-1")
	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	pm.RemovePool("backend-1")

	if pm.PoolCount() != 0 {
		t.Errorf("expected 0 pools after removal, got %d", pm.PoolCount())
	}

	// Remove non-existent should be safe
	pm.RemovePool("does-not-exist")
}

func TestPoolManager_ShutdownAll(t *testing.T) {
	pm := NewPoolManager("cat", 1)

	pm.GetPool("backend-1")
	pm.GetPool("backend-2")

	if pm.PoolCount() != 2 {
		t.Errorf("expected 2 pools, got %d", pm.PoolCount())
	}

	pm.ShutdownAll()

	if pm.PoolCount() != 0 {
		t.Errorf("expected 0 pools after shutdown all, got %d", pm.PoolCount())
	}
}

func TestPoolManager_ConcurrentAccess(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	var wg sync.WaitGroup
	numGoroutines := 20

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			pm.GetPool("backend-1")
		}()
	}

	wg.Wait()

	if pm.PoolCount() != 1 {
		t.Errorf("expected exactly 1 pool from concurrent access, got %d", pm.PoolCount())
	}
}

func TestSafetyRecycler_AcquireRelease(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	pool := pm.GetPool("test")
	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	sr := NewSafetyRecycler(pm)

	proc := sr.AcquireProcess("test", "user1")
	if proc == nil {
		t.Fatal("expected to acquire process")
	}

	if pool.ActiveCount() != 1 {
		t.Errorf("expected 1 active after acquire, got %d", pool.ActiveCount())
	}

	// With dedicated policy, release should return to pool (not recycle)
	sr.ReleaseProcess("test", "user1", proc, nil)

	// Wait for refill
	time.Sleep(500 * time.Millisecond)

	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active after release, got %d", pool.ActiveCount())
	}
}

func TestSafetyRecycler_DedicatedProcess(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	pool := pm.GetPool("test")
	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	sr := NewSafetyRecycler(pm)

	proc := sr.AcquireProcess("test", "user1")
	if proc == nil {
		t.Fatal("expected to acquire process")
	}

	dedicated := sr.GetDedicatedProcess("test", "user1")
	if dedicated != proc {
		t.Error("expected dedicated process to match acquired process")
	}

	dedicated2 := sr.GetDedicatedProcess("test", "user2")
	if dedicated2 != nil {
		t.Error("expected nil dedicated process for different user")
	}

	sr.ReleaseProcess("test", "user1", proc, nil)
}

func TestSafetyRecycler_SetRecyclePolicy(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	sr := NewSafetyRecycler(pm)

	sr.SetRecyclePolicy(RecyclePolicyAlways)
	sr.SetRecyclePolicy(RecyclePolicyOnError)
	sr.SetRecyclePolicy(RecyclePolicyDedicated)
}

func TestSafetyRecycler_ReleaseDedicated(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	pool := pm.GetPool("test")
	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	sr := NewSafetyRecycler(pm)

	proc := sr.AcquireProcess("test", "user1")
	if proc == nil {
		t.Fatal("expected to acquire process")
	}

	sr.ReleaseDedicated("test", "user1")

	dedicated := sr.GetDedicatedProcess("test", "user1")
	if dedicated != nil {
		t.Error("expected nil after ReleaseDedicated")
	}

	// Decrement active since ReleaseDedicated kills the process but
	// doesn't go through ReleaseProcess
	pool.DecrementActive()
}

func TestSpawnProcess_Cat(t *testing.T) {
	pool := NewPool("test", 0, "cat")
	defer pool.Shutdown()

	proc, err := SpawnProcess(pool, "cat", nil)
	if err != nil {
		t.Fatalf("failed to spawn cat: %v", err)
	}
	defer proc.Kill()

	if proc.Cmd == nil {
		t.Error("expected non-nil Cmd")
	}
	if proc.Stdin == nil {
		t.Error("expected non-nil Stdin")
	}
	if proc.Stdout == nil {
		t.Error("expected non-nil Stdout")
	}
}

func TestSpawnProcess_ShellCommand(t *testing.T) {
	pool := NewPool("test", 0, "cat")
	defer pool.Shutdown()

	// Commands not in the direct-exec list go through "sh -c"
	proc, err := SpawnProcess(pool, "echo hello", nil)
	if err != nil {
		t.Fatalf("failed to spawn shell command: %v", err)
	}
	defer proc.Kill()

	if proc.Cmd == nil {
		t.Error("expected non-nil Cmd")
	}
}

func TestMin(t *testing.T) {
	if min(1*time.Second, 2*time.Second) != 1*time.Second {
		t.Error("expected 1s")
	}
	if min(5*time.Second, 3*time.Second) != 3*time.Second {
		t.Error("expected 3s")
	}
	if min(2*time.Second, 2*time.Second) != 2*time.Second {
		t.Error("expected 2s")
	}
}

// ---------- SpawnProcess with explicit env ----------

func TestSpawnProcess_WithExplicitEnv(t *testing.T) {
	pool := NewPool("test", 0, "cat")
	defer pool.Shutdown()

	env := []string{"MY_VAR=hello", "PATH=/usr/bin:/bin"}
	proc, err := SpawnProcess(pool, "env", env)
	if err != nil {
		t.Fatalf("SpawnProcess with env: %v", err)
	}
	defer proc.Kill()

	if proc.Cmd.Env == nil {
		t.Error("expected cmd.Env to be set")
	}
	if len(proc.Cmd.Env) != 2 {
		t.Errorf("expected 2 env vars, got %d", len(proc.Cmd.Env))
	}
}

func TestSpawnProcess_NilEnvInherits(t *testing.T) {
	pool := NewPool("test", 0, "cat")
	defer pool.Shutdown()

	proc, err := SpawnProcess(pool, "cat", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proc.Kill()

	if proc.Cmd.Env != nil {
		t.Error("expected cmd.Env to be nil (inherit parent env)")
	}
}

// ---------- NewPoolWithEnv ----------

func TestNewPoolWithEnv(t *testing.T) {
	env := []string{"FOO=bar", "PATH=/usr/bin:/bin"}
	pool := NewPoolWithEnv("test", 1, 1, "cat", env)
	defer pool.Shutdown()

	if len(pool.Env) != 2 {
		t.Errorf("Env len = %d, want 2", len(pool.Env))
	}
	if pool.Env[0] != "FOO=bar" {
		t.Errorf("Env[0] = %q, want FOO=bar", pool.Env[0])
	}
}

// ---------- Pool LastUsed / TouchLastUsed ----------

func TestPool_LastUsed(t *testing.T) {
	pool := NewPool("test", 1, "cat")
	defer pool.Shutdown()

	t1 := pool.LastUsed()
	if t1.IsZero() {
		t.Error("LastUsed should not be zero after creation")
	}

	time.Sleep(10 * time.Millisecond)
	pool.TouchLastUsed()
	t2 := pool.LastUsed()
	if !t2.After(t1) {
		t.Error("TouchLastUsed should advance the timestamp")
	}
}

// ---------- GetOrCreateUserPool ----------

func TestPoolManager_GetOrCreateUserPool(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	env := []string{"MY_TOKEN=abc123", "PATH=/usr/bin:/bin"}
	pool := pm.GetOrCreateUserPool("jira", "user-1", "cat", 1, 1, env)
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}

	if !pool.IsDedicated() {
		t.Error("expected user pool to be dedicated")
	}
	if pool.DedicatedUser() != "user-1" {
		t.Errorf("DedicatedUser = %q, want user-1", pool.DedicatedUser())
	}
	if len(pool.Env) != 2 {
		t.Errorf("Env len = %d, want 2", len(pool.Env))
	}

	// Same key should return same pool
	pool2 := pm.GetOrCreateUserPool("jira", "user-1", "cat", 1, 1, env)
	if pool != pool2 {
		t.Error("expected same pool for same backendID:userID")
	}

	// Different user should get different pool
	pool3 := pm.GetOrCreateUserPool("jira", "user-2", "cat", 1, 1, []string{"OTHER=val"})
	if pool3 == pool {
		t.Error("expected different pool for different userID")
	}

	if pm.PoolCount() != 2 {
		t.Errorf("PoolCount = %d, want 2", pm.PoolCount())
	}
}

func TestPoolManager_UserIsolation(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	env := []string{"USER_TOKEN=user-a-secret"}
	poolA := pm.GetOrCreateUserPool("backend1", "user-a", "cat", 1, 1, env)

	envB := []string{"USER_TOKEN=user-b-secret"}
	poolB := pm.GetOrCreateUserPool("backend1", "user-b", "cat", 1, 1, envB)

	if poolA == poolB {
		t.Fatal("different users should get different pools")
	}

	if poolA.DedicatedUser() != "user-a" {
		t.Errorf("poolA DedicatedUser = %q, want user-a", poolA.DedicatedUser())
	}
	if poolB.DedicatedUser() != "user-b" {
		t.Errorf("poolB DedicatedUser = %q, want user-b", poolB.DedicatedUser())
	}

	if !poolA.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process for user-a")
	}
	if !poolB.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm process for user-b")
	}

	procA := poolA.TryAcquireWarm()
	if procA == nil {
		t.Fatal("expected to acquire warm process from poolA")
	}
	poolA.ReleaseWarm(procA)

	procB := poolB.TryAcquireWarm()
	if procB == nil {
		t.Fatal("expected to acquire warm process from poolB")
	}
	if procA == procB {
		t.Error("different users should have different processes")
	}
	poolB.ReleaseWarm(procB)

	if pm.PoolCount() != 2 {
		t.Errorf("PoolCount = %d, want 2", pm.PoolCount())
	}
}

func TestPoolManager_SameUserSameBackendSameEnv(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	env := []string{"TOKEN=secret123"}
	pool1 := pm.GetOrCreateUserPool("backend", "user1", "cat", 1, 1, env)
	pool2 := pm.GetOrCreateUserPool("backend", "user1", "cat", 1, 1, env)

	if pool1 != pool2 {
		t.Error("same user + same backend should get same pool")
	}

	if pm.PoolCount() != 1 {
		t.Errorf("PoolCount = %d, want 1", pm.PoolCount())
	}
}

// ---------- Idle GC ----------

func TestPoolManager_IdleGC(t *testing.T) {
	// Use a very short idle timeout for testing.
	pm := NewPoolManagerWithGC("cat", 1, 200*time.Millisecond)
	defer pm.ShutdownAll()

	env := []string{"PATH=/usr/bin:/bin"}
	pool := pm.GetOrCreateUserPool("jira", "user-1", "cat", 1, 1, env)
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}

	if pm.PoolCount() != 1 {
		t.Fatalf("PoolCount = %d, want 1", pm.PoolCount())
	}

	// Wait for GC to collect the idle pool (timeout=200ms, GC runs every 100ms).
	time.Sleep(500 * time.Millisecond)

	if pm.PoolCount() != 0 {
		t.Errorf("PoolCount = %d after GC, want 0 (pool should have been collected)", pm.PoolCount())
	}
}

func TestPoolManager_IdleGC_PreservesActivePools(t *testing.T) {
	pm := NewPoolManagerWithGC("cat", 1, 300*time.Millisecond)
	defer pm.ShutdownAll()

	env := []string{"PATH=/usr/bin:/bin"}
	pool := pm.GetOrCreateUserPool("jira", "user-1", "cat", 1, 1, env)
	if pool == nil {
		t.Fatal("expected non-nil pool")
	}

	// Keep touching the pool to prevent GC
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
				pool.TouchLastUsed()
			}
		}
	}()

	time.Sleep(500 * time.Millisecond)
	close(done)

	if pm.PoolCount() != 1 {
		t.Errorf("PoolCount = %d, want 1 (active pool should not be collected)", pm.PoolCount())
	}
}

func TestPoolManager_IdleGC_SkipsNonDedicated(t *testing.T) {
	pm := NewPoolManagerWithGC("cat", 1, 200*time.Millisecond)
	defer pm.ShutdownAll()

	// Create a non-dedicated pool via GetOrCreatePool
	pm.GetOrCreatePool("default", "cat", 1)

	time.Sleep(500 * time.Millisecond)

	// Non-dedicated pools should NOT be collected
	if pm.PoolCount() != 1 {
		t.Errorf("PoolCount = %d, want 1 (non-dedicated pool should not be collected)", pm.PoolCount())
	}
}

// ---------- RemovePoolsByBackend ----------

func TestPoolManager_RemovePoolsByBackend_SharedAndUserPools(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	env := []string{"PATH=/usr/bin:/bin"}

	// Create a shared pool for "jira"
	pm.GetOrCreatePool("jira", "cat", 1)
	// Create per-user pools for "jira"
	pm.GetOrCreateUserPool("jira", "user-1", "cat", 1, 1, env)
	pm.GetOrCreateUserPool("jira", "user-2", "cat", 1, 1, env)
	// Create a pool for a different backend
	pm.GetOrCreatePool("github", "cat", 1)

	if pm.PoolCount() != 4 {
		t.Fatalf("PoolCount = %d, want 4", pm.PoolCount())
	}

	removed := pm.RemovePoolsByBackend("jira")
	if removed != 3 {
		t.Errorf("RemovePoolsByBackend returned %d, want 3", removed)
	}

	if pm.PoolCount() != 1 {
		t.Errorf("PoolCount = %d after removal, want 1 (only github should remain)", pm.PoolCount())
	}
}

func TestPoolManager_RemovePoolsByBackend_NoMatch(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	pm.GetOrCreatePool("github", "cat", 1)

	removed := pm.RemovePoolsByBackend("nonexistent")
	if removed != 0 {
		t.Errorf("RemovePoolsByBackend returned %d, want 0", removed)
	}
	if pm.PoolCount() != 1 {
		t.Errorf("PoolCount = %d, want 1", pm.PoolCount())
	}
}

func TestPoolManager_RemovePoolsByBackend_OnlyUserPools(t *testing.T) {
	pm := NewPoolManager("cat", 1)
	defer pm.ShutdownAll()

	env := []string{"PATH=/usr/bin:/bin"}
	pm.GetOrCreateUserPool("jira", "user-1", "cat", 1, 1, env)
	pm.GetOrCreateUserPool("jira", "user-2", "cat", 1, 1, env)
	pm.GetOrCreateUserPool("jira", "user-3", "cat", 1, 1, env)

	if pm.PoolCount() != 3 {
		t.Fatalf("PoolCount = %d, want 3", pm.PoolCount())
	}

	removed := pm.RemovePoolsByBackend("jira")
	if removed != 3 {
		t.Errorf("RemovePoolsByBackend returned %d, want 3", removed)
	}
	if pm.PoolCount() != 0 {
		t.Errorf("PoolCount = %d, want 0", pm.PoolCount())
	}
}

// ---------- ProbeBackend ----------

func TestProbeBackend_SpawnError(t *testing.T) {
	// Use an explicit empty PATH so that "sh" cannot be found by exec.
	result := ProbeBackend("/nonexistent-command-xyz", []string{"PATH="}, 2*time.Second)
	if result == nil {
		t.Fatal("expected non-nil ProbeResult")
	}
	if result.Status != "spawn_error" {
		t.Errorf("Status = %q, want spawn_error; Message = %s", result.Status, result.Message)
	}
	if result.DurationMs < 0 {
		t.Errorf("DurationMs = %d, should be >= 0", result.DurationMs)
	}
}

func TestProbeBackend_OK(t *testing.T) {
	// cat echoes back whatever is written to stdin, so the initialize
	// JSON-RPC message is echoed back as-is — which is a valid JSON-RPC
	// response (it has an "id" field). The response dispatcher will route
	// it to the pending request channel.
	result := ProbeBackend("cat", nil, 3*time.Second)
	if result == nil {
		t.Fatal("expected non-nil ProbeResult")
	}
	if result.Status != "ok" {
		t.Errorf("Status = %q, want ok; Message = %s; Stderr = %s", result.Status, result.Message, result.Stderr)
	}
	if result.DurationMs < 0 {
		t.Errorf("DurationMs = %d, should be >= 0", result.DurationMs)
	}
}

func TestProbeBackend_Timeout(t *testing.T) {
	// sleep never writes anything to stdout, so the handshake times out.
	result := ProbeBackend("sleep 60", nil, 500*time.Millisecond)
	if result == nil {
		t.Fatal("expected non-nil ProbeResult")
	}
	if result.Status != "handshake_timeout" {
		t.Errorf("Status = %q, want handshake_timeout", result.Status)
	}
	if result.DurationMs < 400 {
		t.Errorf("DurationMs = %d, expected at least ~500ms", result.DurationMs)
	}
}
