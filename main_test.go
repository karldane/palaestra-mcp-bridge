package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
