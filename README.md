# dir2opds

dir2opds 可以把本機的書籍資料夾轉成 OPDS 書庫，並提供簡單的瀏覽器 HTML 介面。它適合個人書庫使用，讓你可以用 OPDS app、手機、平板、電子書閱讀器或瀏覽器，在區網內瀏覽與下載書籍。

## 功能

- 透過 HTTP 提供 OPDS 書庫。
- 提供一般瀏覽器可看的 HTML 介面。
- 提供 Windows GUI 啟動器。
- 可用命令列或 Docker 執行。
- 可依檔名搜尋書庫。
- 只顯示支援的書籍格式：
  - `.epub`
  - `.cbz`
  - `.zip`
  - `.pdf`
- 會盡可能產生封面縮圖。
- 產生的封面縮圖會放在書籍資料夾底下：

```text
books/thumb/
```

## 瀏覽方式

打開書庫根目錄時，dir2opds 會先掃描 books 資料夾，然後只顯示兩個選項：

- `By Name`
- `By Date Added`

選擇其中一個後，才會顯示扁平化的書籍清單。真實資料夾路徑只會用在內部下載連結，不會顯示給使用者選擇。

搜尋也是同樣流程：

1. 輸入搜尋字串。
2. 選擇 `By Name` 或 `By Date Added`。
3. 顯示符合條件的書籍。

## Windows GUI

如果不想使用命令列，可以直接使用 `dir2opds-gui.exe`。

1. 開啟 `dir2opds-gui.exe`。
2. 選擇 books 資料夾。
3. port 可保留預設 `8080`，也可以自行修改。
4. 按下 `Start`。
5. 用瀏覽器開啟，或把 OPDS URL 加到閱讀器。

GUI 會監聽所有網路介面，因此同一個區網內的其他裝置也可以連線。

同一台 Windows 電腦可使用：

```text
http://127.0.0.1:8080/
```

手機、平板或電子書閱讀器請使用 Windows 電腦的區網 IP：

```text
http://192.168.x.x:8080/
```

如果其他裝置無法連線，請檢查 Windows 防火牆，允許所選 port 的 TCP inbound 連線。

GUI 預設啟用：

- HTML 介面
- gzip
- metadata 與封面擷取

GUI 不啟用 HTTP conditional cache，這樣瀏覽器重新整理時比較不會沿用舊畫面。

## 命令列使用方式

直接啟動 server：

```bash
dir2opds -dir /path/to/books -port 8080 -enable-html -gzip -extract-metadata
```

若要讓區網內其他裝置連線：

```bash
dir2opds -host 0.0.0.0 -dir /path/to/books -port 8080 -enable-html -gzip -extract-metadata
```

若只允許本機連線：

```bash
dir2opds -host 127.0.0.1 -dir /path/to/books -port 8080 -enable-html -gzip -extract-metadata
```

## Docker 使用方式

複製 Docker Compose 範本：

```bash
cp docker-compose.example.yml docker-compose.yml
```

視需要修改 volume：

```yaml
volumes:
  - ./books:/books
```

啟動：

```bash
docker compose up -d --build
```

開啟：

```text
http://localhost:8080/
```

`docker-compose.yml` 已加入 git ignore，因此可以在裡面保留自己的本機路徑。

## 支援格式

dir2opds 只會列出：

```text
epub, cbz, zip, pdf
```

其他檔案可以留在資料夾中，但不會出現在書庫或搜尋結果裡。

## 封面

啟用 metadata 擷取後，dir2opds 會產生封面縮圖。

封面處理方式：

- EPUB：讀取 OPF cover metadata、`cover-image` 屬性、cover XHTML 裡的圖片連結，最後 fallback 到第一張圖片。
- CBZ/ZIP：使用壓縮檔裡的第一張圖片。
- PDF：盡可能擷取第一張內嵌 JPEG 或 PNG 圖片。

產生的縮圖會放在：

```text
<books folder>/thumb/
```

如果封面看起來是舊的，可以停止 server 後刪除 `thumb`，下次會重新產生。

## 常用參數

| 參數 | 用途 |
| --- | --- |
| `-dir` | 要提供服務的 books 資料夾。 |
| `-host` | 監聽位址。區網使用請設為 `0.0.0.0`。 |
| `-port` | HTTP port。 |
| `-enable-html` | 啟用瀏覽器 HTML 介面。 |
| `-gzip` | 壓縮回應內容。 |
| `-extract-metadata` | 擷取 metadata 與封面。 |
| `-search` | 啟用 OPDS/OpenSearch 搜尋端點。 |
| `-enable-cache` | 啟用 ETag 與 Last-Modified。 |
| `-no-cache` | 加上 no-cache headers。 |
| `-page-size` | 每頁筆數，最大 200。 |
| `-no-pagination` | 關閉分頁。 |
| `-url` | OPDS 絕對連結用的 base URL。 |
| `-log-format` | `json` 或 `text`。 |
| `-debug` | 啟用 debug log。 |

## 健康檢查

```text
GET /health
```

回應：

```json
{"status":"ok"}
```

## 開發者文件

請見 [README.DEVELOPMENT.md](README.DEVELOPMENT.md)。
