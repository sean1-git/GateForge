package app

import (
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// StaticUI serves precompressed public assets without compressing secrets.
func StaticUI(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve only the public build entry and known asset types. Never expose
		// directory listings, source maps, dotfiles, or files copied here by mistake.
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		asset := strings.HasPrefix(r.URL.Path, "/assets/") && !strings.Contains(name, "/") && !strings.HasPrefix(name, ".")
		switch path.Ext(name) {
		case ".js", ".css", ".png", ".svg", ".jpg", ".jpeg", ".webp", ".woff2", ".ico":
		default:
			asset = false
		}
		if r.URL.Path != "/" && r.URL.Path != "/index.html" && !asset {
			http.NotFound(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		ext := path.Ext(r.URL.Path)
		if (r.Method == "GET" || r.Method == "HEAD") && strings.HasPrefix(r.URL.Path, "/assets/") && (ext == ".js" || ext == ".css") && acceptsGzip(r.Header.Get("Accept-Encoding")) {
			name := path.Clean("/"+r.URL.Path) + ".gz"
			if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(name[1:]))); err == nil && !info.IsDir() {
				clone := r.Clone(r.Context())
				clone.URL.Path = name
				clone.URL.RawPath = ""
				w.Header().Set("Content-Encoding", "gzip")
				w.Header().Set("Content-Type", mime.TypeByExtension(ext))
				files.ServeHTTP(w, clone)
				return
			}
		}
		files.ServeHTTP(w, r)
	})
}
func acceptsGzip(value string) bool {
	for _, encoding := range strings.Split(value, ",") {
		parts := strings.Split(strings.TrimSpace(encoding), ";")
		if !strings.EqualFold(parts[0], "gzip") {
			continue
		}
		quality := 1.0
		for _, param := range parts[1:] {
			pair := strings.SplitN(strings.TrimSpace(param), "=", 2)
			if len(pair) == 2 && strings.EqualFold(pair[0], "q") {
				n, err := strconv.ParseFloat(pair[1], 64)
				if err != nil || !(n >= 0 && n <= 1) {
					return false
				}
				quality = n
			}
		}
		return quality > 0
	}
	return false
}
