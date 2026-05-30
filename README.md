# dir2opds

dir2opds 可以把本機的書籍資料夾轉成 OPDS 書庫，並提供簡單的瀏覽器 HTML 介面。它適合個人書庫使用，讓你可以用 OPDS app、手機、平板、電子書閱讀器或瀏覽器，在區網內瀏覽與下載書籍。

## 功能

- 透過 HTTP 提供 OPDS 書庫。
- 提供一般瀏覽器可看的 HTML 介面。
- 提供 Windows GUI 啟動器。
- 可用命令列或 Docker 執行。
- 可依檔名搜尋書庫。
- 書庫只列出預設允許的書籍副檔名：
  - `.azw`、`.azw3`、`.azw4`
  - `.cbr`、`.cbz`、`.zip`
  - `.djv`、`.djvu`
  - `.epub`、`.fb2`、`.kepub`
  - `.kfx`、`.lit`、`.mobi`、`.prc`
  - `.pdf`
- 內容解析能力有限：書名/作者目前支援 EPUB、PDF；封面目前支援 EPUB、CBZ/ZIP、PDF。
- 產生的封面縮圖會放在書籍資料夾底下：

```text
books/thumb/
```

多書庫模式下，每個書庫會使用獨立的快取目錄，避免不同書庫的索引與封面互相覆蓋。

## 瀏覽方式

打開書庫根目錄時，dir2opds 會先掃描 books 資料夾，然後顯示排序選項：

- `By Name`
- `By Date Added`
- `By Type`

選擇 `By Name` 或 `By Date Added` 後，會顯示扁平化的書籍清單。選擇 `By Type` 後，會先顯示可用檔案類型；再選 `.epub`、`.pdf`、`.zip` 等類型後才列出書籍。真實資料夾路徑只會用在內部下載連結，不會顯示給使用者選擇。

搜尋會在輸入字串後直接顯示符合條件的書籍，不需要再選排序。

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

若要同時提供多個書庫，可以重複指定 `-library`。格式可以是路徑，或 `名稱=路徑`：

```bash
dir2opds -library "小說=/data/fiction" -library "漫畫=/data/comics" -port 8080 -enable-html -gzip -extract-metadata -search
```

多書庫模式下，首頁會先顯示各書庫；進入 `/小說/`、`/漫畫/` 後會沿用原本的排序、分頁、封面與下載行為。搜尋會跨所有指定書庫。

## Docker 使用方式

複製 Docker Compose 範本：

```bash
cp docker-compose.example.yml docker-compose.yml
```

視需要修改 volume：

```yaml
volumes:
  - ./books/fiction:/library/fiction
  - ./books/comics:/library/comics
  - ./books/magazines:/library/magazines
```

啟動：

```bash
docker compose up -d --build
```

在 Windows 開發環境中，建議從 WSL 的專案目錄執行 compose，避免 Windows/WSL 路徑解析不一致：

```bash
wsl bash -lc "cd /mnt/d/github/dir2opds && docker compose up -d --build dir2opds"
```

開啟：

```text
http://localhost:8080/
```

`docker-compose.yml` 已加入 git ignore，因此可以在裡面保留自己的本機路徑。

## 格式處理範圍

dir2opds 目前不是完整的電子書格式解析器。實際行為分成兩層：

會在書庫中列出並提供下載的副檔名：

```text
azw, azw3, azw4, cbr, cbz, djv, djvu, epub, fb2, kepub, kfx, lit, mobi, pdf, prc, zip
```

其他檔案可以留在資料夾中，但不會出現在書庫或搜尋結果裡。

會進一步解析內容的格式：

- 書名/作者 metadata：EPUB、PDF。
- 封面縮圖：EPUB、CBZ/ZIP、PDF。

例如 MOBI、AZW3、FB2、CBR 等格式目前只會列出與提供下載，不會抽取書名、作者或封面。

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
| `-library` | 要提供服務的書庫；可重複指定多次，格式為 `路徑` 或 `名稱=路徑`。指定後首頁會列出所有書庫。 |
| `-host` | 監聽位址。區網使用請設為 `0.0.0.0`。 |
| `-port` | HTTP port。 |
| `-enable-html` | 啟用瀏覽器 HTML 介面。 |
| `-gzip` | 壓縮回應內容。 |
| `-extract-metadata` | 擷取可支援格式的書名、作者與封面；書名/作者支援 EPUB/PDF，封面支援 EPUB、CBZ/ZIP、PDF。 |
| `-search` | 啟用 OPDS/OpenSearch 搜尋端點。 |
| `-sort` | 預設排序或分類方式，可用 `name`、`date`、`type`、`size`；`type` 會先顯示檔案類型清單。 |
| `-calibre` | 隱藏 Calibre 產生的 `metadata.opf`。 |
| `-hide-dot-files` | 隱藏以 `.` 開頭的檔案與資料夾。 |
| `-show-covers` | 將資料夾內的 `cover.jpg` 或 `folder.jpg` 作為 catalog cover。 |
| `-mime-map` | 自訂副檔名 MIME type，例如 `.mobi:application/x-mobipocket-ebook`。 |
| `-enable-cache` | 啟用 ETag 與 Last-Modified。 |
| `-no-cache` | 加上 no-cache headers。 |
| `-page-size` | 每頁筆數，最大 50。 |
| `-no-pagination` | 關閉分頁。 |
| `-cover-warmup` | 啟用 metadata 擷取時，背景預先抽取所有封面。預設關閉以降低 filesystem cache 壓力。 |
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
