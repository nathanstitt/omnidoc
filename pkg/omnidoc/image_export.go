package omnidoc

import (
	"crypto/sha256"
	"encoding/hex"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nathanstitt/omnidoc/pkg/resource"
)

// imageExporter moves embedded data: images out of a document's markup into
// files, for the Markdown and HTML writers' ImageDir option. A file is named by
// the hash of its bytes, so the same picture shares one file and the output is
// deterministic. A failure to write degrades to the inline data: URI, logged.
type imageExporter struct {
	dir, ref string
	logf     func(string, ...any)

	mu   sync.Mutex
	made bool
	done map[string]string // data: URI -> reference already written
}

// newImageExporter returns an exporter for dir, or nil when dir is empty.
func newImageExporter(dir, ref string, logf func(string, ...any)) *imageExporter {
	if dir == "" {
		return nil
	}
	if ref == "" {
		ref = filepath.Base(dir)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &imageExporter{dir: dir, ref: strings.TrimRight(ref, "/"), logf: logf, done: map[string]string{}}
}

// src rewrites one image src. A nil exporter returns src unchanged.
func (e *imageExporter) src(src string) string {
	if e == nil || !strings.HasPrefix(src, "data:") {
		return src
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if ref, ok := e.done[src]; ok {
		return ref
	}
	data, ctype, err := resource.LoadDataURL(src)
	if err != nil {
		e.logf("omnidoc: image export: %v; keeping the inline image", err)
		return src
	}
	if !e.made {
		if err := os.MkdirAll(e.dir, 0o755); err != nil {
			e.logf("omnidoc: image export: %v; keeping the inline image", err)
			return src
		}
		e.made = true
	}
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:6]) + imageExt(ctype)
	if err := os.WriteFile(filepath.Join(e.dir, name), data, 0o644); err != nil {
		e.logf("omnidoc: image export: %v; keeping the inline image", err)
		return src
	}
	ref := path.Join(e.ref, name)
	e.done[src] = ref
	return ref
}

// imageExt maps an image content type to a file extension, ".img" when unknown.
func imageExt(ctype string) string {
	mt, _, _ := mime.ParseMediaType(ctype)
	switch mt {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	}
	return ".img"
}
