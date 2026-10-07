# mcp-vroot

[English](./README.md) | 繁體中文

**mcp-vroot** 是以 Go 撰寫的 [MCP (Model Context Protocol)](https://modelcontextprotocol.io) 伺服器，透過 Streamable HTTP（JSON-RPC 2.0）提供檔案系統工具，讓 LLM 或 MCP Client 能在受控的 workspace 根目錄內列出、搜尋、讀取、檢視（圖片 / PDF）檔案，並在讀寫模式下進行寫入、編輯與目錄管理。

所有檔案操作皆透過 Go 的 `os.Root` API 執行，被嚴格限制在設定的根目錄內，無法逃逸到外部路徑（即名稱中 "vroot" 的由來）。

> **目前開發目的：搭配 llama.cpp 內建的 Web UI 使用。**
> llama.cpp Web UI 會由瀏覽器直接呼叫本 MCP 伺服器（Streamable HTTP），因此本伺服器內建 CORS 與 Origin allow-list 支援。

## 使用情境：搭配 llama.cpp Web UI

1. 啟動本伺服器，將 `fs.path` 指向希望讓模型讀取的資料目錄：

   ```sh
   ./mcp-vroot -c config.json
   # 預設監聽 127.0.0.1:8765
   ```

2. 啟動 llama.cpp server（內建 Web UI，預設 http://localhost:8080）：

   ```sh
   llama-server <your-llama-server-flags>
   ```

3. 在 Web UI 的 MCP 設定中新增本伺服器，URL 填入 `http://127.0.0.1:8765`
   （端點路徑可任意，慣用 `/mcp`）。

4. 確認 `config.json` 的 `allow-origin` 包含 Web UI 的 origin
   （例如 `http://localhost:8080`、`http://127.0.0.1:8080`）。

完成後，模型即可透過 Web UI 呼叫 `fs_list`、`fs_grep`、`fs_read`、`fs_view` 等工具，
閱讀 workspace 內的文字、PDF 與圖片。

## 特色

- **Streamable HTTP 傳輸**：POST only、stateless（無 session），同時支援 modern `2026-07-28` 與 legacy `2025-11-25` 兩個 MCP 協定版本
- **為瀏覽器端 client 設計**：CORS + Origin allow-list，可被 llama.cpp Web UI 直接呼叫
- **沙箱化**：所有路徑均相對於 workspace 根目錄，`os.Root` 防止路徑逃逸
- **唯讀 / 讀寫模式**：預設唯讀（`ro`），寫入工具需在設定中明確開啟（`rw`）
- **PDF 支援**：內建 PDFium（WebAssembly，經 wazero 執行，無需 CGO）
  - 目錄（ToC / Bookmarks）
  - 文字抽取（含像素位置標記 `<pos_left,top-right,bottom>:text`，適合表格）
  - 頁面轉圖（200 DPI PNG，多模態回傳）
- **圖片支援**：PNG / JPEG / GIF / BMP 直接以 base64 回傳；WebP 自動轉換為 PNG
- **零 CGO**：靜態編譯，可打包為極小的 scratch Docker 映像

## 需求

- Go 1.27+
- （Docker 部署時）Docker / BuildKit

## 建置與執行

### 直接執行

```sh
go build -trimpath -ldflags="-s -w" -o mcp-vroot .
./mcp-vroot -c config.json
```

### 命令列參數

| Flag | 說明 | 預設值 |
|------|------|--------|
| `-c` | 設定檔路徑 | `config.json` |
| `-l` | 監聽位址（覆寫設定檔的 `bind`） | 空（使用設定檔） |
| `-v` | 日誌詳細等級（數字越大越詳細） | `3` |

若找不到設定檔，伺服器會以內建預設值啟動：綁定 `127.0.0.1:8765`、workspace `./workspace`、唯讀模式。

### Docker

```sh
docker build -t mcp-vroot:latest -f Dockerfile .

docker run -it --rm -p 8765:8765 -u 1000:1000 \
  -v "$PWD/config.json:/config.json" \
  -v "$PWD/workspace:/workspace" \
  mcp-vroot:latest
```

## 設定

設定檔為 JSON（範例見 [`config.json`](./config.json)）：

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

| 欄位 | 說明 |
|------|------|
| `bind` | 監聽位址 |
| `allow-origin` | CORS / Origin 允許清單（精確比對，`*` 代表全部允許）；無 `Origin` 標頭的請求（非瀏覽器 client）一律允許 |
| `server-name` | 伺服器名稱（預設 `mcp-vroot`） |
| `tool-prefix` | 工具名稱前綴（如 `fs_` → `fs_list`） |
| `fs.path` | workspace 根目錄（所有工具路徑的基準） |
| `fs.mode` | `ro`（唯讀，預設）或 `rw`（讀寫，額外註冊寫入工具） |
| `fs.allow-tools` / `fs.deny-tools` | ⚠️ 開發中，目前尚未生效（見「開發中功能」） |

## 提供的工具

### 唯讀工具（`ro` / `rw` 模式皆可用）

| 工具 | 說明 |
|------|------|
| `fs_list` | 列出 / 尋找指定路徑下 N 層內符合條件的檔案。參數：`path`（必要）、`pattern`（檔名 regex）、`depth`（預設 2，`-1` 不限）、`offset`、`limit`（最大 250） |
| `fs_grep` | 以 regex 在指定路徑及所有子目錄遞迴搜尋檔案內容。參數：`pattern`（必要）、`path`（預設 `.`）、`offset`、`limit`（最大 250 行）。**PDF 檔會回傳符合的頁數** |
| `fs_read` | 以行或位元組讀取檔案。參數：`path`（必要）、`mode`（`line` 預設 / `byte`）、`offset`、`length`（line 模式最大 150 行；byte 模式最大 1024 bytes）、`show_line`（line 模式行號，預設開啟）。偵測到二進位內容會拒絕回傳 |
| `fs_view` | 檢視圖片或 PDF。參數：`path`（必要）、`page`（PDF 頁碼，1 起算；`0` 回傳 ToC）、`mode`（PDF 用：`text` / `image`）。圖片以多模態（base64）回傳；PDF `image` 模式以 200 DPI 渲染為 PNG。遇到表格或圖片內容，建議 `text` 與 `image` 各讀取一次 |

### 寫入工具（僅 `fs.mode = "rw"` 時註冊）

| 工具 | 說明 |
|------|------|
| `fs_write` | 寫入檔案。參數：`path`、`content`（皆必要）、`write_behavior`：`replace`（預設）/ `append` / `create`（存在即失敗） |
| `fs_edit` | 行基礎編輯：以 `edits: [{old_text, new_text}]` 做精確比對取代（僅取代第一處），回傳 git 風格 unified diff。`dry_run: true` 可預覽變更 |
| `fs_mkdir` | 建立目錄（`MkdirAll`，已存在不視為錯誤） |
| `fs_copy` | 複製檔案 / 目錄（遞迴）。`dst` 不得已存在；不保留時間戳、擁有者、symlink 等屬性 |
| `fs_move` | 移動 / 重新命名。`dst` 不得已存在（避免覆寫丟資料） |
| `fs_remove` | 移除檔案，或遞迴移除目錄 |
| `fs_download` | 從 http/https URL 下載檔案。僅允許 `download-allow-domain` (JSON 陣列) 中定義的網域（完整匹配）。預設允許網域：`cdn.jsdelivr.net`, `unpkg.com`, `esm.unpkg.com`, `cdnjs.cloudflare.com`, `esm.sh`。空陣列 `[]` 不允許任何網域；包含 `*` 的陣列則允許任何網域。 |

> 除錯用的 `echo` 工具已實作但未註冊（見 `server.go`）。

## 協定細節

- 端點：任意路徑，僅接受 `POST`（modern 版本的 Streamable HTTP 已移除 GET/DELETE）
- 請求需符合：
  - `Content-Type: application/json`
  - `Accept` 需同時包含 `application/json` 與 `text/event-stream`
  - 請求 body 上限 4 MB
- 支援的方法：
  - `server/discover`（僅 modern `2026-07-28`）
  - `tools/list`
  - `tools/call`
  - `initialize` / `notifications/initialized`（僅 legacy `2025-11-25`）
- modern 版本要求 header 與 body 一致（`MCP-Protocol-Version`、`Mcp-Method`、`Mcp-Name` 與 `_meta`、`method`、`params.name` 相互驗證）
- stateless：不發行 `MCP-Session-Id`

### 呼叫範例（curl）

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

## 安全性

- **沙箱**：所有工具路徑都相對 `fs.path`，`os.Root` 阻止 `..` 與絕對路徑逃逸
- **預設唯讀**：寫入工具需明確以 `rw` 模式開啟
- **Origin 驗證**：對攜帶 `Origin` 的請求做 allow-list 精確比對（防 DNS rebinding）
- **建議**：本伺服器**無鑑權機制**，請保持綁定 `127.0.0.1` 或置於反向代理 / VPN 之後，勿直接暴露於公網

## 測試

```sh
go test ./...
```

`server_test.go` 涵蓋：modern `server/discover`、modern `tools/call`、header/body 一致性驗證、legacy `initialize` → `tools/call` 流程、未知 Origin 拒絕、CORS preflight。

## 專案結構

```
├── mcp.go             # JSON-RPC 型別、MCP 協定版本常數
├── mcp_server.go      # Streamable HTTP handler、版本分流（modern/legacy）、CORS
├── server.go          # main()、設定解析、工具註冊
├── toolcall.go        # Tool Registry（Register / Dispatch）
├── tool_fs_list.go    # fs_list
├── tool_fs_grep.go    # fs_grep
├── tool_fs_read.go    # fs_read
├── tool_fs_view.go    # fs_view（圖片 / PDF）
├── tool_fs_write.go   # fs_write
├── tool_fs_edit.go    # fs_edit
├── tool_fs_mkdir.go   # fs_mkdir
├── tool_fs_copy.go    # fs_copy
├── tool_fs_move.go    # fs_move
├── tool_fs_remove.go  # fs_remove
├── tool_echo.go       # echo 除錯工具（預設未註冊）
├── pdf.go             # PDF 處理（PDFium/WASM worker pool）
├── diff.go            # LCS unified diff（fs_edit 用）
├── utils.go           # 共用工具（日誌、路徑轉換、併發控制）
├── utils_msg.go       # MCP content part（text/image）序列化
├── jsonschema/        # JSON Schema 定義型別
├── config.json        # 範例設定
├── Dockerfile         # 多階段建置 → scratch 映像
└── server_test.go     # 協定 / HTTP 測試
```

## 開發中 / 未完成功能

以下功能尚在開發中，目前**不生效**或**有已知限制**：

| 項目 | 狀態 |
|------|------|
| `fs.allow-tools` / `fs.deny-tools` | 設定欄位已定義，但工具過濾邏輯尚未實作（`rw` 模式會註冊全部寫入工具） |
| `fs_write` 的 `content_format: hex` | 二進位內容寫入尚未實作（程式碼已註解），目前僅支援 UTF-8 文字 |
| SVG / AVIF 圖片 | 尚未支援（SVG 轉換已註解；WebP 可轉為 PNG） |
| `fs_edit` 寬容比對 | 僅精確比對；whitespace 寬容的逐行比對（`applyFlexibleReplace`）尚未啟用；編輯會將整檔載入記憶體，大檔效能待改善 |
| 原子寫入 | `fs_write` 未使用「暫存檔 + rename」方式，中斷時可能留下不完整檔案 |
| 檔案權限設定 | 寫入檔固定 `0644`、目錄固定 `0755`，尚無設定選項 |
| 併發控制 | `MaxConcurrent` 工具已實作但尚未套用至工具 dispatch |
| Registry 同步 | 工具註冊僅發生在啟動階段，Registry 讀取鎖尚未啟用 |
| 鑑權 | 無使用者 / token 驗證，僅依賴綁定位址與 Origin allow-list |

## License

Apache 2.0

