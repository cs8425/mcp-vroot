# mcp-vroot

繁體中文 ([README.zh.md](./README.zh.md)) | English

**mcp-vroot** is a [Model Context Protocol (MCP)](https://modelcontextprotocol.io) server written in Go. It exposes filesystem tools over Streamable HTTP and stdio (JSON-RPC 2.0), letting an LLM or MCP client list, search, read, and view (images / PDFs) files inside a controlled workspace root — and, in read-write mode, write, edit, and manage directories.

All file operations are performed through Go's `os.Root` API and are strictly confined to the configured root directory — they cannot escape to external paths (hence the "vroot" in the name).

> **Current development goal: to be used together with the built-in Web UI of llama.cpp.**
> The llama.cpp Web UI calls this MCP server directly from the browser (Streamable HTTP), so this server ships with built-in CORS and Origin allow-list support.

## Use Case: llama.cpp Web UI

1. Start this server, pointing `fs.path` at the data directory you want the model to read:

   ```sh
   ./mcp-vroot -c config.json
   # listens on 127.0.0.1:8765 by default
   ```

2. Start the llama.cpp server (its built-in Web UI is served by default, e.g. http://localhost:8080):

   ```sh
   llama-server <your-llama-server-flags>
   ```

3. Add this server in the Web UI's MCP settings, using the URL
   `http://127.0.0.1:8765` (any endpoint path works; `/mcp` is conventional).

4. Make sure `allow-origin` in `config.json` includes the Web UI's origin
   (e.g. `http://localhost:8080`, `http://127.0.0.1:8080`).

The model can then use tools such as `fs_list`, `fs_grep`, `fs_read`, and `fs_view`
through the Web UI to read text, PDFs, and images inside the workspace.

## Features

- **Streamable HTTP transport**: POST only, stateless (no sessions); supports both the modern `2026-07-28` and legacy `2025-11-25` MCP protocol revisions
- **stdio transport**: newline-delimited JSON-RPC over stdin/stdout (`-stdio` flag); the same two protocol revisions (modern requests are self-describing via `_meta`, legacy clients use the `initialize` handshake); the process exits when stdin is closed
- **Designed for browser-based clients**: CORS + Origin allow-list, so the llama.cpp Web UI can call it directly
- **Sandboxed**: all paths are relative to the workspace root; `os.Root` prevents path escape
- **Read-only / read-write modes**: read-only by default (`ro`); write tools must be explicitly enabled (`rw`)
- **PDF support**: built-in PDFium (WebAssembly, run via wazero, no CGO)
  - Table of contents (ToC / bookmarks)
  - Text extraction with pixel-position markers `<pos_left,top-right,bottom>:text` (great for tables)
  - Page rendering to PNG at 200 DPI (returned as a multimodal image)
- **Image support**: PNG / JPEG / GIF / BMP returned directly as base64; WebP is converted to PNG automatically
- **Zero CGO**: statically compiled; packs into a minimal `scratch` Docker image

## Requirements

- Go 1.27+
- Docker / BuildKit (for Docker deployment)

## Build & Run

### Run directly

```sh
go build -trimpath -ldflags="-s -w" -o mcp-vroot .
./mcp-vroot -c config.json
```

### Command-line flags

| Flag | Description | Default |
|------|-------------|---------|
| `-c` | Config file path | `config.json` |
| `-l` | Bind address (overrides `bind` in the config) | empty (use config) |
| `-stdio` | Serve MCP over stdio instead of starting the HTTP listener | `false` |
| `-v` | Log verbosity (higher = more detailed) | `3` |

If no config file is found, the server starts with built-in defaults: bind `127.0.0.1:8765`, workspace `./workspace`, read-only mode.

### Run in stdio mode

```sh
./mcp-vroot -c config.json -stdio
```

In stdio mode the server reads newline-delimited JSON-RPC messages from `stdin` and writes responses to `stdout`; logs go to `stderr` (so they never corrupt the message stream). No HTTP listener is started, and the process exits when `stdin` is closed. This is the transport for MCP clients that launch servers as subprocesses (e.g. Claude Desktop, Cursor, `mcp-cli`):

```json
{
  "mcpServers": {
    "vroot": {
      "command": "/path/to/mcp-vroot",
      "args": ["-c", "/path/to/config.json", "-stdio"]
    }
  }
}
```

### Docker

```sh
docker build -t mcp-vroot:latest -f Dockerfile .

docker run -it --rm -p 8765:8765 -u 1000:1000 \
  -v "$PWD/config.json:/config.json" \
  -v "$PWD/workspace:/workspace" \
  mcp-vroot:latest
```

## Configuration

The config file is JSON (see [`config.json`](./config.json) for an example):

```json
{
  "bind": "127.0.0.1:8765",
  "allow-origin": ["*", "http://127.0.0.1:8080", "http://localhost:8080"],
  "server-name": "mcp-vroot",
  "tool-prefix": "fs_",
  "fs": {
    "path": "./",
    "mode": "ro",
    "allow-tools": [],
    "deny-tools": []
  }
}
```

| Field | Description |
|-------|-------------|
| `bind` | Listen address |
| `allow-origin` | CORS / Origin allow-list (exact match; `*` allows all). Requests without an `Origin` header (non-browser clients) are always allowed |
| `server-name` | Server name (default `mcp-vroot`) |
| `tool-prefix` | Tool name prefix (e.g. `fs_` → `fs_list`) |
| `fs.path` | Workspace root directory (base for all tool paths) |
| `fs.mode` | `ro` (read-only, default) or `rw` (read-write; registers the write tools) |
| `fs.allow-tools` / `fs.deny-tools` | ⚠️ Work in progress, not enforced yet (see "Work in Progress") |

## Tools

### Read-only tools (available in both `ro` and `rw` modes)

| Tool | Description |
|------|-------------|
| `fs_list` | Lists / finds files under a path within N levels. Params: `path` (required), `pattern` (filename regex), `depth` (default 2, `-1` = unlimited), `offset`, `limit` (max 250) |
| `fs_grep` | Recursively searches file contents with a regex under a path and all subdirectories. Params: `pattern` (required), `path` (default `.`), `offset`, `limit` (max 250 lines). **For PDF files, matching page numbers are returned** |
| `fs_read` | Reads a file by line or by byte. Params: `path` (required), `mode` (`line` default / `byte`), `offset`, `length` (max 150 lines in line mode; max 1024 bytes in byte mode), `show_line` (line numbers in line mode, on by default). Binary content (NUL bytes) is rejected |
| `fs_view` | Views an image or a PDF. Params: `path` (required), `page` (PDF page, 1-based; `0` returns the ToC), `mode` (for PDF: `text` / `image`). Images are returned as multimodal base64; PDF `image` mode renders the page to PNG at 200 DPI. For tables or figure-heavy pages, it is recommended to read with both `text` and `image` modes |

### Write tools (registered only when `fs.mode = "rw"`)

| Tool | Description |
|------|-------------|
| `fs_write` | Writes to a file. Params: `path`, `content` (both required), `write_behavior`: `replace` (default) / `append` / `create` (fails if the file exists) |
| `fs_edit` | Line-based editing: exact-match replacements via `edits: [{old_text, new_text}]` (only the first occurrence is replaced); returns a git-style unified diff. `dry_run: true` previews the changes |
| `fs_mkdir` | Creates a directory (`MkdirAll`; an existing directory is not an error) |
| `fs_copy` | Copies files / directories (recursively). `dst` must not exist; timestamps, ownership, symlinks, etc. are not preserved |
| `fs_move` | Moves / renames. `dst` must not exist (avoids overwriting and losing data) |
| `fs_remove` | Removes a file, or a directory recursively |
| `fs_download` | Download a file from a http/https URL. Only domains in the `download-allow-domain` (JSON array) are permitted (full match). Default allowed domains: `cdn.jsdelivr.net`, `unpkg.com`, `esm.unpkg.com`, `cdnjs.cloudflare.com`, `esm.sh`. An empty array `[]` allows no domains; an array containing `*` allows any domain. |

> A debug `echo` tool is implemented but not registered by default (see `server.go`).

## Protocol Details

### Streamable HTTP

- Endpoint: any path, `POST` only (the modern Streamable HTTP revision dropped GET/DELETE)
- Requests must satisfy:
  - `Content-Type: application/json`
  - `Accept` must include both `application/json` and `text/event-stream`
  - Request body limit: 4 MB
- Supported methods:
  - `server/discover` (modern `2026-07-28` only)
  - `tools/list`
  - `tools/call`
  - `initialize` / `notifications/initialized` (legacy `2025-11-25` only)
  - `ping` (legacy `2025-11-25` utility, answered with an empty result; removed in `2026-07-28`)
- The modern revision requires headers and body to be consistent (`MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name` are cross-checked against `_meta`, `method`, and `params.name`)
- Legacy version negotiation: if a client advertises a version this server does not support, the `initialize` response returns `2025-11-25` (the latest version this server supports) and the client decides whether to continue
- Stateless: no `MCP-Session-Id` is issued

### stdio

- One JSON-RPC message per line on `stdin`/`stdout`; no embedded newlines; `stderr` is reserved for logs
- Same supported methods as HTTP (including `ping`, answered at any time); there is no header layer, so modern requests carry their metadata inline in `_meta` (the protocol version is required on every request)
- Legacy `2025-11-25` clients must perform the `initialize` handshake first; a request without an inline version before that is rejected with `-32600`
- Legacy version negotiation: an `initialize` advertising an older version is answered with `2025-11-25` (the latest version this server supports) instead of an error
- Cancellation via `notifications/cancelled {requestId, reason}`
- The server exits when `stdin` reaches EOF

### Example Request (curl)

```sh
curl -s http://127.0.0.1:8765/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/list' \
  -d '{
    "jsonrpc": "2.0",
    "id": 1,
    "method": "tools/list",
    "params": {
      "_meta": {
        "io.modelcontextprotocol/protocolVersion": "2026-07-28"
      }
    }
  }'
```

## Security

- **Sandbox**: all tool paths are relative to `fs.path`; `os.Root` blocks `..` and absolute-path escape
- **Read-only by default**: write tools must be explicitly enabled with `rw` mode
- **Origin validation**: exact allow-list matching for requests carrying an `Origin` header (DNS rebinding defense)
- **Recommendation**: this server has **no authentication**. Keep it bound to `127.0.0.1` or place it behind a reverse proxy / VPN; do not expose it directly to the public internet

## Testing

```sh
go test ./...
```

`server_test.go` covers: modern `server/discover`, modern `tools/call`, header/body consistency validation, the legacy `initialize` → `tools/call` flow, rejection of unknown origins, and CORS preflight. `TestStdio` covers the stdio transport: the modern `server/discover` / `tools/list` / `tools/call` flow, the legacy `initialize` → `tools/call` handshake, the not-initialized error, unknown methods, and prompt exit on stdin EOF.

## Project Structure

```
├── mcp.go             # JSON-RPC types, MCP protocol version constants
├── mcp_server.go      # Streamable HTTP handler, version dispatch (modern/legacy), CORS, shared dispatch core
├── mcp_stdio.go       # stdio transport (newline-delimited JSON-RPC, modern + legacy)
├── server.go          # main(), config parsing, tool registration
├── toolcall.go        # Tool Registry (Register / Dispatch)
├── tool_fs_list.go    # fs_list
├── tool_fs_grep.go    # fs_grep
├── tool_fs_read.go    # fs_read
├── tool_fs_view.go    # fs_view (images / PDF)
├── tool_fs_write.go   # fs_write
├── tool_fs_edit.go    # fs_edit
├── tool_fs_mkdir.go   # fs_mkdir
├── tool_fs_copy.go    # fs_copy
├── tool_fs_move.go    # fs_move
├── tool_fs_remove.go  # fs_remove
├── tool_echo.go       # echo debug tool (not registered by default)
├── pdf.go             # PDF handling (PDFium/WASM worker pool)
├── diff.go            # LCS unified diff (used by fs_edit)
├── utils.go           # Shared utilities (logging, path conversion, concurrency)
├── utils_msg.go       # MCP content part (text/image) serialization
├── jsonschema/        # JSON Schema definition types
├── config.json        # Example configuration
├── Dockerfile         # Multi-stage build → scratch image
└── server_test.go     # Protocol / HTTP tests
```

## Work in Progress

The following features are still in development and currently **do not take effect** or **have known limitations**:

| Item | Status |
|------|--------|
| `fs.allow-tools` / `fs.deny-tools` | Config fields are defined, but the tool-filtering logic is not implemented yet (`rw` mode registers all write tools) |
| `content_format: hex` for `fs_write` | Binary content writing is not implemented (code is commented out); only UTF-8 text is supported for now |
| SVG / AVIF images | Not supported (SVG conversion is commented out; WebP can be converted to PNG) |
| Tolerant matching in `fs_edit` | Exact match only; whitespace-tolerant line matching (`applyFlexibleReplace`) is not enabled; edits load the entire file into memory — large-file performance needs improvement |
| Atomic writes | `fs_write` does not use "temp file + rename"; an interrupted write may leave a partial file |
| Configurable file permissions | Written files are fixed at `0644` and directories at `0755`; no config option yet |
| Concurrency control | The `MaxConcurrent` utility is implemented but not yet applied to tool dispatch |
| Registry synchronization | Tool registration happens only at startup; the registry read lock is not enabled yet |
| Authentication | No user / token verification; relies on the bind address and the Origin allow-list only |

## License

Apache 2.0
