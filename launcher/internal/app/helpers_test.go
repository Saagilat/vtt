package app

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

type zipItem struct {
	body []byte
	mode os.FileMode
}

// zipOf собирает zip в памяти: файлы релиза в тестах не должны лежать на диске.
func zipOf(t *testing.T, items map[string]zipItem) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, item := range items {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		mode := item.mode
		if mode == 0 {
			mode = 0o644
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(item.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// writeReleaseDir раскладывает «релиз» в каталог — то, что человек приносит на флешке.
func writeReleaseDir(t *testing.T, dir, releaseVersion string, specs ...pkgSpec) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, _ := buildManifest(t, 1, releaseVersion, specs...)
	for _, s := range specs {
		if err := os.WriteFile(filepath.Join(dir, s.file), s.payload, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "latest.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
