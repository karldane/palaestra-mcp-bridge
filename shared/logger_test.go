package shared

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func captureOutput(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	os.Stdout = old
	return buf.String()
}

func TestSetLogLevel_debug(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("debug")
	if currentLevel != DEBUG {
		t.Errorf("expected DEBUG, got %v", currentLevel)
	}
}

func TestSetLogLevel_info(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("info")
	if currentLevel != INFO {
		t.Errorf("expected INFO, got %v", currentLevel)
	}
}

func TestSetLogLevel_warn(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("warn")
	if currentLevel != WARN {
		t.Errorf("expected WARN, got %v", currentLevel)
	}
}

func TestSetLogLevel_warning(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("warning")
	if currentLevel != WARN {
		t.Errorf("expected WARN for 'warning', got %v", currentLevel)
	}
}

func TestSetLogLevel_error(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("error")
	if currentLevel != ERROR {
		t.Errorf("expected ERROR, got %v", currentLevel)
	}
}

func TestSetLogLevel_default(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("unknown")
	if currentLevel != INFO {
		t.Errorf("expected INFO (default), got %v", currentLevel)
	}
}

func TestGetLogLevel(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	tests := []struct {
		set    string
		expect string
	}{
		{"debug", "debug"},
		{"info", "info"},
		{"warn", "warn"},
		{"error", "error"},
	}
	for _, tt := range tests {
		SetLogLevel(tt.set)
		if got := GetLogLevel(); got != tt.expect {
			t.Errorf("GetLogLevel after SetLogLevel(%q) = %q, want %q", tt.set, got, tt.expect)
		}
	}

	currentLevel = LogLevel(99)
	if got := GetLogLevel(); got != "info" {
		t.Errorf("GetLogLevel for unknown level = %q, want %q", got, "info")
	}
}

func TestDebug_output(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("debug")
	output := captureOutput(func() { Debug("test debug") })
	if !strings.Contains(output, "test debug") || !strings.Contains(output, `"level":"debug"`) {
		t.Errorf("unexpected debug output: %s", output)
	}
}

func TestDebug_suppressed(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("info")
	output := captureOutput(func() { Debug("should not appear") })
	if output != "" {
		t.Errorf("expected no output at INFO level, got: %s", output)
	}
}

func TestDebugf_output(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("debug")
	output := captureOutput(func() { Debugf("formatted %d %s", 42, "test") })
	if !strings.Contains(output, "formatted 42 test") || !strings.Contains(output, `"level":"debug"`) {
		t.Errorf("unexpected debugf output: %s", output)
	}
}

func TestInfo_output(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("info")
	output := captureOutput(func() { Info("test info") })
	if !strings.Contains(output, "test info") || !strings.Contains(output, `"level":"info"`) {
		t.Errorf("unexpected info output: %s", output)
	}
}

func TestInfo_suppressed_at_warn(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("warn")
	output := captureOutput(func() { Info("should not appear") })
	if output != "" {
		t.Errorf("expected no output at WARN level, got: %s", output)
	}
}

func TestInfof_output(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("info")
	output := captureOutput(func() { Infof("fmt %s %d", "infof", 1) })
	if !strings.Contains(output, "fmt infof 1") || !strings.Contains(output, `"level":"info"`) {
		t.Errorf("unexpected infof output: %s", output)
	}
}

func TestWarn_output(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("warn")
	output := captureOutput(func() { Warn("test warn") })
	if !strings.Contains(output, "test warn") || !strings.Contains(output, `"level":"warn"`) {
		t.Errorf("unexpected warn output: %s", output)
	}
}

func TestWarn_suppressed_at_error(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("error")
	output := captureOutput(func() { Warn("should not appear") })
	if output != "" {
		t.Errorf("expected no output at ERROR level, got: %s", output)
	}
}

func TestWarnf_output(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("warn")
	output := captureOutput(func() { Warnf("warn %s", "formatted") })
	if !strings.Contains(output, "warn formatted") || !strings.Contains(output, `"level":"warn"`) {
		t.Errorf("unexpected warnf output: %s", output)
	}
}

func TestError_output(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("error")
	output := captureOutput(func() { Error("test error") })
	if !strings.Contains(output, "test error") || !strings.Contains(output, `"level":"error"`) {
		t.Errorf("unexpected error output: %s", output)
	}
}

func TestError_always_outputs(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("error")
	output := captureOutput(func() { Error("always shown") })
	if !strings.Contains(output, "always shown") {
		t.Errorf("expected error to always output, got: %s", output)
	}
}

func TestErrorf_output(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("error")
	output := captureOutput(func() { Errorf("err %s %d", "code", 500) })
	if !strings.Contains(output, "err code 500") || !strings.Contains(output, `"level":"error"`) {
		t.Errorf("unexpected errorf output: %s", output)
	}
}

func TestIsDebugEnabled(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	SetLogLevel("debug")
	if !IsDebugEnabled() {
		t.Error("expected IsDebugEnabled()=true at DEBUG level")
	}
	SetLogLevel("info")
	if IsDebugEnabled() {
		t.Error("expected IsDebugEnabled()=false at INFO level")
	}
}

func TestLogJSON_levelFilter(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	currentLevel = ERROR
	output := captureOutput(func() { logJSON(DEBUG, "debug", "should be filtered") })
	if output != "" {
		t.Errorf("expected empty output when level < currentLevel, got: %s", output)
	}
}

func TestLogJSON_jsonFormat(t *testing.T) {
	prev := currentLevel
	defer func() { currentLevel = prev }()
	currentLevel = DEBUG
	output := captureOutput(func() { logJSON(INFO, "info", "json check") })
	if !strings.Contains(output, `"level":"info"`) ||
		!strings.Contains(output, `"message":"json check"`) {
		t.Errorf("unexpected JSON format: %s", output)
	}
	if !strings.HasSuffix(strings.TrimSpace(output), "}") {
		t.Errorf("output should end with } on its own line, got: %s", output)
	}
}
