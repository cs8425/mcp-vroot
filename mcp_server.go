package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type McpServer struct {
	Name    string
	Version string

	// Exact CORS Origin allow-list.
	// No Origin = non-browser client, allowed.
	Origin map[string]bool

	Registry *Registry
}

func (s *McpServer) Handler() http.Handler {
	return http.HandlerFunc(s.handle)
}

func (s *McpServer) handle(w http.ResponseWriter, r *http.Request) {
	// CORS is not part of MCP, but is needed when the llama.cpp WebUI
	// directly calls this endpoint from a browser.
	s.setCORS(w, r)

	if r.Method == http.MethodOptions {
		if origin := r.Header.Get("Origin"); origin != "" && !s.allowedOrigin(origin) {
			writePlain(w, http.StatusForbidden, "forbidden origin")
			return
		}

		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 2026-07-28 Streamable HTTP only uses POST.
	// GET/DELETE were part of earlier revisions.
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Origin validation is required for HTTP MCP servers to defend
	// against DNS rebinding.
	if origin := r.Header.Get("Origin"); origin != "" && !s.allowedOrigin(origin) {
		writePlain(w, http.StatusForbidden, "forbidden origin")
		return
	}

	// A conforming Streamable HTTP client advertises both.
	if !acceptsStreamableHTTP(r.Header.Get("Accept")) {
		writeJSONRPCError(
			w,
			http.StatusNotAcceptable,
			nil,
			-32600,
			"Accept must include application/json and text/event-stream",
			nil,
		)
		return
	}

	// MCP POST body is JSON.
	if ct := strings.ToLower(r.Header.Get("Content-Type")); !strings.HasPrefix(ct, "application/json") {
		writeJSONRPCError(
			w,
			http.StatusUnsupportedMediaType,
			nil,
			-32600,
			"Content-Type must be application/json",
			nil,
		)
		return
	}

	// Avoid an accidental unbounded body.
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			nil,
			-32700,
			"failed to read request body",
			nil,
		)
		return
	}

	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			nil,
			-32700,
			"invalid JSON",
			nil,
		)
		return
	}

	if req.JSONRPC != JSONRPCVersion || req.Method == "" {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			req.ID,
			-32600,
			"invalid JSON-RPC request",
			nil,
		)
		return
	}

	/*
		2026-07-28:
		  - per-request _meta
		  - MCP-Protocol-Version
		  - Mcp-Method
		  - Mcp-Name for tools/call, resources/read, prompts/get

		2025-11-25:
		  - initialize handshake
		  - MCP-Protocol-Version on subsequent HTTP requests
		  - optional MCP-Session-Id

		We distinguish them by the presence/value of the modern
		header mirrors. A legacy client may still send
		MCP-Protocol-Version: 2025-11-25, but will not send Mcp-Method.
	*/
	if r.Header.Get("Mcp-Method") != "" ||
		r.Header.Get("MCP-Protocol-Version") == ModernVersion {
		s.handleModern(w, r, req)
		return
	}

	s.handleLegacy(w, r, req)
}

func (s *McpServer) handleModern(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	version := r.Header.Get("MCP-Protocol-Version")

	if version != ModernVersion {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			req.ID,
			-32022,
			"unsupported protocol version",
			map[string]any{
				"requested": version,
				"supported": []string{
					ModernVersion,
					LegacyVersion,
				},
			},
		)
		return
	}

	var p ToolRequest
	if len(req.Params) == 0 {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			req.ID,
			-32602,
			"params must contain _meta",
			nil,
		)
		return
	}

	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			req.ID,
			-32602,
			"invalid params",
			nil,
		)
		return
	}

	// Header/body version must match.
	if p.Meta.ProtocolVersion != version {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			req.ID,
			-32020,
			"MCP-Protocol-Version does not match _meta",
			nil,
		)
		return
	}

	// Header/body method must match.
	if r.Header.Get("Mcp-Method") != req.Method {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			req.ID,
			-32020,
			"Mcp-Method does not match method",
			nil,
		)
		return
	}

	// tools/call additionally mirrors params.name.
	if req.Method == "tools/call" {
		if r.Header.Get("Mcp-Name") != p.Name {
			writeJSONRPCError(
				w,
				http.StatusBadRequest,
				req.ID,
				-32020,
				"Mcp-Name does not match params.name",
				nil,
			)
			return
		}
	}

	s.dispatch(w, r, req, ModernVersion, p)
}

func (s *McpServer) handleLegacy(w http.ResponseWriter, r *http.Request, req rpcRequest) {
	// Old Streamable HTTP clients start with initialize.
	switch req.Method {
	case "notifications/initialized": // just skip
		return
	case "initialize":
		var p initializeParams

		if err := json.Unmarshal(req.Params, &p); err != nil {
			writeJSONRPCError(
				w,
				http.StatusBadRequest,
				req.ID,
				-32602,
				"invalid initialize params",
				nil,
			)
			return
		}

		if p.ProtocolVersion != LegacyVersion {
			writeJSONRPCError(
				w,
				http.StatusBadRequest,
				req.ID,
				-32022,
				"unsupported protocol version",
				map[string]any{
					"requested": p.ProtocolVersion,
					"supported": []string{
						ModernVersion,
						LegacyVersion,
					},
				},
			)
			return
		}

		result := map[string]any{
			"protocolVersion": LegacyVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    s.Name,
				"version": s.Version,
			},
		}

		// Deliberately do NOT send MCP-Session-Id.
		// This keeps the legacy side stateless.
		writeResult(w, r, req.ID, result)
		return
	}

	// Subsequent 2025 requests should carry MCP-Protocol-Version.
	if version := r.Header.Get("MCP-Protocol-Version"); version != LegacyVersion {
		writeJSONRPCError(
			w,
			http.StatusBadRequest,
			req.ID,
			-32022,
			"missing or invalid legacy MCP-Protocol-Version",
			map[string]any{
				"requested": version,
				"supported": []string{
					LegacyVersion,
				},
			},
		)
		return
	}

	var p ToolRequest
	if len(req.Params) > 0 {
		_ = json.Unmarshal(req.Params, &p)
	}

	s.dispatch(w, r, req, LegacyVersion, p)
}

func (s *McpServer) dispatch(
	w http.ResponseWriter,
	r *http.Request,
	req rpcRequest,
	version string,
	p ToolRequest,
) {
	switch req.Method {

	case "server/discover":
		if version != ModernVersion {
			writeJSONRPCError(
				w,
				http.StatusNotFound,
				req.ID,
				-32601,
				"method not found",
				nil,
			)
			return
		}

		result := map[string]any{
			"resultType": "complete",
			"supportedVersions": []string{
				ModernVersion,
				LegacyVersion,
			},
			"capabilities": map[string]any{
				"tools": map[string]any{}, // allTools, ?
			},
			"_meta": map[string]any{
				"io.modelcontextprotocol/serverInfo": map[string]any{
					"name":    s.Name,
					"version": s.Version,
				},
			},
			"instructions": "Minimal MCP test server.",
			"ttlMs":        3600000,
			"cacheScope":   "public",
		}

		writeResult(w, r, req.ID, result)

	case "tools/list":
		result := map[string]any{
			"tools": s.Registry.GetToolList(), // TODO: lock?
		}

		// 2026-07-28 requires these cache/result fields on list results.
		if version == ModernVersion {
			result["resultType"] = "complete"
			result["ttlMs"] = 60000
			result["cacheScope"] = "public"
		}

		writeResult(w, r, req.ID, result)

	case "tools/call": // TODO: move to McpServer.handleToolCall()
		res, err := s.Registry.Dispatch(r.Context(), &p)
		if err != nil {
			// Tool lookup/argument failures are normally returned as
			// JSON-RPC errors so the client can react to them.
			writeJSONRPCError(
				w,
				http.StatusOK,
				req.ID,
				-32602,
				"unknown tool",
				nil,
			)
			return
		}
		writeResult(w, r, req.ID, res)
		return

	default:
		// Unknown MCP method over Streamable HTTP is HTTP 404 + JSON-RPC -32601.
		writeJSONRPCError(
			w,
			http.StatusNotFound,
			req.ID,
			-32601,
			"method not found",
			nil,
		)
	}
}

// func (s *McpServer) handleToolCall(req *ToolRequest) {
// 	tool, ok := s.Registry.Dispatch(req)

// }

func (s *McpServer) setCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")

	if origin == "" || !s.allowedOrigin(origin) {
		return
	}

	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set(
		"Access-Control-Allow-Methods",
		"POST, OPTIONS",
	)
	w.Header().Set(
		"Access-Control-Allow-Headers",
		"Content-Type, MCP-Protocol-Version, Mcp-Method, Mcp-Name, Authorization",
	)
	w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
	w.Header().Add("Vary", "Origin")
}

func (s *McpServer) allowedOrigin(origin string) bool {
	return s.Origin[origin] || s.Origin["*"]
}

func acceptsStreamableHTTP(value string) bool {
	value = strings.ToLower(value)

	return strings.Contains(value, "application/json") &&
		strings.Contains(value, "text/event-stream")
}

func writeResult(
	w http.ResponseWriter,
	r *http.Request,
	id json.RawMessage,
	result any,
) {
	writeRPCStatus(
		w,
		http.StatusOK,
		rpcResponse{
			JSONRPC: JSONRPCVersion,
			ID:      id,
			Result:  result,
		},
	)
}

func writeRPCStatus(
	w http.ResponseWriter,
	status int,
	resp rpcResponse,
) {
	body, err := json.Marshal(resp)
	if err != nil {
		status = http.StatusInternalServerError

		body = []byte(fmt.Sprintf(
			`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":%q}}`,
			err.Error(),
		))
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writePlain(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}
