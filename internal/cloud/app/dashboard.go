package app

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

func isCustomerPage(requestPath string) bool {
	return requestPath == "/app" || strings.HasPrefix(requestPath, "/app/")
}

func isControlPage(requestPath string) bool {
	return requestPath == "/control" || strings.HasPrefix(requestPath, "/control/")
}

func isDashboardPage(requestPath string) bool {
	return requestPath == "/login" || isCustomerPage(requestPath) || isControlPage(requestPath) ||
		strings.HasPrefix(requestPath, "/assets/")
}

type dashboardHandler struct {
	fileServer  http.Handler
	index       []byte
	indexErr    error
	staticFiles map[string]struct{}
	origin      string
}

func newDashboardHandler(files fs.FS, origin string) *dashboardHandler {
	staticFiles := make(map[string]struct{})
	_ = fs.WalkDir(files, ".", func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && path != "index.html" {
			staticFiles["/"+path] = struct{}{}
		}
		return nil
	})
	index, indexErr := fs.ReadFile(files, "index.html")
	index = newAssetVersions(files).rewrite(index)
	return &dashboardHandler{
		fileServer:  http.FileServer(http.FS(files)),
		index:       index,
		indexErr:    indexErr,
		staticFiles: staticFiles,
		origin:      strings.TrimRight(origin, "/"),
	}
}

func (h *dashboardHandler) handles(path string) bool {
	if isDashboardPage(path) {
		return true
	}
	_, ok := h.staticFiles[path]
	return ok
}

func (h *dashboardHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErrorPage(w, r, http.StatusMethodNotAllowed, h.origin, true)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/assets/") {
		if _, ok := h.staticFiles[r.URL.Path]; !ok {
			writeErrorPage(w, r, http.StatusNotFound, h.origin, true)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.fileServer.ServeHTTP(w, r)
		return
	}
	if _, ok := h.staticFiles[r.URL.Path]; ok {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		setStaticContentType(w, r.URL.Path)
		h.fileServer.ServeHTTP(w, r)
		return
	}
	if !isDashboardPage(r.URL.Path) {
		writeErrorPage(w, r, http.StatusNotFound, h.origin, true)
		return
	}

	h.serveSPAEntry(w, r)
}

func (h *dashboardHandler) serveSPAEntry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	if h.indexErr != nil {
		writeErrorPage(w, r, http.StatusInternalServerError, h.origin, true)
		return
	}
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(h.index))
}

func setStaticContentType(w http.ResponseWriter, requestPath string) {
	switch strings.ToLower(path.Ext(requestPath)) {
	case ".ico":
		w.Header().Set("Content-Type", "image/vnd.microsoft.icon")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".webmanifest":
		w.Header().Set("Content-Type", "application/manifest+json")
	}
}
