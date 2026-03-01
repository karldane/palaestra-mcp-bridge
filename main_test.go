package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIntegration_DefaultCommandProducesSSEOutput(t *testing.T) {
	os.Unsetenv("COMMAND")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process with default command")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, "pool", pool)

	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	sseHandler(w, req)

	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", w.Header().Get("Content-Type"))
	}

	body := w.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("expected SSE data with default command, got empty or no data")
	}
}

func TestIntegration_SSEEndpointStreamsProcessStdout(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, "pool", pool)

	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	sseHandler(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "data: y") {
		t.Errorf("expected SSE data with 'y' from yes command, got: %s", body)
	}
}

func TestIntegration_ReadyzReturns200WhenWarm(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm processes")
	}

	ctx := context.WithValue(context.Background(), "pool", pool)
	req := httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	readyzHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestIntegration_ReadyzReturns503WhenEmpty(t *testing.T) {
	os.Setenv("COMMAND", "false")
	defer os.Unsetenv("COMMAND")

	pool := &ProcessPool{
		warm:     make(chan *ManagedProcess, 2),
		spawning: make(chan struct{}, 1),
	}

	ctx := context.WithValue(context.Background(), "pool", pool)
	req := httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	readyzHandler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestIntegration_HealthzAlwaysReturns200(t *testing.T) {
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	healthzHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestIntegration_PoolMaintainsFixedSize(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)
	defer pool.Shutdown()

	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm processes")
	}

	if pool.WarmCount() != 2 {
		t.Errorf("expected pool size 2, got %d", pool.WarmCount())
	}

	pool.Shutdown()
}

func TestIntegration_PoolRefillsAfterDisconnect(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel1()
	ctx1 = context.WithValue(ctx1, "pool", pool)

	req1 := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx1)
	w1 := httptest.NewRecorder()
	sseHandler(w1, req1)

	time.Sleep(200 * time.Millisecond)

	if pool.WarmCount() != 1 {
		t.Errorf("expected pool to refill after disconnect, got %d", pool.WarmCount())
	}

	pool.Shutdown()
}

func TestIntegration_NoZombieProcesses(t *testing.T) {
	os.Setenv("COMMAND", "sleep 60")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	proc := <-pool.warm
	pool.warm <- proc

	time.Sleep(100 * time.Millisecond)

	proc.Kill()

	time.Sleep(300 * time.Millisecond)

	if pool.WarmCount() != 1 {
		t.Errorf("expected pool to refill after kill, got %d warm processes", pool.WarmCount())
	}

	pool.Shutdown()
}

func TestIntegration_MessagesEndpointRoutesToStdin(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	testPayload := `{"jsonrpc":"2.0","method":"test","id":1}`

	ctx := context.WithValue(context.Background(), "pool", pool)
	req := httptest.NewRequest("POST", "/messages", strings.NewReader(testPayload)).WithContext(ctx)
	w := httptest.NewRecorder()
	messagesHandler(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", w.Code)
	}

	pool.Shutdown()
}

func TestIntegration_MessagesReturns503WhenEmpty(t *testing.T) {
	pool := &ProcessPool{
		warm:     make(chan *ManagedProcess, 1),
		spawning: make(chan struct{}, 1),
	}

	ctx := context.WithValue(context.Background(), "pool", pool)
	req := httptest.NewRequest("POST", "/messages", strings.NewReader("{}")).WithContext(ctx)
	w := httptest.NewRecorder()
	messagesHandler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", w.Code)
	}
}

func TestIntegration_ConcurrentConnections(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)
	defer pool.Shutdown()

	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm processes")
	}

	numClients := 10
	var wg sync.WaitGroup
	wg.Add(numClients)

	for i := 0; i < numClients; i++ {
		go func() {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			ctx = context.WithValue(ctx, "pool", pool)

			req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
			req.Header.Set("Accept", "text/event-stream")

			w := httptest.NewRecorder()
			sseHandler(w, req)
		}()
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	if pool.WarmCount() != 2 {
		t.Errorf("expected pool to maintain 2 warm processes, got %d", pool.WarmCount())
	}
	if pool.ActiveCount() != 0 {
		t.Errorf("expected 0 active sessions after all disconnected, got %d", pool.ActiveCount())
	}

	pool.Shutdown()
}

func TestIntegration_HighConcurrencyStress(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)
	defer pool.Shutdown()

	if !pool.WaitForWarm(5 * time.Second) {
		t.Fatal("timeout waiting for initial warm processes")
	}

	numClients := 50
	var wg sync.WaitGroup
	wg.Add(numClients)

	for i := 0; i < numClients; i++ {
		go func() {
			defer wg.Done()

			duration := time.Duration(10+rand.Intn(40)) * time.Millisecond

			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			ctx = context.WithValue(ctx, "pool", pool)

			req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
			req.Header.Set("Accept", "text/event-stream")

			w := httptest.NewRecorder()
			sseHandler(w, req)
		}()
	}

	wg.Wait()
	time.Sleep(500 * time.Millisecond)

	warmCount := pool.WarmCount()
	if warmCount != 2 {
		t.Errorf("expected WarmCount=2 after stress test, got %d", warmCount)
	}

	activeCount := pool.ActiveCount()
	if activeCount != 0 {
		t.Errorf("expected ActiveCount=0 after all clients disconnected, got %d", activeCount)
	}

	pool.Shutdown()
}

func TestIntegration_GracefulShutdown(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)

	if !pool.WaitForWarm(2 * time.Second) {
		pool.Shutdown()
		t.Fatal("timeout waiting for warm processes")
	}

	pool.Shutdown()

	if !pool.IsClosed() {
		t.Error("expected pool to be closed after Shutdown")
	}
}

func TestIntegration_ExponentialBackoffOnFailure(t *testing.T) {
	os.Setenv("COMMAND", "false")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	time.Sleep(50 * time.Millisecond)

	proc1 := pool.warm
	if len(proc1) > 0 {
		<-proc1
	}

	time.Sleep(50 * time.Millisecond)

	select {
	case <-pool.warm:
		t.Error("should not spawn immediately due to backoff")
	default:
	}

	pool.Shutdown()
}

func TestIntegration_ProcessReaperCleansUp(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, "pool", pool)

	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	sseHandler(w, req)

	time.Sleep(300 * time.Millisecond)

	if pool.WarmCount() != 1 {
		t.Errorf("expected pool to refill after connection closed, got %d", pool.WarmCount())
	}

	pool.Shutdown()
}

func TestIntegration_SSEContentTypeHeader(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, "pool", pool)

	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	sseHandler(w, req)

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("expected Content-Type to contain text/event-stream, got %s", contentType)
	}
}

func TestIntegration_SSECacheControlHeader(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, "pool", pool)

	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	sseHandler(w, req)

	cacheControl := w.Header().Get("Cache-Control")
	if cacheControl != "no-cache" {
		t.Errorf("expected Cache-Control no-cache, got %s", cacheControl)
	}
}

func TestIntegration_SSESingleProcessPerConnection(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm processes")
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel1()
	ctx1 = context.WithValue(ctx1, "pool", pool)

	req1 := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx1)
	w1 := httptest.NewRecorder()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sseHandler(w1, req1)
	}()

	time.Sleep(50 * time.Millisecond)

	activeCount := pool.ActiveCount()
	if activeCount != 1 {
		t.Errorf("expected 1 active session during connection, got %d", activeCount)
	}

	wg.Wait()
	time.Sleep(200 * time.Millisecond)

	activeCount = pool.ActiveCount()
	if activeCount != 0 {
		t.Errorf("expected 0 active sessions after disconnect, got %d", activeCount)
	}

	pool.Shutdown()
}

func TestIntegration_ReadyzPutsProcessBack(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	ctx := context.WithValue(context.Background(), "pool", pool)
	req := httptest.NewRequest("GET", "/readyz", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	readyzHandler(w, req)

	time.Sleep(50 * time.Millisecond)

	if pool.WarmCount() != 1 {
		t.Errorf("expected process to be returned to pool after readyz, got %d", pool.WarmCount())
	}

	pool.Shutdown()
}

func TestIntegration_MultipleRoundsRefillStability(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)
	defer pool.Shutdown()

	if !pool.WaitForWarm(3 * time.Second) {
		t.Fatal("timeout waiting for warm processes")
	}

	for round := 0; round < 10; round++ {
		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				defer cancel()
				ctx = context.WithValue(ctx, "pool", pool)

				req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
				req.Header.Set("Accept", "text/event-stream")

				w := httptest.NewRecorder()
				sseHandler(w, req)
			}()
		}
		wg.Wait()
		time.Sleep(100 * time.Millisecond)

		if pool.WarmCount() != 2 {
			t.Errorf("round %d: expected 2 warm processes, got %d", round, pool.WarmCount())
		}
		if pool.ActiveCount() != 0 {
			t.Errorf("round %d: expected 0 active, got %d", round, pool.ActiveCount())
		}
	}

	pool.Shutdown()
}

func TestIntegration_StructuredJSONLogging(t *testing.T) {
	entry := LogEntry{
		Level:   "info",
		Message: "test message",
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("failed to marshal log: %v", err)
	}

	if !strings.Contains(string(data), "test message") {
		t.Error("expected log entry to contain message")
	}

	var parsed LogEntry
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal log: %v", err)
	}

	if parsed.Level != "info" {
		t.Errorf("expected level info, got %s", parsed.Level)
	}
}

func TestIntegration_ProcessKillUsesSIGKILL(t *testing.T) {
	os.Setenv("COMMAND", "sleep 60")
	defer os.Unsetenv("COMMAND")

	proc, err := spawnProcess("sleep 60")
	if err != nil {
		t.Fatalf("failed to spawn process: %v", err)
	}

	if proc.Cmd.Process == nil {
		t.Fatal("expected process to be started")
	}

	proc.Kill()

	time.Sleep(100 * time.Millisecond)

	if proc.Cmd.ProcessState != nil && !proc.Cmd.ProcessState.Exited() {
		proc.Cmd.Process.Kill()
		t.Error("expected process to be killed")
	}
}

func TestIntegration_EmptyCommandUsesDefault(t *testing.T) {
	os.Unsetenv("COMMAND")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("default command should produce warm process")
	}

	pool.Shutdown()
}
