package api

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type spaHandler struct {
	root  string
	index string
}

func newSPAHandler(root string) http.Handler {
	return spaHandler{root: root, index: filepath.Join(root, "index.html")}
}

func (handler spaHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clean := filepath.Clean(filepath.FromSlash("/" + strings.TrimPrefix(request.URL.Path, "/")))
	candidate := filepath.Join(handler.root, clean)
	if relative, err := filepath.Rel(handler.root, candidate); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			http.ServeFile(writer, request, candidate)
			return
		}
	}
	http.ServeFile(writer, request, handler.index)
}
