// Package service provides a HTTP handler that reads the path in the request URL
// and returns an XML document that follows the OPDS 1.2 standard.
// https://specs.opds.io/opds-1.2
package service

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dubyte/dir2opds/opds"
	"golang.org/x/tools/blog/atom"
)

type epubManifestItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type epubMeta struct {
	Name     string `xml:"name,attr"`
	Content  string `xml:"content,attr"`
	Property string `xml:"property,attr"`
	Value    string `xml:",chardata"`
}

func init() {
	_ = mime.AddExtensionType(".mobi", "application/x-mobipocket-ebook")
	_ = mime.AddExtensionType(".epub", "application/epub+zip")
	_ = mime.AddExtensionType(".cbz", "application/x-cbz")
	_ = mime.AddExtensionType(".cbr", "application/x-cbr")
	_ = mime.AddExtensionType(".fb2", "text/fb2+xml")
	_ = mime.AddExtensionType(".pdf", "application/pdf")
	_ = mime.AddExtensionType(".txt", "text/plain; charset=utf-8")
}

const (
	pathTypeFile = iota
	pathTypeDirOfDirs
	pathTypeDirOfFiles
)

const (
	defaultPageSize = 50
	maxPageSize     = 200
)

const (
	ignoreFile       = true
	includeFile      = false
	currentDirectory = "."
	parentDirectory  = ".."
	hiddenFilePrefix = "."
	thumbDirectory   = ".thumb"
)

var supportedBookExtensions = map[string]bool{
	".epub": true,
	".cbz":  true,
	".zip":  true,
	".pdf":  true,
}

type OPDS struct {
	TrustedRoot      string
	ThumbDir         string
	HideCalibreFiles bool
	HideDotFiles     bool
	NoCache          bool
	EnableCache      bool
	SortBy           string
	ShowCovers       bool
	MimeMap          map[string]string
	EnableSearch     bool
	ExtractMetadata  bool
	EnableHTML       bool
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

type CatalogEntry struct {
	Name      string
	Type      int
	Href      string
	ModTime   time.Time
	Size      int64
	Title     string
	Author    string
	CoverPath string
}

type IsDirer interface {
	IsDir() bool
}

func isFile(e IsDirer) bool {
	return !e.IsDir()
}

const (
	opdsNamespace    = "http://opds-spec.org/2010/catalog"
	dcTermsNamespace = "http://purl.org/dc/terms/"

	navigationType  = "application/atom+xml;profile=opds-catalog;kind=navigation"
	acquisitionType = "application/atom+xml;profile=opds-catalog;kind=acquisition"
)

var TimeNow = timeNowFunc()

// Scan inspects the directory and builds a Catalog model
func (s OPDS) pageSize() int {
	if s.PageSize <= 0 {
		return defaultPageSize
	}
	if s.PageSize > maxPageSize {
		return maxPageSize
	}
	return s.PageSize
}

func parsePage(pageStr string) int {
	if pageStr == "" {
		return 1
	}
	page := 1
	if n, err := strconv.Atoi(pageStr); err == nil && n > 0 {
		page = n
	}
	return page
}

func etag(urlPath string, modTime time.Time, page int) string {
	h := sha256.New()
	h.Write([]byte(urlPath))
	h.Write([]byte(modTime.UTC().Format(time.RFC3339Nano)))
	h.Write([]byte(strconv.Itoa(page)))
	return `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
}

func (s OPDS) Scan(fPath string, urlPath string, page int) (*Catalog, error) {
	if s.ExtractMetadata {
		cleanupThumbCache(s.thumbDir())
	}

	dirEntries, err := os.ReadDir(fPath)
	if err != nil {
		return nil, err
	}

	dirInfo, err := os.Stat(fPath)
	if err != nil {
		return nil, err
	}

	catalog := &Catalog{
		ID:      urlPath,
		Title:   "Catalog in " + urlPath,
		Type:    getPathType(fPath),
		ModTime: dirInfo.ModTime(),
	}

	for _, entry := range dirEntries {
		if fileShouldBeIgnored(entry.Name(), s.HideCalibreFiles, s.HideDotFiles) {
			continue
		}

		if s.ShowCovers && (entry.Name() == "cover.jpg" || entry.Name() == "folder.jpg") {
			catalog.Cover = filepath.Join(urlPath, entry.Name())
			continue
		}

		entryPath := filepath.Join(fPath, entry.Name())
		info, err := entry.Info()
		if err != nil {
			slog.Error("error getting info for entry", "error", err)
			continue
		}

		if !entry.IsDir() && !isSupportedBookFile(entry.Name()) {
			continue
		}

		catalog.Entries = append(catalog.Entries, CatalogEntry{
			Name:    entry.Name(),
			Type:    getPathType(entryPath),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		})

		if info.ModTime().After(catalog.ModTime) {
			catalog.ModTime = info.ModTime()
		}

		if s.ExtractMetadata && !entry.IsDir() {
			idx := len(catalog.Entries) - 1
			title, author, coverPath := extractMetadata(entryPath)
			if title != "" {
				catalog.Entries[idx].Title = title
			}
			if author != "" {
				catalog.Entries[idx].Author = author
			}
			if coverPath != "" {
				catalog.Entries[idx].CoverPath = coverPath
			}

			if cachedCover, err := ensureCachedCover(s.thumbDir(), entryPath); err == nil && cachedCover != "" {
				catalog.Entries[idx].CoverPath = cachedCover
			}
		}
	}

	s.sortEntries(catalog.Entries)

	total := len(catalog.Entries)
	pageSize := s.pageSize()
	if page < 1 {
		page = 1
	}

	// When NoPagination is enabled, show all entries on a single page
	if s.NoPagination {
		pageSize = total
		if pageSize == 0 {
			pageSize = 1 // Avoid division by zero
		}
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	catalog.Total = total
	catalog.Page = page
	catalog.PageSize = pageSize
	catalog.Entries = catalog.Entries[start:end]

	return catalog, nil
}

func extractMetadata(path string) (string, string, string) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".epub":
		return extractEpubMetadata(path)
	case ".pdf":
		title, author := extractPdfMetadata(path)
		return title, author, ""
	}
	return "", "", ""
}

func (s OPDS) thumbDir() string {
	if s.ThumbDir != "" {
		_ = os.MkdirAll(s.ThumbDir, 0o755)
		return s.ThumbDir
	}

	fallback := filepath.Join(os.TempDir(), "thumb")
	_ = os.MkdirAll(fallback, 0o755)
	return fallback
}

func sourceMarkerPath(thumbPath string) string {
	return thumbPath + ".src"
}

func thumbPathForSource(baseDir, sourcePath, coverExt string) string {
	sum := sha1.Sum([]byte(sourcePath))
	name := hex.EncodeToString(sum[:]) + coverExt
	return filepath.Join(baseDir, name)
}

func ensureCachedCover(baseDir, sourcePath string) (string, error) {
	coverData, coverExt, err := extractCoverForFile(sourcePath)
	if err != nil || len(coverData) == 0 || coverExt == "" {
		return "", err
	}

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return "", err
	}

	thumbPath := thumbPathForSource(baseDir, sourcePath, coverExt)
	if _, err := os.Stat(thumbPath); err == nil {
		return thumbPath, nil
	}

	if err := os.WriteFile(thumbPath, coverData, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(sourceMarkerPath(thumbPath), []byte(sourcePath), 0o644); err != nil {
		return "", err
	}

	return thumbPath, nil
}

func cleanupThumbCache(baseDir string) {
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".src") {
			continue
		}

		srcMarker := filepath.Join(baseDir, entry.Name())
		sourcePathBytes, err := os.ReadFile(srcMarker)
		if err != nil {
			continue
		}

		sourcePath := strings.TrimSpace(string(sourcePathBytes))
		if sourcePath == "" {
			_ = os.Remove(srcMarker)
			continue
		}

		if _, err := os.Stat(sourcePath); err == nil {
			continue
		}

		thumbPath := strings.TrimSuffix(srcMarker, ".src")
		_ = os.Remove(thumbPath)
		_ = os.Remove(srcMarker)
	}
}

func extractEpubMetadata(path string) (string, string, string) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", "", ""
	}
	defer r.Close()

	var opfPath string
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, ".opf") {
			opfPath = f.Name
			break
		}
	}

	if opfPath == "" {
		return "", "", ""
	}

	f, err := r.Open(opfPath)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()

	opfContent, err := io.ReadAll(f)
	if err != nil {
		return "", "", ""
	}

	var opf struct {
		Metadata struct {
			Title   string     `xml:"title"`
			Creator string     `xml:"creator"`
			Meta    []epubMeta `xml:"meta"`
		} `xml:"metadata"`
		Manifest struct {
			Items []epubManifestItem `xml:"item"`
		} `xml:"manifest"`
	}

	decoder := xml.NewDecoder(bytes.NewReader(opfContent))
	if err := decoder.Decode(&opf); err != nil {
		return "", "", ""
	}

	// If standard unmarshal fails to get values due to namespaces
	if opf.Metadata.Title == "" || opf.Metadata.Creator == "" {
		decoder = xml.NewDecoder(bytes.NewReader(opfContent))
		decoder.DefaultSpace = "http://purl.org/dc/elements/1.1/"
		var opf2 struct {
			Metadata struct {
				Title   string `xml:"title"`
				Creator string `xml:"creator"`
			} `xml:"metadata"`
		}
		_ = decoder.Decode(&opf2)
		if opf2.Metadata.Title != "" {
			opf.Metadata.Title = opf2.Metadata.Title
		}
		if opf2.Metadata.Creator != "" {
			opf.Metadata.Creator = opf2.Metadata.Creator
		}
	}

	// Find cover image in manifest
	coverPath := findEpubCover(r, opf.Manifest.Items, opf.Metadata.Meta, opfPath)

	return opf.Metadata.Title, opf.Metadata.Creator, coverPath
}

func findEpubCover(r *zip.ReadCloser, items []epubManifestItem, metas []epubMeta, opfPath string) string {
	coverIDs := []string{"cover", "cover-image", "coverimage", "frontcover", "front-cover"}
	imageExtensions := []string{".jpg", ".jpeg", ".png", ".gif", ".webp"}
	opfDir := path.Dir(opfPath)

	itemByID := make(map[string]epubManifestItem)
	for _, item := range items {
		itemByID[item.ID] = item
	}

	for _, meta := range metas {
		if strings.EqualFold(meta.Name, "cover") && meta.Content != "" {
			if coverPath := coverPathFromItem(r, opfDir, itemByID[meta.Content]); coverPath != "" {
				return coverPath
			}
		}
	}

	for _, item := range items {
		if strings.Contains(strings.ToLower(item.Properties), "cover-image") {
			if coverPath := coverPathFromItem(r, opfDir, item); coverPath != "" {
				return coverPath
			}
		}
	}

	for _, item := range items {
		itemID := strings.ToLower(item.ID)
		for _, coverID := range coverIDs {
			if strings.Contains(itemID, coverID) {
				if coverPath := coverPathFromItem(r, opfDir, item); coverPath != "" {
					return coverPath
				}
			}
		}
	}

	coverNames := []string{"cover.jpg", "cover.jpeg", "cover.png", "cover.gif", "cover.webp", "frontcover.jpg", "frontcover.jpeg", "frontcover.png", "frontcover.gif", "frontcover.webp"}
	for _, f := range r.File {
		name := strings.ToLower(f.Name)
		for _, coverName := range coverNames {
			if strings.HasSuffix(name, coverName) {
				return f.Name
			}
		}
	}

	for _, f := range r.File {
		name := strings.ToLower(f.Name)
		if strings.Contains(name, "images/") || strings.Contains(name, "oebps/images/") {
			for _, ext := range imageExtensions {
				if strings.HasSuffix(name, ext) {
					// Prefer files with "cover" in the name
					if strings.Contains(name, "cover") {
						return f.Name
					}
				}
			}
		}
	}

	for _, f := range r.File {
		if hasImageExtension(f.Name, imageExtensions) {
			return f.Name
		}
	}

	return ""
}

func coverPathFromItem(r *zip.ReadCloser, opfDir string, item epubManifestItem) string {
	if item.Href == "" {
		return ""
	}

	itemPath := cleanZipPath(path.Join(opfDir, item.Href))
	if isImageMediaType(item.MediaType) || hasImageExtension(itemPath, nil) {
		return itemPath
	}

	if strings.Contains(item.MediaType, "xhtml") || strings.Contains(item.MediaType, "html") || hasHTMLExtension(itemPath) {
		if coverPath := coverPathFromHTML(r, opfDir, itemPath); coverPath != "" {
			return coverPath
		}
	}

	return ""
}

func coverPathFromHTML(r *zip.ReadCloser, opfDir, htmlPath string) string {
	f, err := r.Open(htmlPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	content, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	decoder := xml.NewDecoder(bytes.NewReader(content))
	for {
		tok, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "img" && start.Name.Local != "image" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local != "src" && attr.Name.Local != "href" {
				continue
			}
			if attr.Value == "" {
				continue
			}
			base := path.Dir(htmlPath)
			return cleanZipPath(path.Join(base, strings.Split(attr.Value, "#")[0]))
		}
	}
}

func cleanZipPath(p string) string {
	return strings.TrimPrefix(path.Clean(strings.ReplaceAll(p, "\\", "/")), "./")
}

func isImageMediaType(mediaType string) bool {
	return strings.HasPrefix(strings.ToLower(mediaType), "image/")
}

func hasImageExtension(name string, exts []string) bool {
	if len(exts) == 0 {
		exts = []string{".jpg", ".jpeg", ".png", ".gif", ".webp"}
	}
	ext := strings.ToLower(path.Ext(name))
	for _, candidate := range exts {
		if ext == candidate {
			return true
		}
	}
	return false
}

func hasHTMLExtension(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	return ext == ".html" || ext == ".xhtml" || ext == ".htm"
}

func extractPdfMetadata(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var title, author string
	// Only scan first 4KB to keep it fast
	maxLines := 100
	for i := 0; i < maxLines && scanner.Scan(); i++ {
		line := scanner.Text()
		if title == "" && strings.Contains(line, "/Title") {
			title = parsePdfValue(line, "/Title")
		}
		if author == "" && strings.Contains(line, "/Author") {
			author = parsePdfValue(line, "/Author")
		}
		if title != "" && author != "" {
			break
		}
	}
	return title, author
}

func parsePdfValue(line, key string) string {
	idx := strings.Index(line, key)
	if idx == -1 {
		return ""
	}
	start := strings.Index(line[idx:], "(")
	if start == -1 {
		return ""
	}
	end := strings.Index(line[idx+start:], ")")
	if end == -1 {
		return ""
	}
	return line[idx+start+1 : idx+start+end]
}

func (s OPDS) sortEntries(entries []CatalogEntry) {
	switch s.SortBy {
	case "date":
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].ModTime.Before(entries[j].ModTime)
		})
	case "size":
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Size > entries[j].Size
		})
	default: // name
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name < entries[j].Name
		})
	}
}

func isBrowser(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}

// Handler serves the content of a book file or
// returns an Acquisition Feed when the entries are documents or
// returns a Navigation Feed when the entries are other folders
func (s OPDS) Handler(w http.ResponseWriter, req *http.Request) error {
	var err error
	urlPath, err := url.PathUnescape(req.URL.Path)
	if err != nil {
		slog.Error("error unescaping path", "urlPath", req.URL.Path, "error", err)
		return err
	}

	fPath := filepath.Join(s.TrustedRoot, urlPath)

	// verifyPath avoid the http transversal by checking the path is under DirRoot
	_, err = verifyPath(fPath, s.TrustedRoot)
	if err != nil {
		slog.Error("verify path error", "error", err)
		w.WriteHeader(http.StatusNotFound)
		return nil
	}

	if _, err := os.Stat(fPath); err != nil {
		slog.Error("file system stat error", "error", err)
		w.WriteHeader(http.StatusNotFound)
		return nil
	}

	pathType := getPathType(fPath)

	// it's a file just serve the file
	if pathType == pathTypeFile {
		http.ServeFile(w, req, fPath)
		return nil
	}

	if s.NoCache {
		w.Header().Add("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Add("Expires", "0")
	}

	query := req.URL.Query()
	if urlPath == "/" {
		if query.Get("sort") == "" {
			if s.EnableHTML && isBrowser(req) {
				return s.renderHTML(w, req, s.makeSortSelectionCatalog(req))
			}
			return s.serveSortSelectionFeed(w, req)
		}

		catalog, err := s.collectBookCatalog(fPath, urlPath, "Catalog in "+urlPath, pageFromRequest(req), query.Get("sort"), "")
		if err != nil {
			return err
		}
		if s.EnableHTML && isBrowser(req) {
			return s.renderHTML(w, req, catalog)
		}
		return s.serveCatalogFeed(w, req, catalog)
	}

	sortBy := query.Get("sort")
	if sortBy == "" {
		sortBy = s.SortBy
	}

	page := parsePage(query.Get("page"))
	scanService := s
	scanService.SortBy = sortBy
	catalog, err := scanService.Scan(fPath, urlPath, page)
	if err != nil {
		slog.Error("error scanning path", "error", err)
		return err
	}

	slog.Debug("request",
		"urlPath", urlPath,
		"page", catalog.Page,
		"pageSize", catalog.PageSize,
		"total", catalog.Total,
		"totalPages", (catalog.Total+catalog.PageSize-1)/catalog.PageSize,
	)

	if pathType == pathTypeDirOfFiles && query.Get("sort") == "" {
		if s.EnableHTML && isBrowser(req) {
			return s.renderHTML(w, req, s.makeSortSelectionCatalog(req))
		}
		return s.serveSortSelectionFeed(w, req)
	}

	if s.EnableCache {
		eTag := etag(urlPath, catalog.ModTime, page)
		lastModified := catalog.ModTime.UTC()

		w.Header().Set("ETag", eTag)
		w.Header().Set("Last-Modified", lastModified.Format(http.TimeFormat))

		if ifNoneMatch := req.Header.Get("If-None-Match"); ifNoneMatch != "" {
			if ifNoneMatch == eTag {
				w.WriteHeader(http.StatusNotModified)
				return nil
			}
		}

		if ifModifiedSince := req.Header.Get("If-Modified-Since"); ifModifiedSince != "" {
			if t, err := time.Parse(http.TimeFormat, ifModifiedSince); err == nil {
				if !lastModified.After(t) {
					w.WriteHeader(http.StatusNotModified)
					return nil
				}
			}
		}
	}

	if s.EnableHTML && isBrowser(req) {
		return s.renderHTML(w, req, catalog)
	}

	return s.serveCatalogFeed(w, req, catalog)
}

func (s OPDS) serveCatalogFeed(w http.ResponseWriter, req *http.Request, catalog *Catalog) error {
	navFeed := s.makeFeed(catalog, req)
	var content []byte
	var err error
	// it is an acquisition feed
	if catalog.Type == pathTypeDirOfFiles {
		acFeed := &opds.AcquisitionFeed{Feed: &navFeed, Dc: dcTermsNamespace, Opds: opdsNamespace}
		content, err = xml.MarshalIndent(acFeed, "  ", "    ")
		w.Header().Add("Content-Type", acquisitionType)
	} else { // it is a navigation feed
		content, err = xml.MarshalIndent(navFeed, "  ", "    ")
		w.Header().Add("Content-Type", navigationType)
	}
	if err != nil {
		slog.Error("error marshaling feed", "error", err)
		return err
	}

	content = append([]byte(xml.Header), content...)
	http.ServeContent(w, req, "feed.xml", TimeNow(), bytes.NewReader(content))

	return nil
}

func (s OPDS) serveSortSelectionFeed(w http.ResponseWriter, req *http.Request) error {
	content, err := xml.MarshalIndent(s.makeSortSelectionFeed(req), "  ", "    ")
	if err != nil {
		slog.Error("error marshaling sort selection feed", "error", err)
		return err
	}
	w.Header().Add("Content-Type", navigationType)
	content = append([]byte(xml.Header), content...)
	http.ServeContent(w, req, "feed.xml", TimeNow(), bytes.NewReader(content))
	return nil
}

func pageFromRequest(req *http.Request) int {
	return parsePage(req.URL.Query().Get("page"))
}

func buildSortURL(basePath string, query url.Values, sortBy string) string {
	query.Set("sort", sortBy)
	query.Del("page")
	return basePath + "?" + query.Encode()
}

func (s OPDS) makeSortSelectionCatalog(req *http.Request) *Catalog {
	query := req.URL.Query()
	nameURL := buildSortURL(req.URL.Path, query, "name")
	dateURL := buildSortURL(req.URL.Path, query, "date")

	return &Catalog{
		ID:       req.URL.Path,
		Title:    "Sort catalog",
		Type:     pathTypeDirOfDirs,
		Total:    2,
		Page:     1,
		PageSize: 2,
		Entries: []CatalogEntry{
			{
				Name: "By Name",
				Type: pathTypeDirOfFiles,
				Href: nameURL,
			},
			{
				Name: "By Date Added",
				Type: pathTypeDirOfFiles,
				Href: dateURL,
			},
		},
	}
}

func (s OPDS) makeSortSelectionFeed(req *http.Request) atom.Feed {
	updated := TimeNow()
	query := req.URL.Query()

	feedBuilder := opds.FeedBuilder.
		ID(req.URL.Path).
		Title("Sort catalog").
		Updated(updated).
		Author(atom.Person{Name: "dir2opds"}).
		AddLink(opds.LinkBuilder.Rel("self").Href(s.joinURL(req.URL.RequestURI())).Type(navigationType).Build()).
		AddLink(opds.LinkBuilder.Rel("start").Href(s.joinURL("/")).Type(navigationType).Build())

	nameURL := s.joinURL(buildSortURL(req.URL.Path, query, "name"))
	dateURL := s.joinURL(buildSortURL(req.URL.Path, query, "date"))

	feedBuilder = feedBuilder.AddEntry(opds.EntryBuilder.
		ID(nameURL).
		Title("By Name").
		Published(updated).
		Updated(updated).
		AddLink(opds.LinkBuilder.
			Rel("subsection").
			Title("By Name").
			Href(nameURL).
			Type(acquisitionType).
			Build()).
		Build())

	feedBuilder = feedBuilder.AddEntry(opds.EntryBuilder.
		ID(dateURL).
		Title("By Date Added").
		Published(updated).
		Updated(updated).
		AddLink(opds.LinkBuilder.
			Rel("subsection").
			Title("By Date Added").
			Href(dateURL).
			Type(acquisitionType).
			Build()).
		Build())

	return feedBuilder.Build()
}

func (s OPDS) collectBookCatalog(rootPath, urlPath, title string, page int, sortBy string, query string) (*Catalog, error) {
	catalog := &Catalog{
		ID:       urlPath,
		Title:    title,
		Type:     pathTypeDirOfFiles,
		Page:     page,
		PageSize: s.pageSize(),
	}

	err := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fileShouldBeIgnored(info.Name(), s.HideCalibreFiles, s.HideDotFiles) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() || !isSupportedBookFile(info.Name()) {
			return nil
		}
		if query != "" && !strings.Contains(strings.ToLower(info.Name()), strings.ToLower(query)) {
			return nil
		}

		relPath, err := filepath.Rel(s.TrustedRoot, path)
		if err != nil {
			return err
		}
		entry := CatalogEntry{
			Name:    info.Name(),
			Type:    pathTypeFile,
			Href:    "/" + filepath.ToSlash(relPath),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		}

		if catalog.ModTime.IsZero() || info.ModTime().After(catalog.ModTime) {
			catalog.ModTime = info.ModTime()
		}

		if s.ExtractMetadata {
			title, author, coverPath := extractMetadata(path)
			if title != "" {
				entry.Title = title
			}
			if author != "" {
				entry.Author = author
			}
			if coverPath != "" {
				entry.CoverPath = coverPath
			}
			if cachedCover, err := ensureCachedCover(s.thumbDir(), path); err == nil && cachedCover != "" {
				entry.CoverPath = cachedCover
			}
		}

		catalog.Entries = append(catalog.Entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sortService := s
	sortService.SortBy = sortBy
	sortService.sortEntries(catalog.Entries)

	total := len(catalog.Entries)
	pageSize := catalog.PageSize
	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > total {
		start = total
	}
	if end > total {
		end = total
	}

	catalog.Total = total
	catalog.Page = page
	catalog.Entries = catalog.Entries[start:end]
	return catalog, nil
}

// SearchHandler performs a basic filename search
func (s OPDS) SearchHandler(w http.ResponseWriter, req *http.Request) error {
	query := req.URL.Query().Get("q")
	if query == "" {
		return s.Handler(w, req)
	}

	sortBy := req.URL.Query().Get("sort")
	if sortBy == "" {
		if s.EnableHTML && isBrowser(req) {
			return s.renderHTML(w, req, s.makeSortSelectionCatalog(req))
		}
		return s.serveSortSelectionFeed(w, req)
	}

	catalog, err := s.collectBookCatalog(s.TrustedRoot, "search:"+query, "Search results for: "+query, pageFromRequest(req), sortBy, query)
	if err != nil {
		return err
	}

	if s.EnableHTML && isBrowser(req) {
		return s.renderHTML(w, req, catalog)
	}

	navFeed := s.makeFeed(catalog, req)
	acFeed := &opds.AcquisitionFeed{Feed: &navFeed, Dc: dcTermsNamespace, Opds: opdsNamespace}
	content, err := xml.MarshalIndent(acFeed, "  ", "    ")
	if err != nil {
		return err
	}

	w.Header().Add("Content-Type", acquisitionType)
	content = append([]byte(xml.Header), content...)
	http.ServeContent(w, req, "feed.xml", TimeNow(), bytes.NewReader(content))
	return nil
}

// OpenSearchHandler serves the OpenSearch description document
func (s OPDS) OpenSearchHandler(w http.ResponseWriter, req *http.Request) {
	searchURL := s.joinURL("/search?q={searchTerms}")
	xmlStr := `<?xml version="1.0" encoding="UTF-8"?>
<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/">
  <ShortName>dir2opds</ShortName>
  <Description>Search books in dir2opds</Description>
  <InputEncoding>UTF-8</InputEncoding>
  <OutputEncoding>UTF-8</OutputEncoding>
  <Url type="` + acquisitionType + `" template="` + searchURL + `"/>
</OpenSearchDescription>`
	w.Header().Set("Content-Type", "application/opensearchdescription+xml")
	w.Write([]byte(xmlStr))
}

func (s OPDS) joinURL(p string) string {
	if s.BaseURL == "" {
		return p
	}
	return strings.TrimSuffix(s.BaseURL, "/") + "/" + strings.TrimPrefix(p, "/")
}

// CoverHandler extracts and serves cover images from EPUB files
func (s OPDS) CoverHandler(w http.ResponseWriter, req *http.Request) error {
	filePath := req.URL.Query().Get("file")
	if filePath == "" {
		return fmt.Errorf("missing file parameter")
	}

	urlPath, err := url.PathUnescape(filePath)
	if err != nil {
		slog.Error("error unescaping cover path", "filePath", filePath, "error", err)
		return err
	}

	fPath := filepath.Join(s.TrustedRoot, urlPath)

	// verifyPath avoid the http transversal by checking the path is under TrustedRoot
	_, err = verifyPath(fPath, s.TrustedRoot)
	if err != nil {
		slog.Error("verify path error for cover", "error", err)
		w.WriteHeader(http.StatusNotFound)
		return nil
	}

	if _, err := os.Stat(fPath); err != nil {
		slog.Error("file stat error for cover", "error", err)
		w.WriteHeader(http.StatusNotFound)
		return nil
	}

	cachedPath, err := ensureCachedCover(s.thumbDir(), fPath)
	if err != nil {
		slog.Error("error extracting cover", "path", fPath, "error", err)
		return err
	}

	if cachedPath == "" {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}

	coverData, err := os.ReadFile(cachedPath)
	if err != nil {
		return err
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(cachedPath)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "max-age=86400")
	http.ServeContent(w, req, "cover", TimeNow(), bytes.NewReader(coverData))
	return nil
}

// extractEpubCover extracts the cover image from an EPUB file
func extractEpubCover(epubPath string) ([]byte, string, error) {
	r, err := zip.OpenReader(epubPath)
	if err != nil {
		return nil, "", fmt.Errorf("opening epub: %w", err)
	}
	defer r.Close()

	var opfPath string
	for _, f := range r.File {
		if strings.HasSuffix(f.Name, ".opf") {
			opfPath = f.Name
			break
		}
	}

	if opfPath == "" {
		return nil, "", fmt.Errorf("no OPF file found")
	}

	f, err := r.Open(opfPath)
	if err != nil {
		return nil, "", fmt.Errorf("opening OPF: %w", err)
	}
	defer f.Close()

	opfContent, err := io.ReadAll(f)
	if err != nil {
		return nil, "", fmt.Errorf("reading OPF: %w", err)
	}

	var opf struct {
		Manifest struct {
			Items []epubManifestItem `xml:"item"`
		} `xml:"manifest"`
		Metadata struct {
			Meta []epubMeta `xml:"meta"`
		} `xml:"metadata"`
	}

	decoder := xml.NewDecoder(bytes.NewReader(opfContent))
	if err := decoder.Decode(&opf); err != nil {
		return nil, "", fmt.Errorf("parsing OPF: %w", err)
	}

	coverPath := findEpubCover(r, opf.Manifest.Items, opf.Metadata.Meta, opfPath)
	if coverPath == "" {
		return nil, "", nil
	}

	coverFile, err := r.Open(coverPath)
	if err != nil {
		return nil, "", fmt.Errorf("opening cover: %w", err)
	}
	defer coverFile.Close()

	coverData, err := io.ReadAll(coverFile)
	if err != nil {
		return nil, "", fmt.Errorf("reading cover: %w", err)
	}

	ext := strings.ToLower(filepath.Ext(coverPath))
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return coverData, contentType, nil
}

func extractFirstImageFromZip(zipPath string) ([]byte, string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()

	imageExt := []string{".jpg", ".jpeg", ".png", ".gif", ".webp"}
	for _, f := range r.File {
		lowerName := strings.ToLower(f.Name)
		for _, ext := range imageExt {
			if strings.HasSuffix(lowerName, ext) {
				rc, err := f.Open()
				if err != nil {
					return nil, "", err
				}
				data, err := io.ReadAll(rc)
				rc.Close()
				if err != nil {
					return nil, "", err
				}
				return data, ext, nil
			}
		}
	}

	return nil, "", nil
}

func extractFirstImageFromPDF(pdfPath string) ([]byte, string, error) {
	data, err := os.ReadFile(pdfPath)
	if err != nil {
		return nil, "", err
	}

	if image, ok := extractJPEGBytes(data); ok {
		return image, ".jpg", nil
	}
	if image, ok := extractPNGBytes(data); ok {
		return image, ".png", nil
	}

	return nil, "", nil
}

func extractJPEGBytes(data []byte) ([]byte, bool) {
	start := bytes.Index(data, []byte{0xff, 0xd8, 0xff})
	if start == -1 {
		return nil, false
	}
	endRel := bytes.Index(data[start+2:], []byte{0xff, 0xd9})
	if endRel == -1 {
		return nil, false
	}
	end := start + 2 + endRel + 2
	return data[start:end], true
}

func extractPNGBytes(data []byte) ([]byte, bool) {
	signature := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	start := bytes.Index(data, signature)
	if start == -1 {
		return nil, false
	}
	iend := []byte{'I', 'E', 'N', 'D'}
	iendRel := bytes.Index(data[start:], iend)
	if iendRel == -1 {
		return nil, false
	}
	end := start + iendRel + len(iend) + 4
	if end > len(data) {
		return nil, false
	}
	return data[start:end], true
}

func extractCoverForFile(sourcePath string) ([]byte, string, error) {
	ext := strings.ToLower(filepath.Ext(sourcePath))
	switch ext {
	case ".epub":
		data, contentType, err := extractEpubCover(sourcePath)
		if err != nil || len(data) == 0 {
			return nil, "", err
		}
		switch {
		case strings.Contains(contentType, "png"):
			return data, ".png", nil
		case strings.Contains(contentType, "gif"):
			return data, ".gif", nil
		case strings.Contains(contentType, "webp"):
			return data, ".webp", nil
		default:
			return data, ".jpg", nil
		}
	case ".zip", ".cbz":
		return extractFirstImageFromZip(sourcePath)
	case ".pdf":
		return extractFirstImageFromPDF(sourcePath)
	default:
		return nil, "", nil
	}
}

func isSupportedBookFile(name string) bool {
	return supportedBookExtensions[strings.ToLower(filepath.Ext(name))]
}

func (s OPDS) makeFeed(catalog *Catalog, req *http.Request) atom.Feed {
	feedType := navigationType
	if catalog.Type == pathTypeDirOfFiles {
		feedType = acquisitionType
	}
	updated := TimeNow()

	feedBuilder := opds.FeedBuilder.
		ID(catalog.ID).
		Title(catalog.Title).
		Updated(updated).
		Author(atom.Person{Name: "dir2opds"}).
		AddLink(opds.LinkBuilder.Rel("self").Href(s.joinURL(req.URL.RequestURI())).Type(feedType).Build()).
		AddLink(opds.LinkBuilder.Rel("start").Href(s.joinURL("/")).Type(navigationType).Build())

	if s.EnableSearch {
		feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
			Rel("search").
			Href(s.joinURL("/opensearch.xml")).
			Type("application/opensearchdescription+xml").
			Build())
	}

	if catalog.Cover != "" {
		coverHref := s.joinURL((&url.URL{Path: catalog.Cover}).String())
		feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
			Rel("http://opds-spec.org/image").
			Href(coverHref).
			Type(mime.TypeByExtension(filepath.Ext(catalog.Cover))).
			Build())
		feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
			Rel("http://opds-spec.org/image/thumbnail").
			Href(coverHref).
			Type(mime.TypeByExtension(filepath.Ext(catalog.Cover))).
			Build())
	}

	if !s.NoPagination && catalog.Total > catalog.PageSize {
		totalPages := (catalog.Total + catalog.PageSize - 1) / catalog.PageSize
		basePath := req.URL.Path
		query := req.URL.Query()

		if catalog.Page > 1 {
			feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
				Rel("first").
				Href(s.joinURL(buildPageURL(basePath, query, 1))).
				Type(feedType).
				Build())
			feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
				Rel("previous").
				Href(s.joinURL(buildPageURL(basePath, query, catalog.Page-1))).
				Type(feedType).
				Build())
		}
		if catalog.Page < totalPages {
			feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
				Rel("next").
				Href(s.joinURL(buildPageURL(basePath, query, catalog.Page+1))).
				Type(feedType).
				Build())
			feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
				Rel("last").
				Href(s.joinURL(buildPageURL(basePath, query, totalPages))).
				Type(feedType).
				Build())
		}
	}

	for _, entry := range catalog.Entries {
		title := entry.Name
		if entry.Title != "" {
			title = entry.Title
		}

		var entryPath string
		if entry.Href != "" {
			entryPath = entry.Href
		} else if strings.HasPrefix(catalog.ID, "search:") {
			entryPath = "/" + entry.Name
		} else {
			entryPath = path.Join(req.URL.Path, entry.Name)
		}

		href := s.joinURL((&url.URL{Path: entryPath}).String())

		entryBuilder := opds.EntryBuilder.
			ID(href).
			Title(title).
			Published(updated).
			Updated(updated).
			AddLink(opds.LinkBuilder.
				Rel(getRel(entry.Name, entry.Type)).
				Title(entry.Name).
				Href(href).
				Type(s.getType(entry.Name, entry.Type)).
				Build())

		if entry.Author != "" {
			entryBuilder = entryBuilder.Author(&atom.Person{Name: entry.Author})
		}

		if s.ExtractMetadata && entry.CoverPath != "" && entry.Type == pathTypeFile {
			coverURL := s.joinURL("/cover?file=" + url.QueryEscape(entryPath))
			ext := strings.ToLower(filepath.Ext(entry.CoverPath))
			contentType := mime.TypeByExtension(ext)
			if contentType == "" {
				contentType = "image/jpeg"
			}
			entryBuilder = entryBuilder.AddLink(opds.LinkBuilder.
				Rel("http://opds-spec.org/image").
				Href(coverURL).
				Type(contentType).
				Build())
			entryBuilder = entryBuilder.AddLink(opds.LinkBuilder.
				Rel("http://opds-spec.org/image/thumbnail").
				Href(coverURL).
				Type(contentType).
				Build())
		}

		feedBuilder = feedBuilder.AddEntry(entryBuilder.Build())
	}
	return feedBuilder.Build()
}

func buildPageURL(basePath string, query url.Values, page int) string {
	query.Set("page", strconv.Itoa(page))
	return basePath + "?" + query.Encode()
}

func fileShouldBeIgnored(filename string, hideCalibreFiles, hideDotFiles bool) bool {
	// not ignore those directories
	if filename == currentDirectory || filename == parentDirectory {
		return includeFile
	}

	if filename == thumbDirectory {
		return ignoreFile
	}

	if hideDotFiles && strings.HasPrefix(filename, hiddenFilePrefix) {
		return ignoreFile
	}

	if hideCalibreFiles &&
		(strings.Contains(filename, ".opf") ||
			strings.Contains(filename, "cover.") ||
			strings.Contains(filename, "metadata.db") ||
			strings.Contains(filename, "metadata_db_prefs_backup.json") ||
			strings.Contains(filename, ".caltrash") ||
			strings.Contains(filename, ".calnotes")) {
		return ignoreFile
	}

	return false
}

func getRel(name string, pathType int) string {
	if pathType == pathTypeDirOfFiles || pathType == pathTypeDirOfDirs {
		return "subsection"
	}

	ext := filepath.Ext(name)
	if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" {
		return "http://opds-spec.org/image/thumbnail"
	}

	// mobi, epub, etc
	return "http://opds-spec.org/acquisition"
}

func (s OPDS) getType(name string, pathType int) string {
	switch pathType {
	case pathTypeFile:
		ext := filepath.Ext(name)
		if s.MimeMap != nil {
			if mType, ok := s.MimeMap[ext]; ok {
				return mType
			}
		}
		contentType := mime.TypeByExtension(ext)
		if contentType == "" {
			return "application/octet-stream"
		}
		return contentType
	case pathTypeDirOfFiles:
		return acquisitionType
	case pathTypeDirOfDirs:
		return navigationType
	default:
		return mime.TypeByExtension("xml")
	}
}

func getPathType(dirpath string) int {
	fi, err := os.Stat(dirpath)
	if err != nil {
		slog.Error("getPathType os.Stat error", "error", err)
		return pathTypeFile
	}

	if isFile(fi) {
		return pathTypeFile
	}

	dirEntries, err := os.ReadDir(dirpath)
	if err != nil {
		slog.Error("getPathType: readDir error", "error", err)
	}

	for _, entry := range dirEntries {
		if isFile(entry) && isSupportedBookFile(entry.Name()) {
			return pathTypeDirOfFiles
		}
	}
	// Directory of directories
	return pathTypeDirOfDirs
}

func timeNowFunc() func() time.Time {
	t := time.Now()
	return func() time.Time { return t }
}

// verifyPath uses trustedRoot to avoid http path traversal
// from https://www.stackhawk.com/blog/golang-path-traversal-guide-examples-and-prevention/
func verifyPath(path, trustedRoot string) (string, error) {
	// clean is already used upstream but leaving this
	// to keep the functionality of the function as close as possible to the blog.
	c := filepath.Clean(path)

	// get the canonical path
	r, err := filepath.EvalSymlinks(c)
	if err != nil {
		slog.Error("verifyPath error", "error", err)
		return c, errors.New("unsafe or invalid path specified")
	}

	if !inTrustedRoot(r, trustedRoot) {
		return r, errors.New("unsafe or invalid path specified")
	}

	return r, nil
}

func inTrustedRoot(path string, trustedRoot string) bool {
	path = filepath.Clean(path)
	trustedRoot = filepath.Clean(trustedRoot)
	if path == trustedRoot {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(path, trustedRoot+sep)
}
