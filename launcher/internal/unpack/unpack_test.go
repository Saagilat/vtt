package unpack

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

type entry struct {
	name string
	body string
	mode os.FileMode
}

func writeZip(t *testing.T, path string, entries ...entry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestZipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "vtt-client-1.4.0-win64.zip")
	writeZip(t, archive,
		entry{name: "client", body: "бинарь", mode: 0o755},
		entry{name: "assets/map.json", body: `{"a":1}`},
	)

	dest := filepath.Join(dir, "client-1.4.0")
	if err := File(archive, dest); err != nil {
		t.Fatalf("File: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "client")); err != nil || string(got) != "бинарь" {
		t.Fatalf("точка входа: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(dest, "assets", "map.json")); err != nil || string(got) != `{"a":1}` {
		t.Fatalf("вложенный файл: %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(dest, "client"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("права %v: клиент остался без бита исполнения", info.Mode().Perm())
	}
}

func TestZipRejectsEscape(t *testing.T) {
	for _, name := range []string{"../evil.txt", "/etc/evil.txt", "assets/../../evil.txt"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "vtt-client-1.4.0-win64.zip")
			writeZip(t, archive, entry{name: name, body: "плохое"})

			dest := filepath.Join(dir, "client-1.4.0")
			if err := File(archive, dest); err == nil {
				t.Fatalf("архив с путём %q распакован", name)
			}
			if _, err := os.Stat(filepath.Join(dir, "evil.txt")); !os.IsNotExist(err) {
				t.Fatalf("файл появился за пределами каталога установки")
			}
		})
	}
}

func TestZipRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "vtt-client-1.4.0-win64.zip")
	writeZip(t, archive, entry{name: "ссылка", body: "/etc/passwd", mode: os.ModeSymlink | 0o777})

	if err := File(archive, filepath.Join(dir, "client-1.4.0")); err == nil {
		t.Fatalf("симлинк из архива распакован")
	}
}

func TestSingleExeCopy(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "vtt-annotator-0.2.0-win64.exe")
	if err := os.WriteFile(src, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "annotator-0.2.0")
	if err := File(src, dest); err != nil {
		t.Fatalf("File: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "vtt-annotator-0.2.0-win64.exe"))
	if err != nil || string(got) != "MZ" {
		t.Fatalf("копия одиночного файла: %q, %v", got, err)
	}
}
