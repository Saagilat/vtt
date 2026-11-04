// Package unpack раскладывает скачанный файл в каталог версии: zip — распаковкой,
// одиночный .exe — копией. Оба пути одинаково не доверяют содержимому архива:
// запись за пределы каталога установки — ошибка, а не «ладно, распакуем как просят».
package unpack

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// File раскладывает archive в dest. dest создаётся.
func File(archive, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Ext(archive), ".zip") {
		return CopyFile(archive, filepath.Join(dest, filepath.Base(archive)))
	}
	return zipFile(archive, dest)
}

// CopyFile копирует один файл, сохраняя права: бинарь клиента должен остаться исполняемым.
func CopyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func zipFile(archive, dest string) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(archive), err)
	}
	defer r.Close()

	root, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	for _, f := range r.File {
		target, err := inside(root, f.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(archive), err)
		}
		mode := f.Mode()
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !mode.IsRegular() {
			// Симлинки и устройства из чужого архива не раскладываем: распаковка не должна
			// уметь поставить ссылку в обход каталога установки.
			return fmt.Errorf("%s: %q — не обычный файл", filepath.Base(archive), f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipEntry(f, target); err != nil {
			return fmt.Errorf("%s: %q: %w", filepath.Base(archive), f.Name, err)
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	perm := f.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, rc); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// inside возвращает путь внутри root или ошибку, если имя уводит наружу.
func inside(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q: путь за пределы каталога установки", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q: путь за пределы каталога установки", name)
	}
	return target, nil
}
