# internal/service - OPDS Service Layer

Core business logic for OPDS catalog generation, HTML rendering, search, cover extraction, and HTTP handling.

## WHERE TO LOOK

| Task | File | Key Functions |
|------|------|----------------|
| Add new HTTP endpoint | `service.go` | `Handler()`, `SearchHandler()`, `OpenSearchHandler()`, `CoverHandler()` |
| Modify catalog generation | `service.go` | `Scan()`, `makeFeed()`, `makeRootSortCatalog()`, `makeTypeSelectionCatalog()` |
| Modify multi-library behavior | `service.go` | `multiLibraryHandler()`, `forLibrary()`, `collectMultiBookCatalog()` |
| Modify HTML rendering | `html.go` | `renderHTML()`, `htmlBreadcrumbs()` |
| Add metadata extraction | `service.go` | `extractMetadata()`, `extractEpubMetadata()`, `extractPdfMetadata()` |
| Add cover extraction | `service.go` | `extractCoverForFile()`, `extractEpubCover()`, `extractFirstImageFromZip()`, `extractPDFCover()` |
| Add middleware | `gzip.go` | `GzipMiddleware()` |
| Security/path validation | `service.go` | `verifyPath()`, `inTrustedRoot()` |
| Pagination logic | `service.go` | `parsePage()`, `pageSize()` |
| Caching/ETag | `service.go` | `etag()` |

## KEY STRUCTS

```go
type OPDS struct {
    TrustedRoot      string
    ThumbDir         string
    Libraries        []Library
    HideCalibreFiles bool
    HideDotFiles     bool
    NoCache          bool
    EnableCache      bool
    SortBy           string // name, date, type, size
    ShowCovers       bool
    MimeMap          map[string]string
    TypeFilter       string
    EnableSearch     bool
    ExtractMetadata  bool
    CoverWarmup      bool
    EnableHTML       bool
    URLPrefix        string
    BaseURL          string
    PageSize         int
    NoPagination     bool
}

type Catalog struct {
    ID       string
    Title    string
    Type     int
    Entries  []CatalogEntry
    Cover    string
    Total    int
    Page     int
    PageSize int
    ModTime  time.Time
}
```

## CURRENT USER FLOWS

- Single-library `/` returns root choices: `By Name`, `By Date Added`, and `By Type`.
- Multi-library `/` returns library entries unless a root sort is requested.
- `?sort=name` and `?sort=date` recursively flatten books into a book list.
- `?sort=type` first lists available file types; `?sort=type&type=epub` lists matching books.
- `/search?q=term` returns matching books directly using the configured/default sort.
- `/search?q=term&sort=type` returns type choices unless a `type` query is also present.
- Visible titles should use file names or extracted metadata titles, not full filesystem paths.

## SUPPORTED FILE BEHAVIOR

Catalog listing and downloads are limited to:

```text
.azw, .azw3, .azw4, .cbr, .cbz, .djv, .djvu, .epub, .fb2, .kepub, .kfx, .lit, .mobi, .pdf, .prc, .zip
```

This whitelist only means the file appears in catalogs and can be downloaded. Content parsing is narrower:

- Title/author metadata: EPUB and PDF.
- Cover extraction: EPUB, CBZ/ZIP, and PDF.

`cover.jpg` and `folder.jpg` can be used as catalog covers when `ShowCovers` is enabled, but image files are not listed as books.

## CONVENTIONS

### Handler Pattern
- Handlers return `error` where the service pattern expects it.
- `errorHandler()` wrapper converts returned errors to HTTP responses.
- File requests use `http.ServeFile()` or `http.ServeContent()`; directory/catalog requests return OPDS XML or HTML.

### Path Security
- ALWAYS call `verifyPath()` before filesystem access derived from request input.
- `TrustedRoot` is canonicalized at startup.
- Path traversal tests live in `internal_test.go`.

### Pagination
- Default: 50 entries/page, max: 50.
- Query param: `?page=N`.
- OPDS links: `first`, `previous`, `next`, `last`.
- `NoPagination` disables paging.

### Caching
- `-enable-cache` enables ETag + Last-Modified.
- ETags include catalog data that affects the response.
- 304 Not Modified is returned on cache matches.

## ANTI-PATTERNS

- DO NOT skip `verifyPath()` before request-derived filesystem operations.
- DO NOT claim that every listed extension has metadata or cover parsing.
- DO NOT expose real filesystem paths as visible catalog titles.
- DO NOT write new docs for flags or behavior without checking `main.go`, `internal/server/server.go`, and this package.
