/*
  Copyright (C) 2017 Sinuhé Téllez Rivera

  dir2opds is free software: you can redistribute it and/or modify
  it under the terms of the GNU General Public License as published by
  the Free Software Foundation, either version 3 of the License, or
  (at your option) any later version.

  dir2opds is distributed in the hope that it will be useful,
  but WITHOUT ANY WARRANTY; without even the implied warranty of
  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
  GNU General Public License for more details.

  You should have received a copy of the GNU General Public License
  along with dir2opds.  If not, see <http://www.gnu.org/licenses/>.
*/

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/SamLaio/dir2opds/internal/server"
)

type libraryFlag []server.LibraryConfig

func (f *libraryFlag) String() string {
	if f == nil {
		return ""
	}
	parts := make([]string, 0, len(*f))
	for _, library := range *f {
		if library.Name == "" {
			parts = append(parts, library.Path)
			continue
		}
		parts = append(parts, library.Name+"="+library.Path)
	}
	return strings.Join(parts, ",")
}

func (f *libraryFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("library cannot be empty")
	}

	library := server.LibraryConfig{Path: value}
	if name, libraryPath, ok := strings.Cut(value, "="); ok {
		name = strings.TrimSpace(name)
		libraryPath = strings.TrimSpace(libraryPath)
		if name == "" || libraryPath == "" {
			return fmt.Errorf("library must be path or name=path")
		}
		library.Name = name
		library.Path = libraryPath
	}
	*f = append(*f, library)
	return nil
}

var (
	port         = flag.String("port", "8080", "The server will listen in this port.")
	host         = flag.String("host", "0.0.0.0", "The server will listen in this host.")
	dirRoot      = flag.String("dir", "./books", "A directory with books.")
	libraries    libraryFlag
	debug        = flag.Bool("debug", false, "If it is set it will log the requests.")
	calibre      = flag.Bool("calibre", false, "Hide files stored by calibre.")
	hideDotFiles = flag.Bool("hide-dot-files", false, "Hide files that starts with dot.")
	noCache      = flag.Bool("no-cache", false, "adds reponse headers to avoid client from caching.")
	enableCache  = flag.Bool("enable-cache", false, "Enable ETag and Last-Modified headers for conditional requests.")
	gzip         = flag.Bool("gzip", false, "Enable gzip compression for responses.")
	sortBy       = flag.String("sort", "date", "Sort entries by: name, date, type, size.")
	showCovers   = flag.Bool("show-covers", false, "Show cover.jpg or folder.jpg as catalog cover.")
	mimeMapStr   = flag.String("mime-map", "", "Custom mime types (e.g., '.mobi:application/x-mobipocket-ebook,.azw3:application/vnd.amazon.ebook')")
	searchEnable = flag.Bool("search", false, "Enable basic filename search.")
	extractMeta  = flag.Bool("extract-metadata", false, "Extract title/author metadata and covers where supported.")
	coverWarmup  = flag.Bool("cover-warmup", false, "Pre-extract covers in the background when metadata extraction is enabled.")
	enableHTML   = flag.Bool("enable-html", false, "Enable web-friendly HTML view for browsers.")
	baseURL      = flag.String("url", "", "The base URL used for absolute links in the feed (e.g., https://opds.example.com).")
	logFormat    = flag.String("log-format", "json", "Log format: json, text.")
	pageSize     = flag.Int("page-size", 50, "Number of entries per page (0 for default, max 50).")
	noPagination = flag.Bool("no-pagination", false, "Disable pagination and show all entries in a single feed.")
)

func main() {

	flag.Var(&libraries, "library", "A book library. Repeat for multiple libraries. Use path or name=path.")
	flag.Parse()

	server.ConfigureLogger(*debug, *logFormat, *baseURL)

	// Use the absolute canonical path of the dir parm as the trustedRoot.
	// Helps avoid http path traversal. https://github.com/SamLaio/dir2opds/issues/17
	absolutePath := *dirRoot
	if len(libraries) == 0 {
		var err error
		absolutePath, err = absoluteCanonicalPath(*dirRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s\n", err)
			os.Exit(1)
		}

		slog.Info("trusted root", "path", absolutePath)
	}

	fmt.Println(startValues())

	cfg := server.Config{
		Host:             *host,
		Port:             *port,
		DirRoot:          absolutePath,
		Libraries:        []server.LibraryConfig(libraries),
		HideCalibreFiles: *calibre,
		HideDotFiles:     *hideDotFiles,
		NoCache:          *noCache,
		EnableCache:      *enableCache,
		EnableGzip:       *gzip,
		SortBy:           *sortBy,
		ShowCovers:       *showCovers,
		MimeMap:          parseMimeMap(*mimeMapStr),
		EnableSearch:     *searchEnable,
		ExtractMetadata:  *extractMeta,
		CoverWarmup:      *coverWarmup,
		EnableHTML:       *enableHTML,
		BaseURL:          *baseURL,
		PageSize:         *pageSize,
		NoPagination:     *noPagination,
	}

	srv, _, err := server.NewHTTPServer(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", err)
		os.Exit(1)
	}

	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "server failed: %s\n", err)
		os.Exit(1)
	}
}

func parseMimeMap(s string) map[string]string {
	return server.ParseMimeMap(s)
}

func startValues() string {
	result := fmt.Sprintf("listening in: %s:%s", *host, *port)
	return result
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

// absoluteCanonicalPath returns the canonical path of the absolute path that was passed
func absoluteCanonicalPath(aPath string) (string, error) {
	return server.AbsoluteCanonicalPath(aPath)
}
