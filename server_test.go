package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
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

	t.Run("legacy ping before initialize", func(t *testing.T) {
		body := map[string]any{
			"jsonrpc": "2.0",
			"id":      30,
			"method":  "ping",
		}

		rr := doJSON(
			t,
			handler,
			http.MethodPost,
			"/mcp",
			nil,
			body,
		)

		if rr.Code != http.StatusOK {
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

		if response.Error != nil {
			t.Fatalf("unexpected error=%v", response.Error)
		}
	})

	t.Run("legacy initialize with older version negotiates", func(t *testing.T) {
		initializeBody := map[string]any{
			"jsonrpc": "2.0",
			"id":      31,
			"method":  "initialize",

			"params": map[string]any{
				"protocolVersion": "2025-06-18",

				"capabilities": map[string]any{},

				"clientInfo": map[string]any{
					"name":    "old-client",
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

		var response struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}

		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}

		if response.Result.ProtocolVersion != LegacyVersion {
			t.Fatalf(
				"unexpected protocolVersion=%q",
				response.Result.ProtocolVersion,
			)
		}
	})

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

func TestStdio(t *testing.T) {
	reg := NewRegistry("")
	regToolEcho(reg)

	s := &McpServer{
		Name:     "test-server",
		Version:  "0.1.0",
		Registry: reg,
	}

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	std := NewStdioServer(s)
	std.in = inR
	std.out = outW

	done := make(chan error, 1)
	go func() {
		done <- std.Serve()
	}()

	out := bufio.NewReader(outR)

	writeMsg := func(t *testing.T, v any) {
		t.Helper()

		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}

	readResp := func(t *testing.T) rpcResponse {
		t.Helper()

		line, err := out.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}

		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resultJSON := func(t *testing.T, resp rpcResponse, v any) {
		t.Helper()

		b, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("legacy request before initialize", func(t *testing.T) {
		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      5,
			"method":  "tools/list",
			"params":  map[string]any{},
		})

		resp := readResp(t)
		if resp.Error == nil {
			t.Fatal("expected JSON-RPC error")
		}
		if resp.Error.Code != -32600 {
			t.Fatalf("unexpected error code=%d", resp.Error.Code)
		}
	})

	t.Run("ping before initialize", func(t *testing.T) {
		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      6,
			"method":  "ping",
		})

		resp := readResp(t)
		if resp.Error != nil {
			t.Fatalf("unexpected error=%v", resp.Error)
		}
	})

	t.Run("modern server/discover", func(t *testing.T) {
		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "server/discover",
			"params": map[string]any{
				"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion": ModernVersion,
				},
			},
		})

		resp := readResp(t)
		if resp.Error != nil {
			t.Fatalf("unexpected error=%v", resp.Error)
		}

		var result struct {
			ResultType        string   `json:"resultType"`
			SupportedVersions []string `json:"supportedVersions"`
		}
		resultJSON(t, resp, &result)

		if result.ResultType != "complete" {
			t.Fatalf("unexpected resultType=%q", result.ResultType)
		}
		if !slices.Contains(result.SupportedVersions, ModernVersion) {
			t.Fatalf("unexpected supported versions=%v", result.SupportedVersions)
		}
	})

	t.Run("modern tools/list", func(t *testing.T) {
		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/list",
			"params": map[string]any{
				"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion": ModernVersion,
				},
			},
		})

		resp := readResp(t)
		if resp.Error != nil {
			t.Fatalf("unexpected error=%v", resp.Error)
		}

		var result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		resultJSON(t, resp, &result)

		if len(result.Tools) != 1 || result.Tools[0].Name != "echo" {
			t.Fatalf("unexpected tools=%v", result.Tools)
		}
	})

	t.Run("modern tools/call", func(t *testing.T) {
		writeMsg(t, map[string]any{
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
				},
			},
		})

		resp := readResp(t)
		if resp.Error != nil {
			t.Fatalf("unexpected error=%v", resp.Error)
		}

		var result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		resultJSON(t, resp, &result)

		if len(result.Content) != 1 || result.Content[0].Text != "hello" {
			t.Fatalf("unexpected content=%v", result.Content)
		}
	})

	t.Run("unknown method", func(t *testing.T) {
		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      4,
			"method":  "resources/list",
			"params": map[string]any{
				"_meta": map[string]any{
					"io.modelcontextprotocol/protocolVersion": ModernVersion,
				},
			},
		})

		resp := readResp(t)
		if resp.Error == nil {
			t.Fatal("expected JSON-RPC error")
		}
		if resp.Error.Code != -32601 {
			t.Fatalf("unexpected error code=%d", resp.Error.Code)
		}
	})

	t.Run("legacy initialize then tools/call", func(t *testing.T) {
		// Advertise an older version on purpose: per the 2025-11-25 version
		// negotiation rules the server must answer with the latest version
		// it supports instead of failing.
		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      10,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{},
				"clientInfo": map[string]any{
					"name":    "legacy-client",
					"version": "1.0",
				},
			},
		})

		resp := readResp(t)
		if resp.Error != nil {
			t.Fatalf("initialize error=%v", resp.Error)
		}

		var initResult struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		resultJSON(t, resp, &initResult)
		if initResult.ProtocolVersion != LegacyVersion {
			t.Fatalf("unexpected protocolVersion=%q", initResult.ProtocolVersion)
		}

		// A ping after initialization must also be answered.
		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      105,
			"method":  "ping",
		})

		resp = readResp(t)
		if resp.Error != nil {
			t.Fatalf("ping error=%v", resp.Error)
		}

		// notifications/initialized is fire-and-forget: no response.
		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/initialized",
		})

		writeMsg(t, map[string]any{
			"jsonrpc": "2.0",
			"id":      11,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "echo",
				"arguments": map[string]any{
					"text": "legacy",
				},
			},
		})

		resp = readResp(t)
		if resp.Error != nil {
			t.Fatalf("tools/call error=%v", resp.Error)
		}

		var result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		resultJSON(t, resp, &result)
		if len(result.Content) != 1 || result.Content[0].Text != "legacy" {
			t.Fatalf("unexpected content=%v", result.Content)
		}
	})

	// Closing stdin is the graceful-shutdown signal: Serve must return.
	if err := inW.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected serve error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not exit on stdin EOF")
	}
}
