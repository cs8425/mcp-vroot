package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func doJSON(
	t *testing.T,
	h http.Handler,
	method string,
	url string,
	headers map[string]string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()

	var buf bytes.Buffer

	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(method, url, &buf)

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(
		"Accept",
		"application/json, text/event-stream",
	)
	req.Header.Set(
		"Origin",
		"http://localhost:8080",
	)

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	return rr
}

func TestStreamableHTTP(t *testing.T) {
	reg := NewRegistry("")
	regToolEcho(reg)

	s := &McpServer{
		Name:    "test-server",
		Version: "0.1.0",
		Origin: map[string]bool{
			"http://localhost:8080": true,
		},
		Registry: reg,
	}

	handler := s.Handler()

	t.Run("modern server/discover", func(t *testing.T) {
		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "server/discover",

			"params": map[string]any{
				"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion": ModernVersion,

					"io.modelcontextprotocol/clientInfo": map[string]any{
						"name":    "test-client",
						"version": "1.0",
					},

					"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				},
			},
		}

		rr := doJSON(
			t,
			handler,
			http.MethodPost,
			"/mcp",
			map[string]string{
				"MCP-Protocol-Version": ModernVersion,
				"Mcp-Method":           "server/discover",
			},
			body,
		)

		if rr.Code != http.StatusOK {
			t.Fatalf(
				"unexpected status=%d body=%s",
				rr.Code,
				rr.Body.String(),
			)
		}

		if got := rr.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("unexpected Content-Type=%q", got)
		}

		var response struct {
			Result struct {
				ResultType        string   `json:"resultType"`
				SupportedVersions []string `json:"supportedVersions"`
				TTLMS             int      `json:"ttlMs"`
			} `json:"result"`
		}

		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}

		if response.Result.ResultType != "complete" {
			t.Fatalf(
				"unexpected resultType=%q",
				response.Result.ResultType,
			)
		}

		if response.Result.TTLMS <= 0 {
			t.Fatalf(
				"unexpected ttlMs=%d",
				response.Result.TTLMS,
			)
		}

		if len(response.Result.SupportedVersions) != 2 {
			t.Fatalf(
				"unexpected supported versions=%v",
				response.Result.SupportedVersions,
			)
		}
	})

	t.Run("modern tools/call", func(t *testing.T) {
		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/call",

			"params": map[string]any{
				"name": "echo",

				"arguments": map[string]any{
					"text": "hello",
				},

				"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion": ModernVersion,

					"io.modelcontextprotocol/clientInfo": map[string]any{
						"name":    "test-client",
						"version": "1.0",
					},

					"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				},
			},
		}

		rr := doJSON(
			t,
			handler,
			http.MethodPost,
			"/mcp",
			map[string]string{
				"MCP-Protocol-Version": ModernVersion,
				"Mcp-Method":           "tools/call",
				"Mcp-Name":             "echo",
			},
			body,
		)

		if rr.Code != http.StatusOK {
			t.Fatalf(
				"unexpected status=%d body=%s",
				rr.Code,
				rr.Body.String(),
			)
		}

		var response struct {
			Result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}

		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}

		if len(response.Result.Content) != 1 {
			t.Fatalf(
				"unexpected content=%v",
				response.Result.Content,
			)
		}

		if response.Result.Content[0].Type != "text" {
			t.Fatalf(
				"unexpected content type=%q",
				response.Result.Content[0].Type,
			)
		}

		if response.Result.Content[0].Text != "hello" {
			t.Fatalf(
				"unexpected tool output=%q",
				response.Result.Content[0].Text,
			)
		}
	})

	t.Run("modern header/body mismatch", func(t *testing.T) {
		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "tools/call",

			"params": map[string]any{
				"name": "echo",

				"arguments": map[string]any{
					"text": "hello",
				},

				"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion": ModernVersion,

					"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				},
			},
		}

		rr := doJSON(
			t,
			handler,
			http.MethodPost,
			"/mcp",
			map[string]string{
				"MCP-Protocol-Version": ModernVersion,
				"Mcp-Method":           "tools/call",
				"Mcp-Name":             "WRONG",
			},
			body,
		)

		if rr.Code != http.StatusBadRequest {
			t.Fatalf(
				"unexpected status=%d body=%s",
				rr.Code,
				rr.Body.String(),
			)
		}

		var response rpcResponse

		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}

		if response.Error == nil {
			t.Fatal("expected JSON-RPC error")
		}

		if response.Error.Code != -32020 {
			t.Fatalf(
				"unexpected error code=%d",
				response.Error.Code,
			)
		}
	})

	t.Run("legacy 2025 initialize then tools/call", func(t *testing.T) {
		initializeBody := map[string]any{
			"jsonrpc": "2.0",
			"id":      10,
			"method":  "initialize",

			"params": map[string]any{
				"protocolVersion": LegacyVersion,

				"capabilities": map[string]any{},

				"clientInfo": map[string]any{
					"name":    "legacy-client",
					"version": "1.0",
				},
			},
		}

		rr := doJSON(
			t,
			handler,
			http.MethodPost,
			"/mcp",
			nil,
			initializeBody,
		)

		if rr.Code != http.StatusOK {
			t.Fatalf(
				"initialize status=%d body=%s",
				rr.Code,
				rr.Body.String(),
			)
		}

		// This server deliberately uses stateless legacy Streamable HTTP.
		if sessionID := rr.Header().Get("MCP-Session-Id"); sessionID != "" {
			t.Fatalf(
				"unexpected MCP-Session-Id=%q",
				sessionID,
			)
		}

		callBody := map[string]any{
			"jsonrpc": "2.0",
			"id":      11,
			"method":  "tools/call",

			"params": map[string]any{
				"name": "echo",

				"arguments": map[string]any{
					"text": "legacy",
				},
			},
		}

		rr = doJSON(
			t,
			handler,
			http.MethodPost,
			"/mcp",
			map[string]string{
				"MCP-Protocol-Version": LegacyVersion,
			},
			callBody,
		)

		if rr.Code != http.StatusOK {
			t.Fatalf(
				"tools/call status=%d body=%s",
				rr.Code,
				rr.Body.String(),
			)
		}
	})

	t.Run("unknown origin rejected", func(t *testing.T) {
		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      20,
			"method":  "tools/list",

			"params": map[string]any{
				"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion": ModernVersion,

					"io.modelcontextprotocol/clientCapabilities": map[string]any{},
				},
			},
		}

		var buf bytes.Buffer

		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(
			http.MethodPost,
			"/mcp",
			&buf,
		)

		req.Header.Set(
			"Content-Type",
			"application/json",
		)

		req.Header.Set(
			"Accept",
			"application/json, text/event-stream",
		)

		req.Header.Set(
			"Origin",
			"https://evil.example",
		)

		req.Header.Set(
			"MCP-Protocol-Version",
			ModernVersion,
		)

		req.Header.Set(
			"Mcp-Method",
			"tools/list",
		)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf(
				"unexpected status=%d body=%s",
				rr.Code,
				rr.Body.String(),
			)
		}
	})

	t.Run("CORS preflight", func(t *testing.T) {
		req := httptest.NewRequest(
			http.MethodOptions,
			"/mcp",
			nil,
		)

		req.Header.Set(
			"Origin",
			"http://localhost:8080",
		)

		req.Header.Set(
			"Access-Control-Request-Method",
			"POST",
		)

		req.Header.Set(
			"Access-Control-Request-Headers",
			"content-type,mcp-protocol-version,mcp-method",
		)

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNoContent {
			t.Fatalf(
				"unexpected status=%d",
				rr.Code,
			)
		}

		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:8080" {
			t.Fatalf(
				"unexpected allow-origin=%q",
				got,
			)
		}
	})
}
