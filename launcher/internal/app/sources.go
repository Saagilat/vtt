package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Saagilat/vtt/launcher/internal/fetch"
	"github.com/Saagilat/vtt/launcher/internal/manifest"
	"github.com/Saagilat/vtt/launcher/internal/unpack"
)

// source — откуда взять файл пакета. Дальше источника разницы между сетью и флешкой нет:
// проверка размера и хеша одна и та же (MANIFEST.md, «Установка из файла»).
type source interface {
	file(ctx context.Context, p manifest.Package) (string, error)
}

// netSource берёт файлы из того же релиза, откуда пришёл манифест: адрес собирается
// от адреса манифеста, а не из его полей.
type netSource struct {
	dl   fetch.Downloader
	base string
	dir  string
}

func (s netSource) file(ctx context.Context, p manifest.Package) (string, error) {
	path := filepath.Join(s.dir, p.File)
	if err := s.dl.Package(ctx, s.base+p.File, path, p.Size, p.SHA256); err != nil {
		return "", err
	}
	return path, nil
}

// dirSource берёт файлы из каталога рядом с latest.json: флешка или распакованный архив.
type dirSource struct {
	dir string
}

func (s dirSource) file(_ context.Context, p manifest.Package) (string, error) {
	path := filepath.Join(s.dir, p.File)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("%s: %w", p.File, err)
	}
	if err := fetch.Verify(path, p.Size, p.SHA256); err != nil {
		return "", fmt.Errorf("%s: %w", p.File, err)
	}
	return path, nil
}

// prepareSource возвращает каталог с latest.json: `--install` принимает и каталог, и архив.
// Архив распаковывается во временный каталог root, а не «ставится» напрямую.
func (r *runner) prepareSource(root string) (string, error) {
	src := filepath.Clean(r.cfg.Source)
	info, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return src, nil
	}
	dir := filepath.Join(root, "source")
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := unpack.File(src, dir); err != nil {
		return "", err
	}
	return dir, nil
}

// readManifestDir читает latest.json рядом с файлами поставки.
func readManifestDir(dir string) (*manifest.Manifest, error) {
	path := filepath.Join(dir, "latest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("нет latest.json в %s: %w", dir, err)
	}
	return manifest.Parse(data)
}

// manifestFromNet — один GET на старте (D38).
func (r *runner) manifestFromNet(ctx context.Context) (*manifest.Manifest, error) {
	data, err := r.dl.Manifest(ctx, r.cfg.ManifestURL)
	if err != nil {
		return nil, err
	}
	return manifest.Parse(data)
}
