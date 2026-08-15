package server

import (
	"archive/zip"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// SourceModuleRoot returns the directory containing go.mod, walking up from the
// builder's clientPkg. Returns "" if not found.
func (bm *BuilderManager) SourceModuleRoot() string {
	bm.mu.RLock()
	clientPkg := bm.clientPkg
	bm.mu.RUnlock()
	dir := filepath.Clean(clientPkg)
	if !filepath.IsAbs(dir) {
		wd, err := os.Getwd()
		if err != nil {
			return ""
		}
		dir = filepath.Join(wd, dir)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// zipDir writes a recursive zip archive of dir to w.
func zipDir(dir string, w io.Writer) error {
	zw := zip.NewWriter(w)
	defer zw.Close()
	base := filepath.Clean(dir)
	return filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "dist" || info.Name() == "build" || info.Name() == "node_modules" || info.Name() == "vendor" || info.Name() == "runtime" || info.Name() == "logs") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			header.Name += "/"
			_, err := zw.CreateHeader(header)
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// Skip binary artifacts (>5MB) — the weave only needs Go source + assets.
		if info.Size() > 5*1024*1024 {
			return nil
		}
		header.Method = zip.Deflate
		fw, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(fw, f)
		f.Close()
		return copyErr
	})
}

// handleV1ReplicateSource zips the probe source module and streams it, so the
// operator can export the source and hand it to MANTLE (source_zip).
func (s *Server) handleV1ReplicateSource(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	if s.builder == nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "builder not configured")
		return
	}
	root := s.builder.SourceModuleRoot()
	if root == "" {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "source module root not found")
		return
	}
	tmp, err := os.CreateTemp("", "probe-src-*.zip")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := zipDir(root, tmp); err != nil {
		tmp.Close()
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
		return
	}
	tmp.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=probe-source.zip")
	http.ServeFile(w, r, tmpName)
}
