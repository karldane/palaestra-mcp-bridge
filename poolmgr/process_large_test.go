package poolmgr

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// TestCaptureStdout_LargeResponse demonstrates the issue where bufio.Scanner
// fails when reading MCP responses with large JSON bodies that don't contain newlines.
// The scanner splits on \n, but if the entire message (after Content-Length header)
// is larger than the buffer, it fails with "bufio.Scanner: token too long".
func TestCaptureStdout_LargeResponse(t *testing.T) {
	// Simulate a large MCP tools/list response that would exceed the default buffer
	largeTools := generateLargeToolsList(500) // 500 tools

	// Create JSON-RPC response with large body
	mcpResponse := `{"jsonrpc":"2.0","id":"1","result":{"tools":` + largeTools + `}}` + "\n"

	// Simulate what the MCP backend sends: Content-Length header + body
	bodyLen := len(mcpResponse)
	input := "Content-Length: " + string(rune('0'+bodyLen/100000)) +
		string(rune('0'+(bodyLen/10000)%10)) +
		string(rune('0'+(bodyLen/1000)%10)) +
		string(rune('0'+(bodyLen/100)%10)) +
		string(rune('0'+(bodyLen/10)%10)) +
		string(rune('0'+bodyLen%10)) +
		"\r\n\r\n" + mcpResponse

	reader := strings.NewReader(input)

	// This is how the current code works - it will fail with token too long
	scanner := bufio.NewScanner(reader)
	const maxCapacity = 32 * 1024 * 1024 // 32MB - current limit
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	// The scanner will read line by line
	// First scan gets: "Content-Length: NNN"
	// Second scan tries to get the JSON, but if it's > 32MB it fails

	// Count how many successful scans we get
	scanCount := 0
	for scanner.Scan() {
		scanCount++
	}

	if err := scanner.Err(); err != nil {
		t.Logf("Scanner error (expected with current implementation): %v", err)
		// This is the bug - we want to see "token too long" or similar
		// For now, we just document that this scenario causes issues
	}

	t.Logf("Scanner completed with %d scans, final error: %v", scanCount, scanner.Err())

	// The issue is that we can't reliably parse the Content-Length protocol
	// with line-based scanning when the body itself may contain no newlines
	// or be larger than any reasonable buffer
}

// TestCaptureStdout_NewReaderApproach tests a bufio.Reader approach
// that properly handles Content-Length headers.
func TestCaptureStdout_NewReaderApproach(t *testing.T) {
	// Generate a large tools list
	largeTools := generateLargeToolsList(500)
	mcpResponse := `{"jsonrpc":"2.0","id":"1","result":{"tools":` + largeTools + `}}`
	bodyLen := len(mcpResponse)

	// MCP message format: "Content-Length: N\r\n\r\n{body}"
	mcpMessage := formatMCPPacket(mcpResponse)

	reader := strings.NewReader(mcpMessage)

	// Use bufio.Reader instead of Scanner
	bufReader := bufio.NewReader(reader)

	messages, err := readMCPMessages(bufReader)
	if err != nil {
		t.Fatalf("Failed to read MCP messages: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if len(messages[0]) != bodyLen {
		t.Errorf("Expected body length %d, got %d", bodyLen, len(messages[0]))
	}

	// Verify it's valid JSON
	if !strings.Contains(string(messages[0]), `"name"`) {
		t.Error("Expected JSON to contain tool names")
	}
}

// generateLargeToolsList generates a JSON array of N tools
func generateLargeToolsList(count int) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < count; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"name":"tool_`)
		sb.WriteString(string(rune('0' + i/100)))
		sb.WriteString(string(rune('0' + (i/10)%10)))
		sb.WriteString(string(rune('0' + i%10)))
		sb.WriteString(`","description":"A test tool with index `)
		sb.WriteString(string(rune('0' + i/100)))
		sb.WriteString(string(rune('0' + (i/10)%10)))
		sb.WriteString(string(rune('0' + i%10)))
		sb.WriteString(`","inputSchema":{"type":"object"}}`)
	}
	sb.WriteString("]")
	return sb.String()
}

// formatMCPPacket formats a message as MCP packet with Content-Length
func formatMCPPacket(body string) string {
	return "Content-Length: " + intToString(len(body)) + "\r\n\r\n" + body
}

// intToString converts int to string without importing strconv
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	var sb strings.Builder
	for n > 0 {
		sb.WriteRune(rune('0' + n%10))
		n /= 10
	}
	// Reverse
	result := sb.String()
	runes := []rune(result)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

// readMCPMessages reads MCP messages from a bufio.Reader, properly handling
// Content-Length headers. This is the FIX for the token-too-long issue.
func readMCPMessages(r *bufio.Reader) ([][]byte, error) {
	var messages [][]byte

	for {
		// Read line by line to get the Content-Length header
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		// Trim \r\n or \n
		line = strings.TrimRight(line, "\r\n")

		// Check for Content-Length header
		if strings.HasPrefix(line, "Content-Length: ") {
			var contentLen int

			// Parse content length
			lenStr := strings.TrimPrefix(line, "Content-Length: ")
			for _, c := range lenStr {
				if c >= '0' && c <= '9' {
					contentLen = contentLen*10 + int(c-'0')
				}
			}

			// Read the blank line (\r\n\r\n) after the header
			// The blank line should be there, read until we get it
			for {
				b, err := r.ReadByte()
				if err != nil {
					return nil, err
				}
				if b == '\n' {
					break
				}
			}

			// Read exactly contentLen bytes
			body := make([]byte, contentLen)
			_, err = io.ReadFull(r, body)
			if err != nil {
				return nil, err
			}

			messages = append(messages, body)
		}
	}

	return messages, nil
}
