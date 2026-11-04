// Package fetch — сеть лаунчера: один GET манифеста и скачивание файлов из того же релиза.
//
// Адрес файла не берётся из манифеста: манифест содержит имя, а URL собирается от адреса
// самого манифеста. Подменённый манифест не уводит лаунчер на чужой хостинг (MANIFEST.md).
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ManifestAdr — единственный адрес, с которого лаунчер берёт манифест (D38).
// Он же адрес по умолчанию; --manifest подменяет его для проверки и установки из файла.
const ManifestAdr = "https://github.com/Saagilat/vtt/releases/latest/download/latest.json"

// Downloader — клиент сети. Таймауты здесь, а не в вызывающем коде: запуск игры не должен
// ждать недоступный хостинг дольше, чем человек готов стоять перед экраном (D38).
type Downloader struct {
	Client       *http.Client
	ManifestWait time.Duration
}

// New возвращает загрузчик с разумными сроками: манифест — коротко, файл — сколько нужно.
func New() Downloader {
	return Downloader{
		Client:       &http.Client{Timeout: 30 * time.Minute},
		ManifestWait: 10 * time.Second,
	}
}

// Manifest скачивает latest.json и возвращает его байты.
func (d Downloader) Manifest(ctx context.Context, url string) ([]byte, error) {
	wait := d.ManifestWait
	if wait <= 0 {
		wait = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("манифест %s: %w", url, err)
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("манифест %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("манифест %s: ответ %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("манифест %s: %w", url, err)
	}
	return data, nil
}

// Package скачивает файл в path, проверяя размер и sha256 до того, как файл будет распакован.
// Недокачанный файл остаётся как path+".part" и докачивается со следующего раза.
func (d Downloader) Package(ctx context.Context, url, path string, size int64, wantSHA string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	part := path + ".part"
	have := int64(0)
	if info, err := os.Stat(part); err == nil {
		have = info.Size()
	}
	if have > size {
		have = 0
		if err := os.Remove(part); err != nil {
			return err
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("скачать %s: %w", url, err)
	}
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return fmt.Errorf("скачать %s: %w", url, err)
	}
	defer resp.Body.Close()

	flags := os.O_CREATE | os.O_WRONLY
	switch {
	case have > 0 && resp.StatusCode == http.StatusPartialContent:
		flags |= os.O_APPEND
	case resp.StatusCode == http.StatusOK:
		flags |= os.O_TRUNC
		have = 0
	default:
		return fmt.Errorf("скачать %s: ответ %s", url, resp.Status)
	}

	f, err := os.OpenFile(part, flags, 0o644)
	if err != nil {
		return fmt.Errorf("скачать %s: %w", url, err)
	}
	// Не больше, чем обещал манифест: лишние байты — тоже порча, а не «просто больше».
	body := io.LimitReader(resp.Body, size-have+1)
	if _, err := io.Copy(f, body); err != nil {
		f.Close()
		return fmt.Errorf("скачать %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("скачать %s: %w", url, err)
	}

	if err := Verify(part, size, wantSHA); err != nil {
		os.Remove(part)
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(part, path); err != nil {
		return fmt.Errorf("скачать %s: %w", url, err)
	}
	return nil
}

// Verify сверяет размер и хеш файла с манифестом. Хеш — про порчу и обрыв, а не про доверие:
// подмену на стороне аккаунта GitHub он не поймает (MANIFEST.md, правило 5).
func Verify(path string, size int64, wantSHA string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() != size {
		return fmt.Errorf("размер %d байт, а в манифесте %d", info.Size(), size)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("sha256 %s, а в манифесте %s", got, strings.ToLower(wantSHA))
	}
	return nil
}

// BaseURL возвращает каталог, в котором лежит манифест: файлы берутся из него же.
func BaseURL(manifestURL string) string {
	i := strings.LastIndexByte(manifestURL, '/')
	if i < 0 {
		return ""
	}
	return manifestURL[:i+1]
}

// PackageURL собирает адрес файла от адреса манифеста: файлы лежат в том же релизе.
func PackageURL(manifestURL, file string) string {
	return BaseURL(manifestURL) + file
}

func (d Downloader) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: 30 * time.Minute}
}
