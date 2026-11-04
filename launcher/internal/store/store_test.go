package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesLayout(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vtt")
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := os.Stat(st.VersionsDir()); err != nil {
		t.Fatalf("каталог versions не создан: %v", err)
	}
	if st.Dir() != dir {
		t.Fatalf("Dir() = %q, ожидали %q", st.Dir(), dir)
	}
}

func TestOpenReadDoesNotCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "нет-такого")
	if _, err := OpenRead(dir); err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("OpenRead создал каталог — проверка не должна ничего менять")
	}
}

func TestLoadEmptyOnCleanMachine(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	state, err := st.Load()
	if err != nil {
		t.Fatalf("Load на пустом каталоге: %v", err)
	}
	if len(state.Packages) != 0 || state.Format != stateFormat {
		t.Fatalf("состояние на чистой машине = %+v", state)
	}
	if got := st.Installed("client"); got != "" {
		t.Fatalf("Installed(client) = %q, ожидали пусто", got)
	}
}

func TestCommitAndActivateKeepPrevious(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, version := range []string{"1.3.0", "1.4.0", "1.5.0"} {
		if err := stage(st, "client", version); err != nil {
			t.Fatalf("подготовить %s: %v", version, err)
		}
		if err := st.Commit("client", version); err != nil {
			t.Fatalf("Commit(%s): %v", version, err)
		}
	}
	if got := st.Installed("client"); got != "1.5.0" {
		t.Fatalf("Installed(client) = %q, ожидали 1.5.0", got)
	}
	// Рядом остаются включённая и предыдущая: на них держится откат, а не копия истории.
	for version, want := range map[string]bool{"1.3.0": false, "1.4.0": true, "1.5.0": true} {
		_, err := os.Stat(st.VersionDir("client", version))
		if got := err == nil; got != want {
			t.Errorf("версия %s на диске: %v, ожидали %v", version, got, want)
		}
	}
	if _, err := os.Stat(st.StagedDir("client", "1.5.0")); !os.IsNotExist(err) {
		t.Errorf("распакованное осталось в tmp: %v", err)
	}
}

func TestStateSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := stage(st, "voice", "0.3.1"); err != nil {
		t.Fatalf("подготовить voice: %v", err)
	}
	if err := st.Commit("voice", "0.3.1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatalf("Open повторно: %v", err)
	}
	if got := again.Installed("voice"); got != "0.3.1" {
		t.Fatalf("после переоткрытия Installed(voice) = %q", got)
	}
}

func TestEntryPoint(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.MkdirAll(st.VersionDir("client", "1.4.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EntryPoint("client", "1.4.0"); err == nil {
		t.Fatalf("пустой каталог версии: EntryPoint должен ругаться")
	}
	path := filepath.Join(st.VersionDir("client", "1.4.0"), "client")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := st.EntryPoint("client", "1.4.0")
	if err != nil {
		t.Fatalf("EntryPoint: %v", err)
	}
	if got != path {
		t.Fatalf("EntryPoint = %q, ожидали %q", got, path)
	}
}

func TestCommitRejectsTraversal(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := st.Commit("..", "1.0.0"); err == nil {
		t.Fatalf("Commit с именем «..» прошёл — раскладка не защищена")
	}
}

func TestLoadBrokenState(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := os.WriteFile(st.StatePath(), []byte("{не json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(); err == nil {
		t.Fatalf("битый state.json прочитан без ошибки")
	}
}

// stage кладёт в tmp «распакованную» версию: так же, как это делает unpack.
func stage(st *Store, name, version string) error {
	dir := st.StagedDir(name, version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte("бинарь "+version), 0o755)
}
