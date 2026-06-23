package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// jsonRPCError builds a JSON-RPC 2.0 error response map.
func jsonRPCError(id interface{}, code int, msg string, data interface{}) map[string]interface{} {
	e := map[string]interface{}{
		"jsonrpc": "2.0",
		"error": map[string]interface{}{
			"code":    code,
			"message": msg,
		},
	}
	if data != nil {
		e["error"].(map[string]interface{})["data"] = data
	}
	if id != nil {
		e["id"] = id
	}
	return e
}

// writeJSONRPCError writes a JSON-RPC 2.0 error response to w, setting the
// Content-Type and Content-Length headers.
func writeJSONRPCError(w http.ResponseWriter, id interface{}, code int, msg string, data interface{}) {
	resp := jsonRPCError(id, code, msg, data)
	body, err := json.Marshal(resp)
	if err != nil {
		// Should never happen with simple map values, but be safe.
		http.Error(w, fmt.Sprintf(`{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error: %s"}}`, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(http.StatusOK) // JSON-RPC errors are 200 OK with JSON-RPC error body
	w.Write(body)
}
