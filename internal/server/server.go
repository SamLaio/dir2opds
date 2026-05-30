// Package server wires the OPDS service into an HTTP server.
package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/SamLaio/dir2opds/internal/service"
)

// LibraryConfig contains one configured book library before canonicalization.
type LibraryConfig struct {
	Name string
	Path string
}

// Config contains runtime options for the OPDS HTTP server.
type Config struct {
	Host             string
	Port             string
	DirRoot          string
	Libraries        []LibraryConfig
	HideCalibreFiles bool
	HideDotFiles     bool
	NoCache          bool
	EnableCache      bool
	EnableGzip       bool
	SortBy           string
	ShowCovers       bool
	MimeMap          map[string]string
	EnableSearch     bool
	ExtractMetadata  bool
	CoverWarmup      bool
	EnableHTML       bool
	BaseURL          string
	PageSize         int
	NoPagination     bool
}

// DefaultConfig returns the default runtime options used by the CLI and GUI.
func DefaultConfig() Config {
	return Config{
		Host:     "0.0.0.0",
		Port:     "8080",
		DirRoot:  "./books",
		SortBy:   "date",
		PageSize: 50,
	}
}

// ParseMimeMap parses custom mime types from flag syntax.
func ParseMimeMap(s string) map[string]string {
	if s == "" {
		return nil
	}
	m := make(map[string]string)
	pairs := strings.Split(s, ",")
	for _, pair := range pairs {
		kv := strings.Split(pair, ":")
		if len(kv) == 2 {
			m[kv[0]] = kv[1]
		}
	}
	return m
}

// AbsoluteCanonicalPath returns the canonical path of the absolute path that was passed.
func AbsoluteCanonicalPath(aPath string) (string, error) {
	aPath, err := filepath.Abs(aPath)
	if err != nil {
		return "", fmt.Errorf("get absolute path %s: %w", aPath, err)
	}

	aPath, err = filepath.EvalSymlinks(aPath)
	if err != nil {
		return "", fmt.Errorf("get canonical path from absolute path %s: %w", aPath, err)
	}

	return aPath, nil
}

func canonicalLibraries(libraries []LibraryConfig) ([]service.Library, error) {
	if len(libraries) == 0 {
		return nil, nil
	}

	usedSlugs := make(map[string]int)
	result := make([]service.Library, 0, len(libraries))
	for idx, library := range libraries {
		absolutePath, err := AbsoluteCanonicalPath(library.Path)
		if err != nil {
			return nil, err
		}

		name := strings.TrimSpace(library.Name)
		if name == "" {
			name = filepath.Base(absolutePath)
		}
		if name == "" || name == "." || name == string(filepath.Separator) {
			name = fmt.Sprintf("Library %d", idx+1)
		}

		baseSlug := librarySlug(name)
		count := usedSlugs[baseSlug]
		usedSlugs[baseSlug] = count + 1
		slug := baseSlug
		if count > 0 {
			slug = fmt.Sprintf("%s-%d", baseSlug, count+1)
		}

		result = append(result, service.Library{
			Name: name,
			Slug: slug,
			Path: absolutePath,
		})
	}
	return result, nil
}

func librarySlug(name string) string {
	var b strings.Builder
	lastWasDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastWasDash = false
		case r == '-' || r == '_' || unicode.IsSpace(r):
			if !lastWasDash && b.Len() > 0 {
				b.WriteByte('-')
				lastWasDash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "library"
	}
	return slug
}

// NewHandler creates the HTTP handler for a configured OPDS service.
func NewHandler(cfg Config) (http.Handler, string, error) {
	libraries, err := canonicalLibraries(cfg.Libraries)
	if err != nil {
		return nil, "", err
	}

	absolutePath := ""
	if len(libraries) > 0 {
		absolutePath = libraries[0].Path
		for _, library := range libraries {
			slog.Info("trusted library", "name", library.Name, "slug", library.Slug, "path", library.Path)
		}
	} else {
		absolutePath, err = AbsoluteCanonicalPath(cfg.DirRoot)
		if err != nil {
			return nil, "", err
		}
		slog.Info("trusted root", "path", absolutePath)
	}

	s := service.OPDS{
		TrustedRoot:      absolutePath,
		ThumbDir:         filepath.Join(absolutePath, "thumb"),
		Libraries:        libraries,
		HideCalibreFiles: cfg.HideCalibreFiles,
		HideDotFiles:     cfg.HideDotFiles,
		NoCache:          cfg.NoCache,
		EnableCache:      cfg.EnableCache,
		SortBy:           cfg.SortBy,
		ShowCovers:       cfg.ShowCovers,
		MimeMap:          cfg.MimeMap,
		EnableSearch:     cfg.EnableSearch,
		ExtractMetadata:  cfg.ExtractMetadata,
		CoverWarmup:      cfg.CoverWarmup,
		EnableHTML:       cfg.EnableHTML,
		BaseURL:          cfg.BaseURL,
		PageSize:         cfg.PageSize,
		NoPagination:     cfg.NoPagination,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/favicon.ico", faviconHandler)
	mux.HandleFunc("/", errorHandler(s.Handler))
	mux.HandleFunc("/health", service.HealthHandler)
	if cfg.EnableSearch {
		mux.HandleFunc("/search", errorHandler(s.SearchHandler))
		mux.HandleFunc("/opensearch.xml", s.OpenSearchHandler)
	}
	if cfg.ExtractMetadata {
		mux.HandleFunc("/cover", errorHandler(s.CoverHandler))
	}

	var handler http.Handler = mux
	if cfg.EnableGzip {
		slog.Info("gzip compression enabled")
		handler = service.GzipMiddleware(handler)
	}

	s.WarmBookIndex()

	return handler, absolutePath, nil
}

func faviconHandler(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// NewHTTPServer creates an HTTP server and returns its URL.
func NewHTTPServer(cfg Config) (*http.Server, string, error) {
	handler, _, err := NewHandler(cfg)
	if err != nil {
		return nil, "", err
	}

	addr := cfg.Host + ":" + cfg.Port
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}

	urlHost := cfg.Host
	if urlHost == "" || urlHost == "0.0.0.0" || urlHost == "::" {
		urlHost = "127.0.0.1"
	}

	return srv, "http://" + urlHost + ":" + cfg.Port, nil
}

// Run starts the HTTP server and blocks until the context is canceled or the server exits.
func Run(ctx context.Context, cfg Config) error {
	srv, _, err := NewHTTPServer(cfg)
	if err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		if err := srv.Shutdown(context.Background()); err != nil {
			return err
		}
		return <-errCh
	case err := <-errCh:
		return err
	}
}

func errorHandler(f func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := f(w, r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			slog.Error("request error", "uri", r.RequestURI, "error", err)
		}
	}
}

// ConfigureLogger installs the default slog logger.
func ConfigureLogger(debug bool, logFormat string, baseURL string) {
	var level slog.Level
	if debug {
		level = slog.LevelDebug
	} else {
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	switch strings.ToLower(logFormat) {
	case "text":
		handler = slog.NewTextHandler(os.Stderr, opts)
	default:
		handler = slog.NewJSONHandler(os.Stderr, opts)
	}

	slog.SetDefault(slog.New(handler).With("base_url", baseURL))
}
