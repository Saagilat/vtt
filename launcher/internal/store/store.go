// Package store — каталог установки: какие версии пакетов стоят и какая включена.
//
// Правило раскладки одно: старая версия не перезаписывается на месте. Новая распаковывается
// рядом, проверяется, и включается одним переключением — перезаписью state.json. Обрыв
// на середине оставляет человека с прежней версией, а не без клиента (launcher/README.md).
//
//	<каталог>/
//	  state.json                    что включено сейчас
//	  versions/client-1.4.0/...     включённая версия
//	  versions/client-1.3.0/...     предыдущая, рядом на случай отката
//	  tmp/                          место для скачивания и распаковки
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// stateFormat — версия самого файла состояния. Меняется вместе с раскладкой каталога,
// чтобы старый лаунчер не читал чужой state.json как свой.
const stateFormat = 1

// State — содержимое state.json.
type State struct {
	Format   int                  `json:"state"`
	Packages map[string]Installed `json:"packages"`
}

// Installed — одна установленная запись.
type Installed struct {
	Version string `json:"version"`
}

// Store — каталог установки.
type Store struct {
	dir string
}

// Open открывает каталог установки, создавая его при необходимости.
func Open(dir string) (*Store, error) {
	abs, err := absDir(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "versions"), 0o755); err != nil {
		return nil, fmt.Errorf("каталог установки %q: %w", abs, err)
	}
	return &Store{dir: abs}, nil
}

// OpenRead открывает каталог установки, ничего не создавая: `--check` обещает не менять
// ничего, и появившийся после проверки пустой каталог был бы уже изменением.
func OpenRead(dir string) (*Store, error) {
	abs, err := absDir(dir)
	if err != nil {
		return nil, err
	}
	return &Store{dir: abs}, nil
}

func absDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("каталог установки %q: %w", dir, err)
	}
	return abs, nil
}

// Dir — каталог установки целиком (его лаунчер передаёт клиенту).
func (s *Store) Dir() string { return s.dir }

// StatePath — файл состояния; печатается в `--check`, чтобы человек мог его найти.
func (s *Store) StatePath() string { return filepath.Join(s.dir, "state.json") }

// VersionsDir — где лежат распакованные версии.
func (s *Store) VersionsDir() string { return filepath.Join(s.dir, "versions") }

// TmpDir — место под скачивание и распаковку.
func (s *Store) TmpDir() string { return filepath.Join(s.dir, "tmp") }

// VersionDir — каталог конкретной версии пакета.
func (s *Store) VersionDir(name, version string) string {
	return filepath.Join(s.dir, "versions", name+"-"+version)
}

// StagedDir — куда распаковывается версия до переименования в VersionDir: распаковка,
// которая упала на середине, не должна оставить полураспакованную версию в versions/.
func (s *Store) StagedDir(name, version string) string {
	return filepath.Join(s.TmpDir(), name+"-"+version)
}

// Load читает state.json. Отсутствие файла — пустое состояние: первый запуск на чистой машине.
func (s *Store) Load() (State, error) {
	state := State{Format: stateFormat, Packages: map[string]Installed{}}
	data, err := os.ReadFile(s.StatePath())
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("прочитать %s: %w", s.StatePath(), err)
	}
	if len(data) == 0 {
		return state, fmt.Errorf("%s: файл пуст", s.StatePath())
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("%s: %w", s.StatePath(), err)
	}
	if state.Format != stateFormat {
		return state, fmt.Errorf("%s: формат состояния %d, понимаем только %d",
			s.StatePath(), state.Format, stateFormat)
	}
	if state.Packages == nil {
		state.Packages = map[string]Installed{}
	}
	return state, nil
}

// Installed возвращает включённую версию пакета или пустую строку.
func (s *Store) Installed(name string) string {
	state, err := s.Load()
	if err != nil {
		return ""
	}
	return state.Packages[name].Version
}

// Activate включает версию пакета и убирает лишние: включённая и предыдущая остаются рядом,
// остальные удаляются. Переключение — одна перезапись state.json, а не копирование файлов.
func (s *Store) Activate(name, version string) error {
	if err := checkName(name, version); err != nil {
		return err
	}
	if _, err := os.Stat(s.VersionDir(name, version)); err != nil {
		return fmt.Errorf("включить %s %s: %w", name, version, err)
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	previous := state.Packages[name].Version
	state.Packages[name] = Installed{Version: version}
	if err := s.save(state); err != nil {
		return err
	}
	keep := map[string]bool{version: true}
	if previous != "" && previous != version {
		keep[previous] = true
	}
	return s.prune(name, keep)
}

// Commit переименовывает распакованную версию из tmp в versions/ и включает её.
func (s *Store) Commit(name, version string) error {
	if err := checkName(name, version); err != nil {
		return err
	}
	staged := s.StagedDir(name, version)
	if _, err := os.Stat(staged); err != nil {
		return fmt.Errorf("установить %s %s: распакованного каталога нет: %w", name, version, err)
	}
	final := s.VersionDir(name, version)
	if err := os.RemoveAll(final); err != nil {
		return fmt.Errorf("установить %s %s: убрать прежнюю копию: %w", name, version, err)
	}
	if err := os.Rename(staged, final); err != nil {
		return fmt.Errorf("установить %s %s: %w", name, version, err)
	}
	return s.Activate(name, version)
}

// EntryPoint находит программу пакета внутри его версии: <name>.exe или <name>.
// Лаунчер не угадывает, что запускать: имя точки входа — часть раскладки.
func (s *Store) EntryPoint(name, version string) (string, error) {
	return EntryPointIn(s.VersionDir(name, version), name)
}

// EntryPointIn — то же для любого каталога: так точку входа проверяют до включения версии,
// пока распакованное ещё лежит в tmp.
func EntryPointIn(dir, name string) (string, error) {
	for _, candidate := range []string{name + ".exe", name} {
		path := filepath.Join(dir, candidate)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s: в %s нет ни %s, ни %s.exe", name, dir, name, name)
}

// CleanTmp убирает остатки скачивания и распаковки.
func (s *Store) CleanTmp() error {
	return os.RemoveAll(s.TmpDir())
}

func (s *Store) save(state State) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(s.StatePath(), data, 0o644)
}

// prune удаляет версии пакета, которых нет в keep.
func (s *Store) prune(name string, keep map[string]bool) error {
	entries, err := os.ReadDir(s.VersionsDir())
	if err != nil {
		return err
	}
	prefix := name + "-"
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		if keep[strings.TrimPrefix(e.Name(), prefix)] {
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.VersionsDir(), e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// checkName защищает раскладку: имена пакетов и версии приходят из манифеста, то есть
// из чужого файла, и не должны уметь выйти за пределы каталога установки.
func checkName(name, version string) error {
	for _, s := range []string{name, version} {
		if s == "" || s == "." || s == ".." || strings.ContainsAny(s, `/\`) {
			return fmt.Errorf("%q: недопустимое имя каталога версии", s)
		}
	}
	return nil
}

// writeFileAtomic — temp в том же каталоге и переименование: перезапись нельзя поймать
// в середине. На Windows rename поверх существующего файла не работает — убираем и пробуем снова.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(tmp, path)
}
