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
├── Containerfile                   # 本機容器映像建置檔
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

目前的使用者流程刻意設計成「入口簡單、書單扁平」：

- 單書庫模式下，根目錄 `/` 先回傳排序選項：`By Name`、`By Date Added`、`By Type`。
- 多書庫模式下，根目錄 `/` 先回傳各書庫入口；`/?sort=name`、`/?sort=date` 可跨所有書庫顯示扁平化結果，`/?sort=type` 會跨所有書庫顯示類型入口。
- `?sort=name` 與 `?sort=date` 會遞迴掃描 trusted root，回傳扁平化書單。
- `?sort=type` 會先回傳可用檔案類型；`?sort=type&type=epub` 才回傳該類型的扁平化書單。
- 搜尋 `/search?q=term` 會直接回傳搜尋結果，不再要求使用者先選排序；若帶 `sort=type` 且未指定 `type`，則先回傳符合搜尋條件的類型清單。
- 真實相對路徑存放在 `CatalogEntry.Href`。
- 顯示名稱應只使用檔名或擷取出的 metadata title。
- 正常使用流程中，不應把資料夾路徑顯示成可選項目。

書庫列出/下載的副檔名白名單為：

```text
.azw, .azw3, .azw4, .cbr, .cbz, .djv, .djvu, .epub, .fb2, .kepub, .kfx, .lit, .mobi, .pdf, .prc, .zip
```

這份白名單只代表會出現在 catalog 並提供下載，不代表每個格式都有內容解析器。metadata 擷取目前只有 EPUB/PDF；封面擷取目前只有 EPUB、CBZ/ZIP、PDF。CBR 等其他格式會列出與下載，但不會解析內容。

## 封面

單書庫模式的封面縮圖快取位置：

```text
<trusted root>/thumb/
```

多書庫模式會以第一個書庫的 `thumb/libraries/<slug>/` 作為各書庫快取根目錄。

實作細節：

- 單書庫模式下，`OPDS.ThumbDir` 由 `internal/server` 設成 `<TrustedRoot>/thumb`。
- 多書庫模式下，每個書庫會透過 `OPDS.forLibrary` 使用獨立的 `ThumbDir`，避免封面和索引互相覆蓋。
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

## 建置 CLI（開發用途）

CLI 入口仍可用於開發與測試，但目前正式產出的 Windows 執行檔只產生 GUI 版 `dir2opds-gui.exe`。不要把 `dir2opds.exe` 當作發行產物。

目前平台測試：

```bash
go build .
```

全部 Makefile targets：

```bash
make build-all
```

## 建置 Windows GUI

GUI 啟動器只支援 Windows。它透過 Go `syscall` 呼叫 Win32 API，不需要 Fyne、Wails、Node.js 或 C toolchain。

目前發行 Windows 執行檔時，只產生這個 GUI 版本：

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
wsl bash -lc "cd /mnt/d/github/dir2opds && docker compose up -d --build dir2opds"
```

`docker-compose.yml` 已加入 git ignore，避免把本機絕對路徑提交出去。

在 Windows 開發環境重建 Docker 時，一律從 WSL 的 `/mnt/d/github/dir2opds` 執行 compose，避免 Windows/WSL 路徑解析不一致。

範本預設啟用：

```text
-enable-html
-gzip
-search
```

範本不啟用 `-enable-cache`，也不預設啟用 `-extract-metadata`；需要書名、作者或封面時再取消註解。

## 測試重點

修改行為時，請特別注意這些測試面：

- 根目錄排序選項。
- 搜尋直接列出結果。
- `By Type` 先列類型，再列選定類型的書。
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
3. 需要 Windows 執行檔時，只建置 `dir2opds-gui.exe`。
4. 不要產生或發佈 `dir2opds.exe`，除非明確需要非 GUI CLI 測試檔。
5. 確認 `docker-compose.yml`、`books/`、`thumb/`、`*.exe` 沒有被 staged。
6. 若 flags 或可見行為有變更，更新使用者文件。
