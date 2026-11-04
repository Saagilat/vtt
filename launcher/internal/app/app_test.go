package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Saagilat/vtt/launcher/internal/fetch"
)

// Клиент в тестах — сам тестовый бинарь: так проверяется настоящий запуск процесса,
// без зависимости от sh, cmd.exe и прав на запись куда-либо ещё.
const helperEnv = "VTT_LAUNCHER_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(helperClient())
	}
	// Дети наследуют окружение: скопированный бинарь узнаёт себя по этой переменной.
	os.Setenv(helperEnv, "1")
	os.Exit(m.Run())
}

// helperClient ведёт себя как клиент: отвечает на --version, а на обычный запуск
// оставляет след с аргументами — по нему видно, что лаунчер передал каталог установки.
func helperClient() int {
	for _, arg := range os.Args[1:] {
		if arg == "--version" {
			// VTT_HELPER_VERSION позволяет изобразить клиент, который отвечает не тем,
			// что записано в state.json: так проверяется `--check`.
			version := os.Getenv("VTT_HELPER_VERSION")
			if version == "" {
				version = "1.4.0"
			}
			fmt.Println(version)
			return 0
		}
	}
	if marker := os.Getenv("VTT_LAUNCHER_MARKER"); marker != "" {
		os.WriteFile(marker, []byte(strings.Join(os.Args[1:], " ")), 0o644)
	}
	return 0
}

func clientEntryName() string {
	if runtime.GOOS == "windows" {
		return "client.exe"
	}
	return "client"
}

// pkgSpec — что положить в «релиз».
type pkgSpec struct {
	name      string
	version   string
	file      string
	payload   []byte
	minClient string
	// serve — что сервер отдаст вместо payload: так проверяется несовпавший хеш.
	serve []byte
}

type jsonPkg struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	File      string `json:"file"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MinClient string `json:"min_client,omitempty"`
}

type jsonManifest struct {
	Format   int       `json:"manifest"`
	Release  string    `json:"release"`
	Packages []jsonPkg `json:"packages"`
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func buildManifest(t *testing.T, format int, releaseVersion string, specs ...pkgSpec) ([]byte, map[string][]byte) {
	t.Helper()
	doc := jsonManifest{Format: format, Release: releaseVersion}
	files := map[string][]byte{}
	for _, s := range specs {
		served := s.payload
		if s.serve != nil {
			served = s.serve
		}
		files[s.file] = served
		doc.Packages = append(doc.Packages, jsonPkg{
			Name: s.name, Version: s.version, File: s.file,
			SHA256: sha256hex(s.payload), Size: int64(len(s.payload)), MinClient: s.minClient,
		})
	}
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return body, files
}

// releaseEnv — «релиз» для теста: адрес манифеста и загрузчик, который его отдаёт.
type releaseEnv struct {
	url string
	dl  fetch.Downloader
}

// offline — сети нет: настоящий клиент на закрытый порт получает отказ сразу.
func offline() releaseEnv {
	return releaseEnv{url: "http://127.0.0.1:1/latest.json"}
}

// publish раскладывает релиз в памяти и отдаёт его через http.RoundTripper.
//
// Сокет здесь не нужен: проверяются разбор манифеста, хеши и раскладка по каталогу,
// а настоящая сеть — в пакете fetch. Заодно тесты не зависят от того, как в среде
// с тестами ведёт себя петлевое соединение на мегабайтах.
func publish(t *testing.T, releaseVersion string, specs ...pkgSpec) releaseEnv {
	t.Helper()
	body, files := buildManifest(t, 1, releaseVersion, specs...)
	return publishRaw(t, body, files)
}

func publishRaw(t *testing.T, manifestBody []byte, files map[string][]byte) releaseEnv {
	t.Helper()
	mem := memoryRelease{manifest: manifestBody, files: files}
	return releaseEnv{
		url: "http://release.test/latest.json",
		dl: fetch.Downloader{
			Client:       &http.Client{Transport: mem, Timeout: 10 * time.Second},
			ManifestWait: 2 * time.Second,
		},
	}
}

// publishWithFormat публикует манифест с заданной версией формата: так проверяется правило 3
// (незнакомую версию не применяем, а продолжаем работать с установленным).
func publishWithFormat(t *testing.T, format int, releaseVersion string, specs ...pkgSpec) releaseEnv {
	t.Helper()
	body, files := buildManifest(t, format, releaseVersion, specs...)
	return publishRaw(t, body, files)
}

// memoryRelease — релиз в памяти: манифест и файлы по тем же именам.
type memoryRelease struct {
	manifest []byte
	files    map[string][]byte
}

func (m memoryRelease) RoundTrip(req *http.Request) (*http.Response, error) {
	name := filepath.Base(req.URL.Path)
	body, ok := m.files[name]
	if name == "latest.json" {
		body, ok = m.manifest, true
	}
	if !ok {
		return response(req, http.StatusNotFound, nil), nil
	}
	return response(req, http.StatusOK, body), nil
}

func response(req *http.Request, code int, body []byte) *http.Response {
	return &http.Response{
		Status:     fmt.Sprintf("%d %s", code, http.StatusText(code)),
		StatusCode: code,
		Header:     http.Header{"Content-Length": {strconv.Itoa(len(body))}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
}

// clientPayload собирает zip клиента: внутри — копия тестового бинаря.
func clientPayload(t *testing.T) []byte {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	return zipOf(t, map[string]zipItem{clientEntryName(): {body: bin, mode: 0o755}})
}

func baseConfig(t *testing.T, dir string, rel releaseEnv) (Config, *bytes.Buffer) {
	t.Helper()
	var out bytes.Buffer
	t.Setenv("VTT_LAUNCHER_MARKER", filepath.Join(t.TempDir(), "marker"))
	return Config{
		InstallDir:  dir,
		ManifestURL: rel.url,
		Downloader:  rel.dl,
		Out:         &out,
		In:          strings.NewReader(""),
	}, &out
}

func markerPath(t *testing.T) string {
	t.Helper()
	return os.Getenv("VTT_LAUNCHER_MARKER")
}
