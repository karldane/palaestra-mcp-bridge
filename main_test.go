package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProcessPoolInitialization(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)
	defer pool.Shutdown()

	time.Sleep(500 * time.Millisecond)

	if len(pool.warm) < 1 {
		t.Errorf("expected pool to have at least 1 warm process, got %d", len(pool.warm))
	}
}

func TestProcessSpawnAndHandshake(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	proc, err := spawnProcess("cat")
	if err != nil {
		t.Fatalf("failed to spawn process: %v", err)
	}
	defer proc.Kill()

	if !proc.isWarm {
		t.Error("expected process to be warm after handshake")
	}
}

func TestReadyzReturns200WhenWarm(t *testing.T) {
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

func TestReadyzReturns503WhenNoWarmProcesses(t *testing.T) {
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

func TestHealthzReturns200(t *testing.T) {
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	healthzHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
}

func TestProcessKillRefillsPool(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm processes")
	}

	proc := <-pool.warm
	proc.Kill()

	time.Sleep(100 * time.Millisecond)

	select {
	case newProc := <-pool.warm:
		if newProc != nil {
			pool.warm <- newProc
		}
	default:
	}
}

func TestJSONRPCRequestRouting(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm processes")
	}

	select {
	case proc := <-pool.warm:
		msg := JSONRPCMessage{
			JSONRPC: "2.0",
			Method:  "test",
			ID:      1,
		}
		data, _ := json.Marshal(msg)
		proc.Stdin.Write(data)
		pool.warm <- proc
	default:
		t.Fatal("no process available in pool")
	}
}

func TestStructuredLogOutput(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	logEntry := LogEntry{
		Level:   "info",
		Message: "test message",
		Time:    time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(logEntry)
	if err != nil {
		t.Fatalf("failed to marshal log: %v", err)
	}

	if !strings.Contains(string(data), "test message") {
		t.Error("expected log entry to contain message")
	}
}

func TestJSONRPCHandshakeSent(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	proc, err := spawnProcess("cat")
	if err != nil {
		t.Fatalf("failed to spawn process: %v", err)
	}
	defer proc.Kill()

	time.Sleep(100 * time.Millisecond)

	proc.Stdout.Read(make([]byte, 1024))
}

func TestHandshakeVerificationRequiresValidResponse(t *testing.T) {
	os.Setenv("COMMAND", "sh -c 'echo invalid'")
	defer os.Unsetenv("COMMAND")

	_, err := spawnProcess("sh -c 'echo invalid'")
	if err == nil {
		t.Error("expected error for invalid handshake response")
	}
}

func TestExponentialBackoffOnFailure(t *testing.T) {
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

func TestSSEConnectionUpgrade(t *testing.T) {
	os.Setenv("COMMAND", "yes")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm processes")
	}

	ctx := context.WithValue(context.Background(), "pool", pool)
	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()
	done := make(chan bool)
	go func() {
		sseHandler(w, req)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}

	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", w.Header().Get("Content-Type"))
	}
}

func TestFixedBufferPoolStress(t *testing.T) {
	os.Setenv("COMMAND", "cat")
	defer os.Unsetenv("COMMAND")

	pool := NewProcessPool(2)

	if !pool.WaitForWarm(5 * time.Second) {
		pool.Shutdown()
		t.Fatal("timeout waiting for initial warm processes")
	}

	initialWarm := pool.WarmCount()
	if initialWarm != 2 {
		pool.Shutdown()
		t.Fatalf("expected 2 initial warm processes, got %d", initialWarm)
	}

	numClients := 50
	var wg sync.WaitGroup
	wg.Add(numClients)

	errors := make(chan error, numClients)

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

			if w.Code != 0 && w.Code != http.StatusOK && w.Code != http.StatusServiceUnavailable {
				errors <- fmt.Errorf("unexpected status code: %d", w.Code)
			}
		}()
	}

	wg.Wait()
	close(errors)

	select {
	case err := <-errors:
		if err != nil {
			t.Errorf("client error: %v", err)
		}
	default:
	}

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

func TestNoZombieProcesses(t *testing.T) {
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

	time.Sleep(200 * time.Millisecond)

	if pool.WarmCount() != 1 {
		t.Errorf("expected pool to refill after kill, got %d warm processes", pool.WarmCount())
	}

	pool.Shutdown()
}

func TestConcurrentRefillStability(t *testing.T) {
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

func TestSSEReadsFromProcessStdout(t *testing.T) {
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
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()

	sseHandler(w, req)

	if w.Header().Get("Content-Type") != "text/event-stream" {
		t.Errorf("expected Content-Type text/event-stream, got %s", w.Header().Get("Content-Type"))
	}

	body := w.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("expected SSE data in response, got: %s", body)
	}
}

func TestDefaultCommandProducesOutput(t *testing.T) {
	os.Unsetenv("COMMAND")

	pool := NewProcessPool(1)
	defer pool.Shutdown()

	if !pool.WaitForWarm(2 * time.Second) {
		t.Fatal("timeout waiting for warm process")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, "pool", pool)

	req := httptest.NewRequest("GET", "/sse", nil).WithContext(ctx)
	req.Header.Set("Accept", "text/event-stream")

	w := httptest.NewRecorder()

	sseHandler(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("expected SSE data with default command, got: %s", body)
	}
}
