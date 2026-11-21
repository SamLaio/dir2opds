package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SamLaio/dir2opds/internal/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPTimeouts(t *testing.T) {
	cfg := server.DefaultConfig()
	cfg.DirRoot = t.TempDir()
	srv, _, err := server.NewHTTPServer(cfg)
	require.NoError(t, err)
	assert.Equal(t, 10*time.Second, srv.ReadHeaderTimeout)
	assert.Equal(t, 30*time.Second, srv.ReadTimeout)
	assert.Equal(t, 60*time.Second, srv.IdleTimeout)
	assert.Zero(t, srv.WriteTimeout)
	waitForIndex(t, cfg.DirRoot, false)
}

func TestHiddenDownloads(t *testing.T) {
	for _, multi := range []bool{false, true} {
		name := "single"
		if multi {
			name = "multiple"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range []string{".secret", ".hidden/book.epub", "metadata.db", "thumb/cache.json", ".thumb/cache.json", "public.txt"} {
				target := filepath.Join(root, filepath.FromSlash(name))
				require.NoError(t, os.MkdirAll(filepath.Dir(target), 0700))
				require.NoError(t, os.WriteFile(target, []byte("test-content"), 0600))
			}
			cfg := server.DefaultConfig()
			cfg.DirRoot = root
			cfg.HideDotFiles = true
			cfg.HideCalibreFiles = true
			cfg.ExtractMetadata = true
			prefix := ""
			if multi {
				cfg.Libraries = []server.LibraryConfig{{Name: "Books", Path: root}}
				prefix = "/books"
			}
			h, _, err := server.NewHandler(cfg)
			require.NoError(t, err)
			waitForIndex(t, root, multi)
			for _, path := range []string{"/.secret", "/%2esecret", "/.hidden/book.epub", "/metadata.db", "/thumb/cache.json", "/.thumb/cache.json"} {
				t.Run(path, func(t *testing.T) {
					for _, requestPath := range []string{prefix + path, "/cover?file=" + url.QueryEscape(prefix+path)} {
						w := httptest.NewRecorder()
						h.ServeHTTP(w, httptest.NewRequest("GET", requestPath, nil))
						assert.Equal(t, http.StatusNotFound, w.Code, requestPath)
						assert.NotContains(t, w.Body.String(), "test-content")
					}
				})
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", prefix+"/public.txt", nil))
			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "test-content", w.Body.String())
			metaPath := indexMetaPath(root, multi)
			require.NoError(t, os.Remove(metaPath))
			cfg.HideDotFiles = false
			cfg.HideCalibreFiles = false
			h, _, err = server.NewHandler(cfg)
			require.NoError(t, err)
			w = httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", prefix+"/.secret", nil))
			assert.Equal(t, http.StatusOK, w.Code)
			waitForIndex(t, root, multi)
		})
	}
}

func indexMetaPath(root string, multi bool) string {
	if multi {
		return filepath.Join(root, "thumb", "libraries", "books", "book-index.meta.json")
	}
	return filepath.Join(root, "thumb", "book-index.meta.json")
}

func waitForIndex(t *testing.T, root string, multi bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, err := os.Stat(indexMetaPath(root, multi))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
}
