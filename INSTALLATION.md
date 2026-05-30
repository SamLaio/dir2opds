# 安裝說明

目前正式提供的安裝方式只有兩種：

- Docker：從本 repository 的 `Containerfile` 本機建置映像檔。
- Windows `.exe`：使用發行檔中的 Windows GUI 執行檔。

目前不提供 Linux、macOS、FreeBSD、Raspberry Pi 等平台的預先編譯執行檔，也不提供 Docker Hub 或其他遠端 registry 的版控映像檔。

## Docker

複製 Docker Compose 範本：

```bash
cp docker-compose.example.yml docker-compose.yml
```

依照自己的書庫位置修改 `docker-compose.yml` 的 `volumes`。

多書庫範例：

```yaml
volumes:
  - ./books/fiction:/library/fiction
  - ./books/comics:/library/comics
  - ./books/magazines:/library/magazines

command:
  - -library
  - Fiction=/library/fiction
  - -library
  - Comics=/library/comics
  - -library
  - Magazines=/library/magazines
```

單書庫範例：

```yaml
volumes:
  - ./books:/books

command:
  - -dir
  - /books
```

啟動或重建：

```bash
docker compose up -d --build dir2opds
```

在 Windows 開發環境中，請從 WSL 的專案目錄執行 compose，避免 Windows/WSL 路徑解析不一致：

```bash
wsl bash -lc "cd /mnt/d/github/dir2opds && docker compose up -d --build dir2opds"
```

啟動後開啟：

```text
http://localhost:8080/
```

`docker-compose.yml` 已加入 git ignore，可以保留自己的本機路徑。

## Windows `.exe`

從發行檔取得 Windows 版本後，使用 GUI 啟動器：

```text
dir2opds-gui.exe
```

開啟後選擇 books 資料夾，確認 port，按下 `Start`。

若要讓同一個區網內的手機、平板或電子書閱讀器連線，請確認 Windows 防火牆允許所選 port 的 TCP inbound 連線。

本機瀏覽：

```text
http://127.0.0.1:8080/
```

區網裝置請使用 Windows 電腦的區網 IP：

```text
http://192.168.x.x:8080/
```

## 從原始碼自行建置

其他平台若需要執行檔，請自行從原始碼建置。這屬於開發者流程，不是目前提供的正式安裝包。

需要 [Go 1.25.3+](https://go.dev/doc/install)。

```bash
go build .
```

更多建置資訊請見 [README.DEVELOPMENT.md](README.DEVELOPMENT.md)。
