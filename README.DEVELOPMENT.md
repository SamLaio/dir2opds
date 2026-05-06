# dir2opds 開發文件

這份文件提供給開發者使用。

## 專案結構

```text
.
├── main.go                         # CLI 入口
├── cmd/dir2opds-gui/               # Windows GUI 啟動器
├── internal/server/server.go       # CLI 和 GUI 共用的 HTTP server wiring
├── internal/service/               # OPDS、HTML、搜尋、封面、gzip、health
├── opds/                           # Atom/OPDS builder helpers
├── Containerfile                   # Docker image
├── docker-compose.example.yml      # 本機 Docker 範本
├── Makefile
└── README.md                       # 一般使用者文件
```

## 執行架構

共用的 server 初始化邏輯在 `internal/server`。

流程：

```text
CLI 或 GUI
  -> internal/server.Config
  -> internal/server.NewHTTPServer
  -> internal/service.OPDS handlers
  -> OPDS XML 或 HTML response
```

主要 handler：

- `/` 使用 `OPDS.Handler`。
- `/health` 使用 `service.HealthHandler`。
- `/search` 在啟用搜尋時使用 `OPDS.SearchHandler`。
- `/opensearch.xml` 在啟用搜尋時使用 `OPDS.OpenSearchHandler`。
- `/cover` 在啟用 metadata 擷取時使用 `OPDS.CoverHandler`。

## 書庫行為

目前的使用者流程刻意設計成扁平化：

- 根目錄 `/` 只先回傳排序選項：`By Name` 與 `By Date Added`。
- `/?sort=name` 與 `/?sort=date` 會遞迴掃描整個 trusted root，回傳扁平化書單。
- 搜尋也使用同樣流程：
  - `/search?q=term` 回傳排序選項。
  - `/search?q=term&sort=name` 或 `sort=date` 回傳扁平化搜尋結果。
- 真實相對路徑存放在 `CatalogEntry.Href`。
- 顯示名稱應只使用檔名或擷取出的 metadata title。
- 正常使用流程中，不應把資料夾路徑顯示成可選項目。

支援顯示的格式限制為：

```text
.epub, .cbz, .zip, .pdf
```

## 封面

封面縮圖快取位置：

```text
<trusted root>/thumb/
```

實作細節：

- `OPDS.ThumbDir` 由 `internal/server` 設成 `<TrustedRoot>/thumb`。
- `thumb` 會被 catalog scanning 忽略；舊版 `.thumb` 也會被跳過，避免舊快取被掃進書庫。
- EPUB 封面擷取支援：
  - OPF `meta name="cover" content="..."`
  - EPUB3 `properties="cover-image"`
  - cover XHTML 圖片連結
  - 常見 cover 檔名與第一張圖片 fallback
- CBZ 和 ZIP 共用 `extractFirstImageFromZip`。
- PDF 使用最佳努力的內嵌 JPEG/PNG byte extraction；不會 render PDF 第一頁。

## 建置與測試

使用 Go 1.25.3 或更新版本。

執行全部測試：

```bash
go test ./...
```

格式化：

```bash
go fmt ./...
```

執行 vet：

```bash
go vet ./...
```

Makefile 預設流程：

```bash
make
```

## 建置 CLI

目前平台：

```bash
go build .
```

Windows CLI：

```bash
GOOS=windows GOARCH=amd64 go build -o dir2opds.exe .
```

全部 Makefile targets：

```bash
make build-all
```

## 建置 Windows GUI

GUI 啟動器只支援 Windows。它透過 Go `syscall` 呼叫 Win32 API，不需要 Fyne、Wails、Node.js 或 C toolchain。

在 Linux/macOS/WSL 交叉編譯：

```bash
GOOS=windows GOARCH=amd64 go build -ldflags -H=windowsgui -o dir2opds-gui.exe ./cmd/dir2opds-gui
```

在 PowerShell 建置：

```powershell
$env:GOOS="windows"
$env:GOARCH="amd64"
go build -ldflags "-H=windowsgui" -o dir2opds-gui.exe ./cmd/dir2opds-gui
```

產生的 `dir2opds-gui.exe` 不會被 git 追蹤，因為 `.gitignore` 已忽略 `*.exe`。

## Docker

使用範本：

```bash
cp docker-compose.example.yml docker-compose.yml
docker compose up -d --build
```

`docker-compose.yml` 已加入 git ignore，避免把本機絕對路徑提交出去。

範本預設啟用：

```text
-enable-html
-gzip
-extract-metadata
```

範本不啟用 `-enable-cache`。

## 測試重點

修改行為時，請特別注意這些測試面：

- 根目錄排序選項。
- 搜尋排序選項。
- 遞迴掃描後的扁平化書單。
- 隱藏路徑行為：真實路徑只在 `Href`，不可出現在可見標題。
- 支援副檔名過濾。
- EPUB 各種封面格式。
- CBZ/ZIP 第一張圖片擷取。
- PDF 內嵌圖片擷取。
- `verifyPath` 路徑穿越防護。
- 檔案下載的 Range requests。

## 程式風格

- `internal/service` 的對外行為要有測試覆蓋。
- 使用 `log/slog` 做 structured logging。
- 優先使用標準庫 parser 與 path helpers，不要只靠字串拼接處理路徑。
- 所有來自 request input 的檔案系統存取都必須保留 `verifyPath` 檢查。
- 不要在 user-facing title 暴露真實檔案系統路徑。
- 若沒有明確理由，不要為 GUI 或封面 rendering 加入大型依賴。

## 主要依賴

- `github.com/lann/builder`：immutable OPDS builder helpers。
- `golang.org/x/tools/blog/atom`：Atom structs。
- `github.com/stretchr/testify`：測試工具。

## 發行檢查清單

1. 執行 `go fmt ./...`。
2. 執行 `go test ./...`。
3. 視需要建置 CLI。
4. 視需要建置 Windows GUI。
5. 確認 `docker-compose.yml`、`books/`、`thumb/`、`*.exe` 沒有被 staged。
6. 若 flags 或可見行為有變更，更新使用者文件。
