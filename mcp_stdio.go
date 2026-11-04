package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"
	"time"
)

// stdioServer serves the MCP protocol over stdin/stdout.
//
// Framing (spec 2026-07-28, basic/transports/stdio):
//   - one JSON-RPC message per line, no embedded newlines
//   - requests/notifications arrive on stdin, responses leave on stdout
//   - stdout must carry nothing but MCP messages; logs go to stderr
//   - the server exits promptly when stdin reaches EOF
//
// stdio has no header layer: modern (2026-07-28) requests carry their
// per-request metadata inline in _meta (the protocol version on every
// request), while legacy (2025-11-25) clients perform the initialize
// handshake once per process.
type stdioServer struct {
	server *McpServer

	// in/out are overridable for tests; default to os.Stdin/os.Stdout.
	in  io.Reader
	out io.Writer

	outMu sync.Mutex
	outBw *bufio.Writer

	mu      sync.Mutex
	version string // negotiated legacy version, "" until initialize

	inflightMu sync.Mutex
	inflight   map[string]context.CancelFunc

	wg sync.WaitGroup // in-flight request goroutines
}

func NewStdioServer(s *McpServer) *stdioServer {
	return &stdioServer{
		server:   s,
		in:       os.Stdin,
		out:      os.Stdout,
		inflight: make(map[string]context.CancelFunc),
	}
}

// Serve reads newline-delimited JSON-RPC messages from stdin until EOF and
// writes responses to stdout. It returns when stdin is closed, which is the
// primary graceful-shutdown signal for the stdio transport.
func (s *stdioServer) Serve() error {
	s.outMu.Lock()
	bw := s.outBw
	if bw == nil {
		bw = bufio.NewWriter(s.out)
		s.outBw = bw
	}
	s.outMu.Unlock()

	scanner := bufio.NewScanner(s.in)
	// A single message may be large (e.g. embedded file content);
	// keep the same 4 MiB ceiling as the HTTP body limit.
	scanner.Buffer(make([]byte, 64*1024), 4<<20)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		s.handleLine(line, bw)
	}

	// Give in-flight requests a bounded grace period to finish so their
	// responses are flushed before we exit (a client may close stdin right
	// after its last request). Then exit promptly, as the spec requires.
	finished := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
	}

	s.outMu.Lock()
	_ = bw.Flush()
	s.outMu.Unlock()
	return scanner.Err()
}

func (s *stdioServer) handleLine(line []byte, bw *bufio.Writer) {
	var msg rpcRequest
	if err := json.Unmarshal(line, &msg); err != nil {
		s.writeMessage(bw, rpcResponse{
			JSONRPC: JSONRPCVersion,
			Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
		})
		return
	}

	if msg.JSONRPC != JSONRPCVersion {
		s.writeMessage(bw, rpcResponse{
			JSONRPC: JSONRPCVersion,
			ID:      msg.ID,
			Error:   &rpcError{Code: -32600, Message: "invalid request: jsonrpc must be 2.0"},
		})
		return
	}

	// Notifications carry no ID and get no response.
	if len(msg.ID) == 0 {
		s.handleNotification(msg)
		return
	}

	// Requests run in their own goroutine so one slow tool cannot block
	// the read loop, and so they can be cancelled by ID.
	ctx, cancel := context.WithCancel(context.Background())
	s.track(msg.ID, cancel)
	s.wg.Add(1)

	go func() {
		defer cancel()
		defer s.untrack(msg.ID)
		defer s.wg.Done()

		resp := s.dispatchRequest(ctx, msg)

		// A cancelled request should not produce a response; clients are
		// expected to ignore late responses to cancelled requests.
		if ctx.Err() != nil {
			return
		}
		s.writeMessage(bw, resp)
	}()
}

// dispatchRequest applies stdio version semantics and runs one request.
//
// Modern (2026-07-28) requests are self-describing: the protocol version
// arrives in _meta on every request. Legacy (2025-11-25) clients perform
// the initialize handshake first; the negotiated version then applies to
// their subsequent requests, which carry no inline version.
func (s *stdioServer) dispatchRequest(ctx context.Context, msg rpcRequest) rpcResponse {
	switch msg.Method {

	case "server/discover":
		// The modern-only probe; also the backward-compatibility probe
		// a dual-version stdio client sends before any other request.
		var p ToolRequest
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &p)
		}
		if v := p.Meta.ProtocolVersion; v != "" && v != ModernVersion {
			return rpcErrorByID(msg.ID, -32022, "unsupported protocol version", map[string]any{
				"requested": v,
				"supported": []string{ModernVersion, LegacyVersion},
			})
		}
		return s.run(ctx, msg, ModernVersion, p)

	case "initialize":
		return s.handleInitialize(msg)

	case "ping":
		// Liveness utility (2025-11-25); allowed at any time, including
		// before initialize. (Removed again in 2026-07-28.)
		return rpcResponse{
			JSONRPC: JSONRPCVersion,
			ID:      msg.ID,
			Result:  map[string]any{},
		}

	case "tools/list", "tools/call":
		var p ToolRequest
		if len(msg.Params) > 0 {
			if err := json.Unmarshal(msg.Params, &p); err != nil {
				return rpcErrorByID(msg.ID, -32602, "invalid params", nil)
			}
		}

		// Self-describing modern request?
		if v := p.Meta.ProtocolVersion; v != "" {
			if v != ModernVersion {
				return rpcErrorByID(msg.ID, -32022, "unsupported protocol version", map[string]any{
					"requested": v,
					"supported": []string{ModernVersion, LegacyVersion},
				})
			}
			return s.run(ctx, msg, ModernVersion, p)
		}

		// Legacy request: requires the initialize handshake first.
		s.mu.Lock()
		version := s.version
		s.mu.Unlock()
		if version == "" {
			return rpcErrorByID(msg.ID, -32600, "not initialized: send initialize first", nil)
		}
		return s.run(ctx, msg, version, p)

	default:
		return rpcErrorByID(msg.ID, -32601, "method not found", nil)
	}
}

func (s *stdioServer) run(ctx context.Context, msg rpcRequest, version string, p ToolRequest) rpcResponse {
	out := s.server.dispatchCore(ctx, msg, version, p)
	if out.code != 0 {
		return rpcErrorByID(msg.ID, out.code, out.msg, out.data)
	}
	return rpcResponse{
		JSONRPC: JSONRPCVersion,
		ID:      msg.ID,
		Result:  out.result,
	}
}

func (s *stdioServer) handleInitialize(msg rpcRequest) rpcResponse {
	var p initializeParams
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return rpcErrorByID(msg.ID, -32602, "invalid initialize params", nil)
		}
	}

	// Version negotiation (2025-11-25): if the client does not request a
	// version we support, we still answer with the latest one we do; the
	// client decides whether to continue.
	if p.ProtocolVersion != LegacyVersion {
		Vln(2, "[stdio]initialize-version", p.ProtocolVersion, "->", LegacyVersion)
	}

	s.mu.Lock()
	s.version = LegacyVersion
	s.mu.Unlock()

	return rpcResponse{
		JSONRPC: JSONRPCVersion,
		ID:      msg.ID,
		Result: map[string]any{
			"protocolVersion": LegacyVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{},
			},
			"serverInfo": map[string]any{
				"name":    s.server.Name,
				"version": s.server.Version,
			},
			"instructions": "Minimal MCP test server.",
		},
	}
}

func (s *stdioServer) handleNotification(msg rpcRequest) {
	switch msg.Method {
	case "notifications/initialized":
		// Legacy handshake completion; nothing to do.

	case "notifications/cancelled":
		var p struct {
			RequestID json.RawMessage `json:"requestId"`
			Reason    string          `json:"reason"`
		}
		if len(msg.Params) > 0 {
			_ = json.Unmarshal(msg.Params, &p)
		}
		if len(p.RequestID) == 0 {
			return // malformed; ignore
		}

		s.inflightMu.Lock()
		cancel, ok := s.inflight[string(p.RequestID)]
		s.inflightMu.Unlock()
		if !ok {
			return // unknown or already finished; ignore
		}

		Vln(2, "[stdio]cancelled", string(p.RequestID), p.Reason)
		cancel()

	default:
		// Unknown notifications are fire-and-forget; ignore.
	}
}

func (s *stdioServer) track(id json.RawMessage, cancel context.CancelFunc) {
	s.inflightMu.Lock()
	s.inflight[string(id)] = cancel
	s.inflightMu.Unlock()
}

func (s *stdioServer) untrack(id json.RawMessage) {
	s.inflightMu.Lock()
	delete(s.inflight, string(id))
	s.inflightMu.Unlock()
}

// writeMessage writes one response as a single line. json.Marshal never
// emits newlines, so the no-embedded-newlines rule holds.
func (s *stdioServer) writeMessage(bw *bufio.Writer, resp rpcResponse) {
	body, err := json.Marshal(resp)
	if err != nil {
		Vln(2, "[stdio]marshal-err", err)
		return
	}

	s.outMu.Lock()
	defer s.outMu.Unlock()

	_, werr := bw.Write(append(body, '\n'))
	ferr := bw.Flush()
	if werr != nil || ferr != nil {
		Vln(2, "[stdio]write-err", werr, ferr)
	}
}
