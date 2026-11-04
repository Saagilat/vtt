package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBaseAndPackageURL(t *testing.T) {
	const manifestAdr = "https://example.test/releases/latest/download/latest.json"
	if got := PackageURL(manifestAdr, "vtt-client-1.4.0-win64.zip"); got !=
		"https://example.test/releases/latest/download/vtt-client-1.4.0-win64.zip" {
		t.Fatalf("PackageURL = %q: файл должен браться из того же каталога, что манифест", got)
	}
	if got := BaseURL("latest.json"); got != "" {
		t.Fatalf("BaseURL(без слэшей) = %q", got)
	}
}

func TestManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/latest.json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"manifest":1}`))
	}))
	defer srv.Close()

	dl := New()
	data, err := dl.Manifest(context.Background(), srv.URL+"/latest.json")
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if string(data) != `{"manifest":1}` {
		t.Fatalf("Manifest вернул %q", data)
	}
	if _, err := dl.Manifest(context.Background(), srv.URL+"/нет.json"); err == nil {
		t.Fatalf("404 должен быть ошибкой")
	}
}

func TestPackageVerifiesAndKeepsNothingExtra(t *testing.T) {
	payload := []byte("архив клиента")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "vtt-client-1.4.0-win64.zip")
	dl := New()
	if err := dl.Package(context.Background(), srv.URL+"/client.zip", path, int64(len(payload)), hash(payload)); err != nil {
		t.Fatalf("Package: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("прочитать скачанное: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("скачали %q", got)
	}
	if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
		t.Fatalf("после успеха остался .part")
	}
}

func TestPackageResumesAfterInterrupt(t *testing.T) {
	payload := []byte("0123456789abcdef")
	askedRange := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		askedRange = r.Header.Get("Range")
		if askedRange != "" {
			w.Header().Set("Content-Range", "bytes 4-15/16")
			w.WriteHeader(http.StatusPartialContent)
			w.Write(payload[4:])
			return
		}
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "vtt-client-1.4.0-win64.zip")
	// Прошлый запуск оборвался на четвёртом байте.
	if err := os.WriteFile(path+".part", payload[:4], 0o644); err != nil {
		t.Fatal(err)
	}
	dl := New()
	if err := dl.Package(context.Background(), srv.URL+"/client.zip", path, int64(len(payload)), hash(payload)); err != nil {
		t.Fatalf("Package с докачкой: %v", err)
	}
	if askedRange != "bytes=4-" {
		t.Fatalf("Range = %q, ожидали bytes=4- (докачка, а не заново)", askedRange)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("после докачки получили %q", got)
	}
}

func TestPackageRejectsBadHashAndSize(t *testing.T) {
	payload := []byte("битый архив")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dl := New()
	path := filepath.Join(dir, "client.zip")

	if err := dl.Package(context.Background(), srv.URL+"/client.zip", path,
		int64(len(payload)), hash([]byte("другой архив"))); err == nil {
		t.Fatalf("несовпавший хеш принят")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("файл с неверным хешем всё равно оказался на месте")
	}

	if err := dl.Package(context.Background(), srv.URL+"/client.zip", path,
		int64(len(payload))+10, hash(payload)); err == nil {
		t.Fatalf("несовпавший размер принят")
	}
}

func TestVerifyReportsSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.zip")
	payload := []byte("12345")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path, 4, hash(payload)); err == nil {
		t.Fatalf("размер 5 при ожидаемых 4 — должен быть ошибкой")
	}
	if err := Verify(path, 5, hash(payload)); err != nil {
		t.Fatalf("Verify на верном файле: %v", err)
	}
}

func hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
