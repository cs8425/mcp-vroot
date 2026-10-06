package main

import (
	"encoding/json"
	"net/http"
)

const (
	JSONRPCVersion = "2.0"

	// Current MCP revision.
	ModernVersion = "2026-07-28"

	// Older revision that also used Streamable HTTP.
	LegacyVersion = "2025-11-25"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type requestMeta struct {
	ProtocolVersion    string         `json:"io.modelcontextprotocol/protocolVersion"`
	ClientInfo         map[string]any `json:"io.modelcontextprotocol/clientInfo,omitempty"`
	ClientCapabilities map[string]any `json:"io.modelcontextprotocol/clientCapabilities"`
}

type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      map[string]any `json:"clientInfo"`
}

func writeJSONRPCError(
	w http.ResponseWriter,
	status int,
	id json.RawMessage,
	code int,
	message string,
	data any,
) {
	writeRPCStatus(
		w,
		status,
		rpcResponse{
			JSONRPC: JSONRPCVersion,
			ID:      id,
			Error: &rpcError{
				Code:    code,
				Message: message,
				Data:    data,
			},
		},
	)
}
