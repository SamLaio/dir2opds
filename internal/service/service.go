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
	"encoding/json"
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
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SamLaio/dir2opds/opds"
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
	_ = mime.AddExtensionType(".azw", "application/vnd.amazon.ebook")
	_ = mime.AddExtensionType(".azw3", "application/vnd.amazon.ebook")
	_ = mime.AddExtensionType(".azw4", "application/vnd.amazon.ebook")
	_ = mime.AddExtensionType(".cbr", "application/x-cbr")
	_ = mime.AddExtensionType(".cbz", "application/x-cbz")
	_ = mime.AddExtensionType(".djv", "image/vnd.djvu")
	_ = mime.AddExtensionType(".djvu", "image/vnd.djvu")
	_ = mime.AddExtensionType(".epub", "application/epub+zip")
	_ = mime.AddExtensionType(".fb2", "text/fb2+xml")
	_ = mime.AddExtensionType(".kepub", "application/epub+zip")
	_ = mime.AddExtensionType(".kfx", "application/vnd.amazon.ebook")
	_ = mime.AddExtensionType(".lit", "application/x-ms-reader")
	_ = mime.AddExtensionType(".mobi", "application/x-mobipocket-ebook")
	_ = mime.AddExtensionType(".pdf", "application/pdf")
	_ = mime.AddExtensionType(".prc", "application/x-mobipocket-ebook")
	_ = mime.AddExtensionType(".txt", "text/plain; charset=utf-8")
}

const (
	pathTypeFile = iota
	pathTypeDirOfDirs
	pathTypeDirOfFiles
)

const (
	defaultPageSize     = 50
	maxPageSize         = 50
	backgroundBatchSize = 50
	bookIndexVersion    = 4
	staticXMLVersion    = 5

	maxCoverReadBytes        = 8 * 1024 * 1024
	maxPDFImageScanBytes     = 16 * 1024 * 1024
	maxPDFEmbeddedImageBytes = maxCoverReadBytes
)

const (
	ignoreFile       = true
	includeFile      = false
	currentDirectory = "."
	parentDirectory  = ".."
	hiddenFilePrefix = "."
	thumbDirectory   = "thumb"
	legacyThumbDir   = ".thumb"
)

var supportedBookExtensions = map[string]bool{
	".azw":   true,
	".azw3":  true,
	".azw4":  true,
	".cbr":   true,
	".cbz":   true,
	".djv":   true,
	".djvu":  true,
	".epub":  true,
	".fb2":   true,
	".kepub": true,
	".kfx":   true,
	".lit":   true,
	".mobi":  true,
	".pdf":   true,
	".prc":   true,
	".zip":   true,
}

var diskBookIndexMu sync.Mutex

type OPDS struct {
	TrustedRoot      string
	ThumbDir         string
	Libraries        []Library
	URLPrefix        string
	HideCalibreFiles bool
	HideDotFiles     bool
	NoCache          bool
	EnableCache      bool
	SortBy           string
	ShowCovers       bool
	MimeMap          map[string]string
	TypeFilter       string
	EnableSearch     bool
	ExtractMetadata  bool
	CoverWarmup      bool
	EnableHTML       bool
	BaseURL          string
	PageSize         int
	NoPagination     bool
}

// Library describes one configured book library.
type Library struct {
	Name string
	Slug string
	Path string
}

type bookIndexFingerprint struct {
	Count         int
	LatestModTime time.Time
	Hash          string
}

type staticXMLCacheMeta struct {
	Version          int    `json:"version"`
	RequestURI       string `json:"request_uri"`
	TrustedRoot      string `json:"trusted_root"`
	BaseURL          string `json:"base_url"`
	Scope            string `json:"scope"`
	PathType         int    `json:"path_type"`
	HideCalibreFiles bool   `json:"hide_calibre_files"`
	HideDotFiles     bool   `json:"hide_dot_files"`
	ShowCovers       bool   `json:"show_covers"`
	PageSize         int    `json:"page_size"`
	NoPagination     bool   `json:"no_pagination"`
	Count            int    `json:"count"`
	LatestModTime    int64  `json:"latest_mod_time"`
	Hash             string `json:"hash"`
}

type diskBookIndexMeta struct {
	RootPath         string `json:"root_path"`
	TrustedRoot      string `json:"trusted_root"`
	HideCalibreFiles bool   `json:"hide_calibre_files"`
	HideDotFiles     bool   `json:"hide_dot_files"`
	Version          int    `json:"version"`
	Count            int    `json:"count"`
	LatestModTime    int64  `json:"latest_mod_time"`
	Hash             string `json:"hash"`
}

type diskBookRecord struct {
	Name    string `json:"name"`
	Href    string `json:"href"`
	ModTime int64  `json:"mod_time"`
	Size    int64  `json:"size"`
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

func (s OPDS) hasLibraries() bool {
	return len(s.Libraries) > 0 && s.URLPrefix == ""
}

func (s OPDS) libraryThumbDir(library Library) string {
	slug := library.Slug
	if slug == "" {
		slug = library.Name
	}
	if slug == "" {
		slug = "library"
	}
	return filepath.Join(s.thumbDir(), "libraries", slug)
}

func (s OPDS) forLibrary(library Library) OPDS {
	next := s
	next.TrustedRoot = library.Path
	next.ThumbDir = s.libraryThumbDir(library)
	next.Libraries = nil
	next.URLPrefix = "/" + strings.Trim(strings.TrimSpace(library.Slug), "/")
	if next.URLPrefix == "/" {
		next.URLPrefix = "/" + strings.Trim(strings.TrimSpace(library.Name), "/")
	}
	return next
}

func (s OPDS) trimURLPrefix(urlPath string) (string, bool) {
	if urlPath == "" {
		urlPath = "/"
	}
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}

	prefix := strings.TrimRight(s.URLPrefix, "/")
	if prefix == "" {
		return urlPath, true
	}
	if urlPath == prefix {
		return "/", true
	}
	if strings.HasPrefix(urlPath, prefix+"/") {
		return strings.TrimPrefix(urlPath, prefix), true
	}
	return "", false
}

func (s OPDS) resolveLibraryURLPath(urlPath string) (Library, string, bool) {
	if urlPath == "" {
		urlPath = "/"
	}
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}
	cleanURLPath := path.Clean(urlPath)
	if cleanURLPath == "/" {
		return Library{}, "", false
	}

	parts := strings.Split(strings.TrimPrefix(cleanURLPath, "/"), "/")
	slug := parts[0]
	rest := "/"
	if len(parts) > 1 {
		rest = "/" + strings.Join(parts[1:], "/")
	}
	for _, library := range s.Libraries {
		if library.Slug == slug {
			return library, rest, true
		}
	}
	return Library{}, "", false
}

func (s OPDS) filePathForURLPath(urlPath string) (string, error) {
	if urlPath == "" {
		urlPath = "/"
	}
	if !strings.HasPrefix(urlPath, "/") {
		urlPath = "/" + urlPath
	}

	if s.hasLibraries() {
		library, rest, ok := s.resolveLibraryURLPath(urlPath)
		if !ok {
			return "", fmt.Errorf("library not found for path %s", urlPath)
		}
		fPath := filepath.Join(library.Path, filepath.FromSlash(strings.TrimPrefix(rest, "/")))
		return verifyPath(fPath, library.Path)
	}

	fsURLPath, ok := s.trimURLPrefix(urlPath)
	if !ok {
		return "", fmt.Errorf("path %s is outside URL prefix %s", urlPath, s.URLPrefix)
	}
	fPath := filepath.Join(s.TrustedRoot, filepath.FromSlash(strings.TrimPrefix(fsURLPath, "/")))
	return verifyPath(fPath, s.TrustedRoot)
}

func (s OPDS) urlPathForRel(relPath string) string {
	relPath = strings.TrimPrefix(filepath.ToSlash(relPath), "/")
	if s.URLPrefix == "" {
		return "/" + relPath
	}
	if relPath == "" {
		return s.URLPrefix
	}
	return path.Join(s.URLPrefix, relPath)
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
		if !entry.IsDir() && !entryMatchesBookType(entry.Name(), s.TypeFilter) {
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
	s.enrichVisibleEntries(catalog, fPath, urlPath)

	return catalog, nil
}

func (s OPDS) enrichVisibleEntries(catalog *Catalog, rootPath, urlPath string) {
	if !s.ExtractMetadata {
		return
	}

	for idx := range catalog.Entries {
		if catalog.Entries[idx].Type != pathTypeFile {
			continue
		}

		entryPath := filepath.Join(rootPath, catalog.Entries[idx].Name)
		if catalog.Entries[idx].Href != "" {
			if hrefPath, err := url.PathUnescape(catalog.Entries[idx].Href); err == nil {
				if resolvedPath, err := s.filePathForURLPath(hrefPath); err == nil {
					entryPath = resolvedPath
				}
			}
		} else if urlPath != "" {
			if resolvedPath, err := s.filePathForURLPath(path.Join(urlPath, catalog.Entries[idx].Name)); err == nil {
				entryPath = resolvedPath
			}
		}

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

func (s OPDS) WarmBookIndex() {
	go func() {
		start := time.Now()
		if s.hasLibraries() {
			total := 0
			for _, library := range s.Libraries {
				meta, err := s.forLibrary(library).ensureDiskBookIndex(library.Path)
				if err != nil {
					slog.Error("book index warmup failed", "library", library.Name, "error", err)
					return
				}
				total += meta.Count
			}
			slog.Info("book indexes warmed", "libraries", len(s.Libraries), "entries", total, "duration", time.Since(start).String())
			if s.ExtractMetadata && s.CoverWarmup {
				for _, library := range s.Libraries {
					s.forLibrary(library).warmBookCoversInBatches(backgroundBatchSize)
				}
			}
			return
		}

		meta, err := s.ensureDiskBookIndex(s.TrustedRoot)
		if err != nil {
			slog.Error("book index warmup failed", "error", err)
			return
		}
		slog.Info("book index warmed", "entries", meta.Count, "duration", time.Since(start).String())
		if s.ExtractMetadata && s.CoverWarmup {
			s.warmBookCoversInBatches(backgroundBatchSize)
		}
	}()
}

func (s OPDS) warmBookCoversInBatches(batchSize int) {
	if batchSize <= 0 {
		batchSize = backgroundBatchSize
	}

	f, err := os.Open(s.diskBookIndexPath("name"))
	if err != nil {
		slog.Debug("book cover warmup skipped", "error", err)
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	batch := make([]diskBookRecord, 0, batchSize)
	processed := 0
	batchNumber := 0
	for scanner.Scan() {
		var record diskBookRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			slog.Debug("book cover warmup record skipped", "error", err)
			continue
		}
		batch = append(batch, record)
		if len(batch) == batchSize {
			batchNumber++
			processed += s.processCoverWarmupBatch(batch, batchNumber)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		batchNumber++
		processed += s.processCoverWarmupBatch(batch, batchNumber)
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("book cover warmup scan failed", "error", err)
	}
	slog.Info("book cover warmup finished", "processed", processed, "batch_size", batchSize)
}

func (s OPDS) processCoverWarmupBatch(batch []diskBookRecord, batchNumber int) int {
	processed := 0
	for _, record := range batch {
		hrefPath, err := url.PathUnescape(record.Href)
		if err != nil {
			continue
		}
		sourcePath, err := s.filePathForURLPath(hrefPath)
		if err != nil {
			continue
		}
		if _, err := ensureCachedCover(s.thumbDir(), sourcePath); err != nil {
			slog.Debug("book cover warmup skipped cover", "path", sourcePath, "error", err)
		}
		processed++
	}
	slog.Debug("book cover warmup batch finished", "batch", batchNumber, "count", processed)
	runtime.GC()
	return processed
}

func writeFingerprintEntry(h hashWriter, name string, pathType int, size int64, modTime time.Time) {
	_, _ = io.WriteString(h, name)
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, strconv.Itoa(pathType))
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, strconv.FormatInt(size, 10))
	_, _ = io.WriteString(h, "\x00")
	_, _ = io.WriteString(h, strconv.FormatInt(modTime.UTC().UnixNano(), 10))
	_, _ = io.WriteString(h, "\x00")
}

type hashWriter interface {
	io.Writer
}

func (s OPDS) scanBookFingerprint(rootPath string) (bookIndexFingerprint, error) {
	var fingerprint bookIndexFingerprint
	h := sha256.New()
	err := filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if fileShouldBeIgnored(entry.Name(), s.HideCalibreFiles, s.HideDotFiles) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !isSupportedBookFile(entry.Name()) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			slog.Debug("error getting info for indexed entry", "path", path, "error", err)
			return nil
		}
		fingerprint.Count++
		if fingerprint.LatestModTime.IsZero() || info.ModTime().After(fingerprint.LatestModTime) {
			fingerprint.LatestModTime = info.ModTime()
		}
		relPath, err := filepath.Rel(s.TrustedRoot, path)
		if err != nil {
			return err
		}
		writeFingerprintEntry(h, filepath.ToSlash(relPath), pathTypeFile, info.Size(), info.ModTime())
		return nil
	})
	if err != nil {
		return bookIndexFingerprint{}, err
	}
	fingerprint.Hash = hex.EncodeToString(h.Sum(nil))
	return fingerprint, nil
}

func (s OPDS) scanBookEntries(rootPath string) ([]CatalogEntry, bookIndexFingerprint, error) {
	var entries []CatalogEntry
	var fingerprint bookIndexFingerprint
	h := sha256.New()

	err := filepath.WalkDir(rootPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if fileShouldBeIgnored(entry.Name(), s.HideCalibreFiles, s.HideDotFiles) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !isSupportedBookFile(entry.Name()) {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			slog.Debug("error getting info for indexed entry", "path", path, "error", err)
			return nil
		}

		relPath, err := filepath.Rel(s.TrustedRoot, path)
		if err != nil {
			return err
		}

		fingerprint.Count++
		if fingerprint.LatestModTime.IsZero() || info.ModTime().After(fingerprint.LatestModTime) {
			fingerprint.LatestModTime = info.ModTime()
		}
		writeFingerprintEntry(h, filepath.ToSlash(relPath), pathTypeFile, info.Size(), info.ModTime())

		entries = append(entries, CatalogEntry{
			Name:    info.Name(),
			Type:    pathTypeFile,
			Href:    s.urlPathForRel(relPath),
			ModTime: info.ModTime(),
			Size:    info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, bookIndexFingerprint{}, err
	}
	fingerprint.Hash = hex.EncodeToString(h.Sum(nil))

	return entries, fingerprint, nil
}

func (s OPDS) diskBookIndexBase() string {
	return filepath.Join(s.thumbDir(), "book-index")
}

func (s OPDS) diskBookIndexMetaPath() string {
	return s.diskBookIndexBase() + ".meta.json"
}

func (s OPDS) diskBookIndexPath(sortBy string) string {
	switch sortBy {
	case "date":
		return s.diskBookIndexBase() + ".date.jsonl"
	case "type":
		return s.diskBookIndexBase() + ".type.jsonl"
	default:
		return s.diskBookIndexBase() + ".name.jsonl"
	}
}

func (s OPDS) expectedDiskBookIndexMeta(rootPath string, fingerprint bookIndexFingerprint) diskBookIndexMeta {
	return diskBookIndexMeta{
		RootPath:         rootPath,
		TrustedRoot:      s.TrustedRoot,
		HideCalibreFiles: s.HideCalibreFiles,
		HideDotFiles:     s.HideDotFiles,
		Version:          bookIndexVersion,
		Count:            fingerprint.Count,
		LatestModTime:    fingerprint.LatestModTime.UnixNano(),
		Hash:             fingerprint.Hash,
	}
}

func (m diskBookIndexMeta) matches(other diskBookIndexMeta) bool {
	return m.RootPath == other.RootPath &&
		m.TrustedRoot == other.TrustedRoot &&
		m.HideCalibreFiles == other.HideCalibreFiles &&
		m.HideDotFiles == other.HideDotFiles &&
		m.Version == other.Version &&
		m.Count == other.Count &&
		m.LatestModTime == other.LatestModTime &&
		m.Hash == other.Hash
}

func (s OPDS) readDiskBookIndexMeta() (diskBookIndexMeta, error) {
	data, err := os.ReadFile(s.diskBookIndexMetaPath())
	if err != nil {
		return diskBookIndexMeta{}, err
	}
	var meta diskBookIndexMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return diskBookIndexMeta{}, err
	}
	return meta, nil
}

func (s OPDS) ensureDiskBookIndex(rootPath string) (diskBookIndexMeta, error) {
	diskBookIndexMu.Lock()
	defer diskBookIndexMu.Unlock()

	fingerprint, err := s.scanBookFingerprint(rootPath)
	if err != nil {
		return diskBookIndexMeta{}, err
	}
	expected := s.expectedDiskBookIndexMeta(rootPath, fingerprint)

	if existing, err := s.readDiskBookIndexMeta(); err == nil && existing.matches(expected) {
		if _, err := os.Stat(s.diskBookIndexPath("name")); err == nil {
			if _, err := os.Stat(s.diskBookIndexPath("date")); err == nil {
				if _, err := os.Stat(s.diskBookIndexPath("type")); err == nil {
					return existing, nil
				}
			}
		}
	}

	start := time.Now()
	entries, fingerprint, err := s.scanBookEntries(rootPath)
	if err != nil {
		return diskBookIndexMeta{}, err
	}
	meta := s.expectedDiskBookIndexMeta(rootPath, fingerprint)
	if err := s.writeDiskBookIndexes(entries, meta); err != nil {
		return diskBookIndexMeta{}, err
	}
	slog.Info("disk book index refreshed", "root", rootPath, "entries", meta.Count, "duration", time.Since(start).String())
	return meta, nil
}

func (s OPDS) writeDiskBookIndexes(entries []CatalogEntry, meta diskBookIndexMeta) error {
	byName := make([]CatalogEntry, len(entries))
	copy(byName, entries)
	sort.Slice(byName, func(i, j int) bool {
		return byName[i].Name < byName[j].Name
	})
	if err := s.writeDiskBookIndexFile(s.diskBookIndexPath("name"), byName); err != nil {
		return err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ModTime.After(entries[j].ModTime)
	})
	if err := s.writeDiskBookIndexFile(s.diskBookIndexPath("date"), entries); err != nil {
		return err
	}

	byType := make([]CatalogEntry, len(entries))
	copy(byType, entries)
	sortEntriesByType(byType)
	if err := s.writeDiskBookIndexFile(s.diskBookIndexPath("type"), byType); err != nil {
		return err
	}

	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.diskBookIndexMetaPath(), metaData, 0o644); err != nil {
		return err
	}
	s.clearStaticXMLCache()
	return nil
}

func (s OPDS) writeDiskBookIndexFile(indexPath string, entries []CatalogEntry) error {
	tmpPath := indexPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(f)
	for _, entry := range entries {
		record := diskBookRecord{
			Name:    entry.Name,
			Href:    entry.Href,
			ModTime: entry.ModTime.UnixNano(),
			Size:    entry.Size,
		}
		if err := enc.Encode(record); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, indexPath)
}

func diskBookRecordToCatalogEntry(record diskBookRecord) CatalogEntry {
	return CatalogEntry{
		Name:    record.Name,
		Type:    pathTypeFile,
		Href:    record.Href,
		ModTime: time.Unix(0, record.ModTime),
		Size:    record.Size,
	}
}

func (s OPDS) readDiskBookIndexPage(rootPath, sortBy string, page, pageSize int, query string, fileType string) ([]CatalogEntry, int, time.Time, error) {
	meta, err := s.ensureDiskBookIndex(rootPath)
	if err != nil {
		return nil, 0, time.Time{}, err
	}

	f, err := os.Open(s.diskBookIndexPath(sortBy))
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	defer f.Close()

	if page < 1 {
		page = 1
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	queryLower := strings.ToLower(query)

	var entries []CatalogEntry
	total := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var record diskBookRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, 0, time.Time{}, err
		}
		if queryLower != "" && !strings.Contains(strings.ToLower(record.Name), queryLower) {
			continue
		}
		if !entryMatchesBookType(record.Name, fileType) {
			continue
		}
		if total >= start && total < end {
			entries = append(entries, diskBookRecordToCatalogEntry(record))
		}
		total++
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, time.Time{}, err
	}

	if query == "" && normalizeBookType(fileType) == "" {
		total = meta.Count
	}
	return entries, total, time.Unix(0, meta.LatestModTime), nil
}

func (s OPDS) readDiskBookIndexEntries(rootPath, sortBy string, query string, fileType string) ([]CatalogEntry, time.Time, error) {
	meta, err := s.ensureDiskBookIndex(rootPath)
	if err != nil {
		return nil, time.Time{}, err
	}

	f, err := os.Open(s.diskBookIndexPath(sortBy))
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()

	queryLower := strings.ToLower(query)
	var entries []CatalogEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var record diskBookRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, time.Time{}, err
		}
		if queryLower != "" && !strings.Contains(strings.ToLower(record.Name), queryLower) {
			continue
		}
		if !entryMatchesBookType(record.Name, fileType) {
			continue
		}
		entries = append(entries, diskBookRecordToCatalogEntry(record))
	}
	if err := scanner.Err(); err != nil {
		return nil, time.Time{}, err
	}
	return entries, time.Unix(0, meta.LatestModTime), nil
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

func cachedThumbPathForSource(baseDir, sourcePath string) string {
	sum := sha1.Sum([]byte(sourcePath))
	prefix := hex.EncodeToString(sum[:])
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg"} {
		thumbPath := filepath.Join(baseDir, prefix+ext)
		if _, err := os.Stat(thumbPath); err == nil {
			return thumbPath
		}
	}
	return ""
}

func ensureCachedCover(baseDir, sourcePath string) (string, error) {
	if cachedPath := cachedThumbPathForSource(baseDir, sourcePath); cachedPath != "" {
		return cachedPath, nil
	}

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

func openAdvisedZipReader(filePath string) (*zip.Reader, func(), error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, err
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}

	r, err := zip.NewReader(f, info.Size())
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}

	return r, func() {
		fadviseDontNeed(f)
		_ = f.Close()
	}, nil
}

func extractEpubMetadata(path string) (string, string, string) {
	r, closeZip, err := openAdvisedZipReader(path)
	if err != nil {
		return "", "", ""
	}
	defer closeZip()

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

func findEpubCover(r *zip.Reader, items []epubManifestItem, metas []epubMeta, opfPath string) string {
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

func coverPathFromItem(r *zip.Reader, opfDir string, item epubManifestItem) string {
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

func coverPathFromHTML(r *zip.Reader, opfDir, htmlPath string) string {
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
			return entries[i].ModTime.After(entries[j].ModTime)
		})
	case "size":
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Size > entries[j].Size
		})
	case "type":
		sortEntriesByType(entries)
	default: // name
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].Name < entries[j].Name
		})
	}
}

func sortEntriesByType(entries []CatalogEntry) {
	sort.Slice(entries, func(i, j int) bool {
		leftExt := strings.ToLower(filepath.Ext(entries[i].Name))
		rightExt := strings.ToLower(filepath.Ext(entries[j].Name))
		if leftExt != rightExt {
			return leftExt < rightExt
		}
		return entries[i].Name < entries[j].Name
	})
}

func isBrowser(r *http.Request) bool {
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}

func (s OPDS) staticXMLCacheDir() string {
	dir := filepath.Join(s.thumbDir(), "static-xml")
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func (s OPDS) staticXMLCachePath(req *http.Request) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(staticXMLVersion) + "|" + s.BaseURL + "|" + req.URL.RequestURI()))
	return filepath.Join(s.staticXMLCacheDir(), hex.EncodeToString(sum[:])+".xml")
}

func staticXMLCacheTypePath(xmlPath string) string {
	return xmlPath + ".type"
}

func staticXMLCacheETagPath(xmlPath string) string {
	return xmlPath + ".etag"
}

func staticXMLCacheMetaPath(xmlPath string) string {
	return xmlPath + ".meta.json"
}

func (s OPDS) serveStaticXMLCache(w http.ResponseWriter, req *http.Request) bool {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return false
	}
	if s.EnableHTML && isBrowser(req) {
		return false
	}

	content, contentType, ok := s.readStaticXMLCache(req)
	if !ok {
		return false
	}

	xmlPath := s.staticXMLCachePath(req)
	if s.EnableCache {
		if data, err := os.ReadFile(staticXMLCacheETagPath(xmlPath)); err == nil {
			eTag := strings.TrimSpace(string(data))
			if eTag != "" {
				w.Header().Set("ETag", eTag)
				if req.Header.Get("If-None-Match") == eTag {
					w.WriteHeader(http.StatusNotModified)
					return true
				}
			}
		}
	}

	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, req, "feed.xml", TimeNow(), bytes.NewReader(content))
	return true
}

func (s OPDS) renderStaticXMLCacheAsHTML(w http.ResponseWriter, req *http.Request) bool {
	if !s.EnableHTML || !isBrowser(req) {
		return false
	}
	content, _, ok := s.readStaticXMLCache(req)
	if !ok {
		return false
	}
	if err := s.renderHTMLFromXML(w, req, content); err != nil {
		slog.Debug("render static xml as html failed", "uri", req.URL.RequestURI(), "error", err)
		return false
	}
	return true
}

func (s OPDS) readStaticXMLCache(req *http.Request) ([]byte, string, bool) {
	xmlPath := s.staticXMLCachePath(req)
	if !s.staticXMLCacheIsFresh(req, xmlPath) {
		return nil, "", false
	}
	content, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, "", false
	}

	contentType := "application/atom+xml"
	if data, err := os.ReadFile(staticXMLCacheTypePath(xmlPath)); err == nil {
		contentType = strings.TrimSpace(string(data))
	}
	return content, contentType, true
}

func (s OPDS) staticXMLCacheIsFresh(req *http.Request, xmlPath string) bool {
	data, err := os.ReadFile(staticXMLCacheMetaPath(xmlPath))
	if err != nil {
		return false
	}
	var cached staticXMLCacheMeta
	if err := json.Unmarshal(data, &cached); err != nil {
		return false
	}
	current, err := s.staticXMLCacheMeta(req)
	if err != nil {
		slog.Debug("static xml cache freshness check failed", "uri", req.URL.RequestURI(), "error", err)
		return false
	}
	return cached.matches(current)
}

func (m staticXMLCacheMeta) matches(other staticXMLCacheMeta) bool {
	return m.Version == other.Version &&
		m.RequestURI == other.RequestURI &&
		m.TrustedRoot == other.TrustedRoot &&
		m.BaseURL == other.BaseURL &&
		m.Scope == other.Scope &&
		m.PathType == other.PathType &&
		m.HideCalibreFiles == other.HideCalibreFiles &&
		m.HideDotFiles == other.HideDotFiles &&
		m.ShowCovers == other.ShowCovers &&
		m.PageSize == other.PageSize &&
		m.NoPagination == other.NoPagination &&
		m.Count == other.Count &&
		m.LatestModTime == other.LatestModTime &&
		m.Hash == other.Hash
}

func (s OPDS) writeStaticXMLCache(req *http.Request, contentType string, content []byte) {
	s.writeStaticXMLCacheWithETag(req, contentType, content, "")
}

func (s OPDS) writeStaticXMLCacheWithETag(req *http.Request, contentType string, content []byte, eTag string) {
	if req.Method != http.MethodGet {
		return
	}

	xmlPath := s.staticXMLCachePath(req)
	tmpPath := xmlPath + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0o644); err != nil {
		slog.Debug("write static xml cache failed", "path", xmlPath, "error", err)
		return
	}
	if err := os.Rename(tmpPath, xmlPath); err != nil {
		slog.Debug("rename static xml cache failed", "path", xmlPath, "error", err)
		return
	}
	if err := os.WriteFile(staticXMLCacheTypePath(xmlPath), []byte(contentType), 0o644); err != nil {
		slog.Debug("write static xml cache content type failed", "path", xmlPath, "error", err)
	}
	if eTag != "" {
		if err := os.WriteFile(staticXMLCacheETagPath(xmlPath), []byte(eTag), 0o644); err != nil {
			slog.Debug("write static xml cache etag failed", "path", xmlPath, "error", err)
		}
	}
	meta, err := s.staticXMLCacheMeta(req)
	if err != nil {
		slog.Debug("build static xml cache meta failed", "path", xmlPath, "error", err)
		return
	}
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		slog.Debug("marshal static xml cache meta failed", "path", xmlPath, "error", err)
		return
	}
	if err := os.WriteFile(staticXMLCacheMetaPath(xmlPath), metaData, 0o644); err != nil {
		slog.Debug("write static xml cache meta failed", "path", xmlPath, "error", err)
	}
}

func (s OPDS) writeCatalogStaticXMLCache(req *http.Request, catalog *Catalog) error {
	contentType, content, err := s.catalogFeedContent(catalog, req)
	if err != nil {
		return err
	}
	s.writeStaticXMLCache(req, contentType, content)
	return nil
}

func (s OPDS) writeSortSelectionStaticXMLCache(req *http.Request) error {
	content, err := xml.MarshalIndent(s.makeSortSelectionFeed(req), "  ", "    ")
	if err != nil {
		return err
	}
	content = append([]byte(xml.Header), content...)
	s.writeStaticXMLCache(req, navigationType, content)
	return nil
}

func (s OPDS) staticXMLCacheMeta(req *http.Request) (staticXMLCacheMeta, error) {
	scope, count, latestModTime, fingerprintHash, pathType, err := s.staticXMLFingerprint(req)
	if err != nil {
		return staticXMLCacheMeta{}, err
	}
	return staticXMLCacheMeta{
		Version:          staticXMLVersion,
		RequestURI:       req.URL.RequestURI(),
		TrustedRoot:      s.TrustedRoot,
		BaseURL:          s.BaseURL,
		Scope:            scope,
		PathType:         pathType,
		HideCalibreFiles: s.HideCalibreFiles,
		HideDotFiles:     s.HideDotFiles,
		ShowCovers:       s.ShowCovers,
		PageSize:         s.pageSize(),
		NoPagination:     s.NoPagination,
		Count:            count,
		LatestModTime:    latestModTime.UTC().UnixNano(),
		Hash:             fingerprintHash,
	}, nil
}

func (s OPDS) staticXMLFingerprint(req *http.Request) (string, int, time.Time, string, int, error) {
	if s.hasLibraries() {
		if req.URL.Path == "/" && req.URL.Query().Get("sort") == "" {
			return s.librariesStaticXMLFingerprint()
		}
		if req.URL.Path == "/search" || req.URL.Path == "/" {
			fingerprint, err := s.multiLibraryBookFingerprint()
			if err != nil {
				return "", 0, time.Time{}, "", 0, err
			}
			return "book-index", fingerprint.Count, fingerprint.LatestModTime, fingerprint.Hash, pathTypeDirOfFiles, nil
		}
	}

	if req.URL.Path == "/search" || (req.URL.Path == "/" && req.URL.Query().Get("sort") != "") {
		fingerprint, err := s.scanBookFingerprint(s.TrustedRoot)
		if err != nil {
			return "", 0, time.Time{}, "", 0, err
		}
		return "book-index", fingerprint.Count, fingerprint.LatestModTime, fingerprint.Hash, pathTypeDirOfFiles, nil
	}

	urlPath, err := url.PathUnescape(req.URL.Path)
	if err != nil {
		return "", 0, time.Time{}, "", 0, err
	}
	fPath, err := s.filePathForURLPath(urlPath)
	if err != nil {
		return "", 0, time.Time{}, "", 0, err
	}
	return s.directoryStaticXMLFingerprint(fPath)
}

func (s OPDS) librariesStaticXMLFingerprint() (string, int, time.Time, string, int, error) {
	h := sha256.New()
	var latestModTime time.Time
	for _, library := range s.Libraries {
		info, err := os.Stat(library.Path)
		if err != nil {
			return "", 0, time.Time{}, "", 0, err
		}
		if latestModTime.IsZero() || info.ModTime().After(latestModTime) {
			latestModTime = info.ModTime()
		}
		writeFingerprintEntry(h, library.Slug+"\x00"+library.Name+"\x00"+library.Path, pathTypeDirOfDirs, 0, info.ModTime())
	}
	return "libraries", len(s.Libraries), latestModTime, hex.EncodeToString(h.Sum(nil)), pathTypeDirOfDirs, nil
}

func (s OPDS) multiLibraryBookFingerprint() (bookIndexFingerprint, error) {
	var fingerprint bookIndexFingerprint
	h := sha256.New()
	for _, library := range s.Libraries {
		libraryFingerprint, err := s.forLibrary(library).scanBookFingerprint(library.Path)
		if err != nil {
			return bookIndexFingerprint{}, err
		}
		fingerprint.Count += libraryFingerprint.Count
		if fingerprint.LatestModTime.IsZero() || libraryFingerprint.LatestModTime.After(fingerprint.LatestModTime) {
			fingerprint.LatestModTime = libraryFingerprint.LatestModTime
		}
		writeFingerprintEntry(h, library.Slug, pathTypeDirOfFiles, int64(libraryFingerprint.Count), libraryFingerprint.LatestModTime)
		_, _ = io.WriteString(h, libraryFingerprint.Hash)
	}
	fingerprint.Hash = hex.EncodeToString(h.Sum(nil))
	return fingerprint, nil
}

func (s OPDS) directoryStaticXMLFingerprint(fPath string) (string, int, time.Time, string, int, error) {
	dirInfo, err := os.Stat(fPath)
	if err != nil {
		return "", 0, time.Time{}, "", 0, err
	}
	if isFile(dirInfo) {
		return "", 0, time.Time{}, "", 0, fmt.Errorf("static xml cache path is not a directory: %s", fPath)
	}

	dirEntries, err := os.ReadDir(fPath)
	if err != nil {
		return "", 0, time.Time{}, "", 0, err
	}

	h := sha256.New()
	pathType := pathTypeDirOfDirs
	latestModTime := dirInfo.ModTime()
	count := 0
	for _, entry := range dirEntries {
		if fileShouldBeIgnored(entry.Name(), s.HideCalibreFiles, s.HideDotFiles) {
			continue
		}
		if s.ShowCovers && (entry.Name() == "cover.jpg" || entry.Name() == "folder.jpg") {
			continue
		}

		entryPath := filepath.Join(fPath, entry.Name())
		info, err := entry.Info()
		if err != nil {
			slog.Debug("error getting info for static xml fingerprint", "path", entryPath, "error", err)
			continue
		}
		entryPathType := getPathType(entryPath)
		if !entry.IsDir() {
			if !isSupportedBookFile(entry.Name()) {
				continue
			}
			pathType = pathTypeDirOfFiles
		}

		count++
		if info.ModTime().After(latestModTime) {
			latestModTime = info.ModTime()
		}
		writeFingerprintEntry(h, entry.Name(), entryPathType, info.Size(), info.ModTime())
	}

	return "directory", count, latestModTime, hex.EncodeToString(h.Sum(nil)), pathType, nil
}

func (s OPDS) clearStaticXMLCache() {
	dir := s.staticXMLCacheDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".xml") || strings.HasSuffix(name, ".xml.type") || strings.HasSuffix(name, ".xml.etag") || strings.HasSuffix(name, ".xml.tmp") || strings.HasSuffix(name, ".xml.meta.json") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

func (s OPDS) multiLibraryHandler(w http.ResponseWriter, req *http.Request) error {
	urlPath, err := url.PathUnescape(req.URL.Path)
	if err != nil {
		slog.Error("error unescaping path", "urlPath", req.URL.Path, "error", err)
		return err
	}

	if urlPath == "/" {
		if s.NoCache {
			w.Header().Add("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Add("Expires", "0")
		}
		if s.renderStaticXMLCacheAsHTML(w, req) || s.serveStaticXMLCache(w, req) {
			return nil
		}

		sortBy := req.URL.Query().Get("sort")
		if sortBy == "" {
			catalog := s.makeLibrariesCatalog()
			if s.EnableHTML && isBrowser(req) {
				if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
					return err
				}
				return s.renderHTML(w, req, catalog)
			}
			return s.serveCatalogFeed(w, req, catalog)
		}

		if sortBy == "type" && normalizeBookType(req.URL.Query().Get("type")) == "" {
			catalog, err := s.collectMultiBookTypeCatalog("/", "Types in /", "", req)
			if err != nil {
				return err
			}
			if s.EnableHTML && isBrowser(req) {
				if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
					return err
				}
				return s.renderHTML(w, req, catalog)
			}
			return s.serveCatalogFeed(w, req, catalog)
		}

		typeService := s
		typeService.TypeFilter = req.URL.Query().Get("type")
		catalog, err := typeService.collectMultiBookCatalog("/", "Catalog in /", pageFromRequest(req), sortBy, "")
		if err != nil {
			return err
		}
		if s.EnableHTML && isBrowser(req) {
			if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
				return err
			}
			return s.renderHTML(w, req, catalog)
		}
		return s.serveCatalogFeed(w, req, catalog)
	}

	library, _, ok := s.resolveLibraryURLPath(urlPath)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return nil
	}
	return s.forLibrary(library).Handler(w, req)
}

func (s OPDS) makeLibrariesCatalog() *Catalog {
	catalog := &Catalog{
		ID:       "/",
		Title:    "Libraries",
		Type:     pathTypeDirOfDirs,
		Total:    len(s.Libraries),
		Page:     1,
		PageSize: len(s.Libraries),
		ModTime:  TimeNow(),
	}
	for _, library := range s.Libraries {
		catalog.Entries = append(catalog.Entries, CatalogEntry{
			Name: library.Name,
			Type: pathTypeDirOfDirs,
			Href: "/" + library.Slug,
		})
	}
	return catalog
}

// Handler serves the content of a book file or
// returns an Acquisition Feed when the entries are documents or
// returns a Navigation Feed when the entries are other folders
func (s OPDS) Handler(w http.ResponseWriter, req *http.Request) error {
	if s.hasLibraries() {
		return s.multiLibraryHandler(w, req)
	}

	var err error
	urlPath, err := url.PathUnescape(req.URL.Path)
	if err != nil {
		slog.Error("error unescaping path", "urlPath", req.URL.Path, "error", err)
		return err
	}

	// verifyPath avoid the http transversal by checking the path is under DirRoot
	fPath, err := s.filePathForURLPath(urlPath)
	if err != nil {
		slog.Debug("verify path rejected request", "path", req.URL.Path, "error", err)
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
		w.Header().Set("Content-Type", s.getFileContentType(fPath))
		http.ServeFile(w, req, fPath)
		return nil
	}

	if s.NoCache {
		w.Header().Add("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Add("Expires", "0")
	}

	if s.renderStaticXMLCacheAsHTML(w, req) || s.serveStaticXMLCache(w, req) {
		return nil
	}

	query := req.URL.Query()
	fsURLPath, _ := s.trimURLPrefix(urlPath)
	if fsURLPath == "/" {
		if query.Get("sort") == "" {
			if s.EnableHTML && isBrowser(req) {
				if err := s.writeSortSelectionStaticXMLCache(req); err != nil {
					return err
				}
				return s.renderHTML(w, req, s.makeSortSelectionCatalog(req))
			}
			return s.serveSortSelectionFeed(w, req)
		}

		if query.Get("sort") == "type" && normalizeBookType(query.Get("type")) == "" {
			catalog, err := s.collectBookTypeCatalog(fPath, urlPath, "Types in "+urlPath, "", req)
			if err != nil {
				return err
			}
			if s.EnableHTML && isBrowser(req) {
				if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
					return err
				}
				return s.renderHTML(w, req, catalog)
			}
			return s.serveCatalogFeed(w, req, catalog)
		}

		typeService := s
		typeService.TypeFilter = query.Get("type")
		catalog, err := typeService.collectBookCatalog(fPath, urlPath, "Catalog in "+urlPath, pageFromRequest(req), query.Get("sort"), "")
		if err != nil {
			return err
		}
		if s.EnableHTML && isBrowser(req) {
			if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
				return err
			}
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
	scanService.TypeFilter = query.Get("type")

	if pathType == pathTypeDirOfFiles && sortBy == "type" && normalizeBookType(query.Get("type")) == "" {
		catalog, err := s.collectDirectoryTypeCatalog(fPath, urlPath, req)
		if err != nil {
			return err
		}
		if s.EnableHTML && isBrowser(req) {
			if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
				return err
			}
			return s.renderHTML(w, req, catalog)
		}
		return s.serveCatalogFeed(w, req, catalog)
	}

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
			if err := s.writeSortSelectionStaticXMLCache(req); err != nil {
				return err
			}
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
		if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
			return err
		}
		return s.renderHTML(w, req, catalog)
	}

	return s.serveCatalogFeed(w, req, catalog)
}

func (s OPDS) serveCatalogFeed(w http.ResponseWriter, req *http.Request, catalog *Catalog) error {
	contentType, content, err := s.catalogFeedContent(catalog, req)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", contentType)
	s.writeStaticXMLCacheWithETag(req, contentType, content, w.Header().Get("ETag"))
	http.ServeContent(w, req, "feed.xml", TimeNow(), bytes.NewReader(content))

	return nil
}

func (s OPDS) catalogFeedContent(catalog *Catalog, req *http.Request) (string, []byte, error) {
	navFeed := s.makeFeed(catalog, req)
	var content []byte
	var err error
	contentType := navigationType
	// it is an acquisition feed
	if catalog.Type == pathTypeDirOfFiles {
		acFeed := &opds.AcquisitionFeed{Feed: &navFeed, Dc: dcTermsNamespace, Opds: opdsNamespace}
		content, err = xml.MarshalIndent(acFeed, "  ", "    ")
		contentType = acquisitionType
	} else { // it is a navigation feed
		content, err = xml.MarshalIndent(navFeed, "  ", "    ")
	}
	if err != nil {
		slog.Error("error marshaling feed", "error", err)
		return "", nil, err
	}

	content = append([]byte(xml.Header), content...)
	return contentType, content, nil
}

func (s OPDS) serveSortSelectionFeed(w http.ResponseWriter, req *http.Request) error {
	content, err := xml.MarshalIndent(s.makeSortSelectionFeed(req), "  ", "    ")
	if err != nil {
		slog.Error("error marshaling sort selection feed", "error", err)
		return err
	}
	w.Header().Set("Content-Type", navigationType)
	content = append([]byte(xml.Header), content...)
	s.writeStaticXMLCache(req, navigationType, content)
	http.ServeContent(w, req, "feed.xml", TimeNow(), bytes.NewReader(content))
	return nil
}

func pageFromRequest(req *http.Request) int {
	return parsePage(req.URL.Query().Get("page"))
}

func buildSortURL(basePath string, query url.Values, sortBy string) string {
	query.Set("sort", sortBy)
	query.Del("type")
	query.Del("page")
	return basePath + "?" + query.Encode()
}

func buildTypeURL(basePath string, query url.Values, fileType string) string {
	query.Set("sort", "type")
	query.Set("type", normalizeBookType(fileType))
	query.Del("page")
	return basePath + "?" + query.Encode()
}

func normalizeBookType(fileType string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileType)), ".")
}

func entryMatchesBookType(name string, fileType string) bool {
	normalized := normalizeBookType(fileType)
	if normalized == "" {
		return true
	}
	return normalizeBookType(filepath.Ext(name)) == normalized
}

func bookTypeTitle(fileType string) string {
	normalized := normalizeBookType(fileType)
	if normalized == "" {
		return "Unknown"
	}
	return strings.ToUpper(normalized)
}

func bookTypeFromName(name string) string {
	return normalizeBookType(filepath.Ext(name))
}

func (s OPDS) makeSortSelectionCatalog(req *http.Request) *Catalog {
	query := req.URL.Query()
	nameURL := buildSortURL(req.URL.Path, query, "name")
	dateURL := buildSortURL(req.URL.Path, query, "date")
	typeURL := buildSortURL(req.URL.Path, query, "type")

	return &Catalog{
		ID:       req.URL.Path,
		Title:    "Sort catalog",
		Type:     pathTypeDirOfDirs,
		Total:    3,
		Page:     1,
		PageSize: 3,
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
			{
				Name: "By Type",
				Type: pathTypeDirOfDirs,
				Href: typeURL,
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
	typeURL := s.joinURL(buildSortURL(req.URL.Path, query, "type"))

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

	feedBuilder = feedBuilder.AddEntry(opds.EntryBuilder.
		ID(typeURL).
		Title("By Type").
		Published(updated).
		Updated(updated).
		AddLink(opds.LinkBuilder.
			Rel("subsection").
			Title("By Type").
			Href(typeURL).
			Type(navigationType).
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

	entries, total, modTime, err := s.readDiskBookIndexPage(rootPath, sortBy, page, catalog.PageSize, query, s.TypeFilter)
	if err != nil {
		return nil, err
	}
	catalog.ModTime = modTime

	catalog.Total = total
	catalog.Page = page
	catalog.Entries = entries
	s.enrichVisibleEntries(catalog, rootPath, urlPath)
	return catalog, nil
}

func (s OPDS) collectBookTypeCatalog(rootPath, urlPath, title, query string, req *http.Request) (*Catalog, error) {
	entries, modTime, err := s.readDiskBookIndexEntries(rootPath, "type", query, "")
	if err != nil {
		return nil, err
	}
	return s.makeTypeSelectionCatalog(req, urlPath, title, entries, modTime), nil
}

func (s OPDS) collectMultiBookCatalog(urlPath, title string, page int, sortBy string, query string) (*Catalog, error) {
	catalog := &Catalog{
		ID:       urlPath,
		Title:    title,
		Type:     pathTypeDirOfFiles,
		Page:     page,
		PageSize: s.pageSize(),
	}

	var entries []CatalogEntry
	for _, library := range s.Libraries {
		libraryService := s.forLibrary(library)
		libraryEntries, modTime, err := libraryService.readDiskBookIndexEntries(library.Path, sortBy, query, s.TypeFilter)
		if err != nil {
			return nil, err
		}
		entries = append(entries, libraryEntries...)
		if catalog.ModTime.IsZero() || modTime.After(catalog.ModTime) {
			catalog.ModTime = modTime
		}
	}

	sortService := s
	sortService.SortBy = sortBy
	sortService.sortEntries(entries)

	total := len(entries)
	pageSize := catalog.PageSize
	if page < 1 {
		page = 1
	}
	if s.NoPagination {
		pageSize = total
		if pageSize == 0 {
			pageSize = 1
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
	catalog.Entries = entries[start:end]
	s.enrichVisibleEntries(catalog, "", urlPath)
	return catalog, nil
}

func (s OPDS) collectMultiBookTypeCatalog(urlPath, title, query string, req *http.Request) (*Catalog, error) {
	var entries []CatalogEntry
	var modTime time.Time
	for _, library := range s.Libraries {
		libraryService := s.forLibrary(library)
		libraryEntries, libraryModTime, err := libraryService.readDiskBookIndexEntries(library.Path, "type", query, "")
		if err != nil {
			return nil, err
		}
		entries = append(entries, libraryEntries...)
		if modTime.IsZero() || libraryModTime.After(modTime) {
			modTime = libraryModTime
		}
	}
	return s.makeTypeSelectionCatalog(req, urlPath, title, entries, modTime), nil
}

func (s OPDS) collectDirectoryTypeCatalog(fPath, urlPath string, req *http.Request) (*Catalog, error) {
	dirEntries, err := os.ReadDir(fPath)
	if err != nil {
		return nil, err
	}
	dirInfo, err := os.Stat(fPath)
	if err != nil {
		return nil, err
	}

	entries := make([]CatalogEntry, 0)
	modTime := dirInfo.ModTime()
	for _, entry := range dirEntries {
		if entry.IsDir() || fileShouldBeIgnored(entry.Name(), s.HideCalibreFiles, s.HideDotFiles) || !isSupportedBookFile(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			slog.Debug("error getting info for type catalog entry", "path", filepath.Join(fPath, entry.Name()), "error", err)
			continue
		}
		if info.ModTime().After(modTime) {
			modTime = info.ModTime()
		}
		entries = append(entries, CatalogEntry{Name: entry.Name(), Type: pathTypeFile, ModTime: info.ModTime(), Size: info.Size()})
	}
	return s.makeTypeSelectionCatalog(req, urlPath, "Types in "+urlPath, entries, modTime), nil
}

func (s OPDS) makeTypeSelectionCatalog(req *http.Request, urlPath, title string, entries []CatalogEntry, modTime time.Time) *Catalog {
	counts := make(map[string]int)
	for _, entry := range entries {
		fileType := bookTypeFromName(entry.Name)
		if fileType == "" {
			continue
		}
		counts[fileType]++
	}

	fileTypes := make([]string, 0, len(counts))
	for fileType := range counts {
		fileTypes = append(fileTypes, fileType)
	}
	sort.Strings(fileTypes)

	catalog := &Catalog{
		ID:       urlPath,
		Title:    title,
		Type:     pathTypeDirOfDirs,
		Total:    len(fileTypes),
		Page:     1,
		PageSize: len(fileTypes),
		ModTime:  modTime,
	}
	if catalog.PageSize == 0 {
		catalog.PageSize = 1
	}

	query := req.URL.Query()
	for _, fileType := range fileTypes {
		title := bookTypeTitle(fileType)
		catalog.Entries = append(catalog.Entries, CatalogEntry{
			Name:  fmt.Sprintf("%s (%d)", title, counts[fileType]),
			Title: title,
			Type:  pathTypeDirOfFiles,
			Href:  buildTypeURL(req.URL.Path, query, fileType),
		})
	}
	return catalog
}

// SearchHandler performs a basic filename search
func (s OPDS) SearchHandler(w http.ResponseWriter, req *http.Request) error {
	query := req.URL.Query().Get("q")
	if query == "" {
		return s.Handler(w, req)
	}

	if s.renderStaticXMLCacheAsHTML(w, req) || s.serveStaticXMLCache(w, req) {
		return nil
	}

	sortBy := req.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = s.SortBy
	}
	fileType := req.URL.Query().Get("type")

	if sortBy == "type" && normalizeBookType(fileType) == "" {
		var catalog *Catalog
		var err error
		if s.hasLibraries() {
			catalog, err = s.collectMultiBookTypeCatalog("search:"+query, "Types for: "+query, query, req)
		} else {
			catalog, err = s.collectBookTypeCatalog(s.TrustedRoot, "search:"+query, "Types for: "+query, query, req)
		}
		if err != nil {
			return err
		}
		if s.EnableHTML && isBrowser(req) {
			if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
				return err
			}
			return s.renderHTML(w, req, catalog)
		}
		return s.serveCatalogFeed(w, req, catalog)
	}

	var catalog *Catalog
	var err error
	typeService := s
	typeService.TypeFilter = fileType
	if s.hasLibraries() {
		catalog, err = typeService.collectMultiBookCatalog("search:"+query, "Search results for: "+query, pageFromRequest(req), sortBy, query)
	} else {
		catalog, err = typeService.collectBookCatalog(s.TrustedRoot, "search:"+query, "Search results for: "+query, pageFromRequest(req), sortBy, query)
	}
	if err != nil {
		return err
	}

	if s.EnableHTML && isBrowser(req) {
		if err := s.writeCatalogStaticXMLCache(req, catalog); err != nil {
			return err
		}
		return s.renderHTML(w, req, catalog)
	}
	return s.serveCatalogFeed(w, req, catalog)
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

	// verifyPath avoid the http transversal by checking the path is under TrustedRoot
	fPath, err := s.filePathForURLPath(urlPath)
	if err != nil {
		slog.Debug("verify path rejected cover request", "file", filePath, "error", err)
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
	r, closeZip, err := openAdvisedZipReader(epubPath)
	if err != nil {
		return nil, "", fmt.Errorf("opening epub: %w", err)
	}
	defer closeZip()

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

	coverData, err := readLimitedCover(coverFile)
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
	r, closeZip, err := openAdvisedZipReader(zipPath)
	if err != nil {
		return nil, "", err
	}
	defer closeZip()

	imageExt := []string{".jpg", ".jpeg", ".png", ".gif", ".webp"}
	for _, f := range r.File {
		lowerName := strings.ToLower(f.Name)
		for _, ext := range imageExt {
			if strings.HasSuffix(lowerName, ext) {
				rc, err := f.Open()
				if err != nil {
					return nil, "", err
				}
				data, err := readLimitedCover(rc)
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

func readLimitedCover(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxCoverReadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCoverReadBytes {
		return nil, fmt.Errorf("cover image is larger than %d bytes", maxCoverReadBytes)
	}
	return data, nil
}

func extractFirstImageFromPDF(pdfPath string) ([]byte, string, error) {
	f, err := os.Open(pdfPath)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		fadviseDontNeed(f)
		_ = f.Close()
	}()

	image, ext, err := extractFirstPDFImageBytes(f, maxPDFImageScanBytes)
	if err != nil {
		return nil, "", err
	}
	if len(image) > 0 {
		return image, ext, nil
	}

	return generatedPDFCover(pdfPath), ".svg", nil
}

func extractFirstPDFImageBytes(r io.Reader, scanLimit int64) ([]byte, string, error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	chunk := make([]byte, 64*1024)
	var buffer []byte
	var scanned int64

	for scanned < scanLimit {
		readSize := len(chunk)
		if remaining := scanLimit - scanned; remaining < int64(readSize) {
			readSize = int(remaining)
		}

		n, err := reader.Read(chunk[:readSize])
		if n > 0 {
			scanned += int64(n)
			buffer = append(buffer, chunk[:n]...)

			if image, ok := extractJPEGBytes(buffer); ok {
				return image, ".jpg", nil
			}
			if image, ok := extractPNGBytes(buffer); ok {
				return image, ".png", nil
			}

			buffer = trimPDFScanBuffer(buffer)
			if len(buffer) > maxPDFEmbeddedImageBytes {
				return nil, "", nil
			}
		}

		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", err
		}
	}

	return nil, "", nil
}

func trimPDFScanBuffer(buffer []byte) []byte {
	if start := bytes.Index(buffer, []byte{0xff, 0xd8, 0xff}); start >= 0 {
		return buffer[start:]
	}
	if start := bytes.Index(buffer, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}); start >= 0 {
		return buffer[start:]
	}
	if len(buffer) > 32 {
		return buffer[len(buffer)-32:]
	}
	return buffer
}

func generatedPDFCover(pdfPath string) []byte {
	title := filepath.Base(pdfPath)
	if len([]rune(title)) > 48 {
		runes := []rune(title)
		title = string(runes[:48]) + "..."
	}

	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(title))

	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" width="480" height="640" viewBox="0 0 480 640">
  <rect width="480" height="640" fill="#f7f3ea"/>
  <rect x="42" y="42" width="396" height="556" rx="18" fill="#ffffff" stroke="#d8d1c4" stroke-width="4"/>
  <rect x="82" y="112" width="316" height="12" fill="#c8c1b6"/>
  <rect x="82" y="150" width="250" height="12" fill="#ded8cf"/>
  <rect x="82" y="188" width="290" height="12" fill="#ded8cf"/>
  <text x="240" y="332" text-anchor="middle" font-family="Arial, sans-serif" font-size="58" font-weight="700" fill="#b23a2f">PDF</text>
  <text x="240" y="404" text-anchor="middle" font-family="Arial, sans-serif" font-size="24" fill="#3b4652">` + escaped.String() + `</text>
</svg>`)
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
	if end-start > maxPDFEmbeddedImageBytes {
		return nil, false
	}
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
	if end-start > maxPDFEmbeddedImageBytes {
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
		coverType := coverContentType(catalog.Cover)
		feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
			Rel("http://opds-spec.org/image").
			Href(coverHref).
			Type(coverType).
			Build())
		feedBuilder = feedBuilder.AddLink(opds.LinkBuilder.
			Rel("http://opds-spec.org/image/thumbnail").
			Href(coverHref).
			Type(coverType).
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
		if entry.Href != "" && strings.Contains(entryPath, "?") {
			href = s.joinURL(entryPath)
		}

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

	if filename == thumbDirectory || filename == legacyThumbDir {
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

func coverContentType(name string) string {
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if contentType == "" {
		return "image/jpeg"
	}
	return contentType
}

func (s OPDS) getType(name string, pathType int) string {
	switch pathType {
	case pathTypeFile:
		return s.getFileContentType(name)
	case pathTypeDirOfFiles:
		return acquisitionType
	case pathTypeDirOfDirs:
		return navigationType
	default:
		return mime.TypeByExtension("xml")
	}
}

func (s OPDS) getFileContentType(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if s.MimeMap != nil {
		if mType, ok := s.MimeMap[ext]; ok {
			return mType
		}
	}
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		return "application/zip"
	}
	return contentType
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
