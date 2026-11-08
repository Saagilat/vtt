package app

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Saagilat/vtt/launcher/internal/manifest"
	"github.com/Saagilat/vtt/launcher/internal/store"
)

func TestCheckChangesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ещё-не-создан")
	cfg, out := baseConfig(t, dir, offline())
	cfg.Check = true

	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("--check: код %d\n%s", code, out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("--check создал каталог установки")
	}
	if !strings.Contains(out.String(), "установлено: ничего") {
		t.Fatalf("--check не сказал, что ничего не стоит:\n%s", out)
	}
	if !strings.Contains(out.String(), "манифест недоступен") {
		t.Fatalf("--check не сказал про недоступный манифест:\n%s", out)
	}
}

func TestCheckShowsVersionsAgainstRelease(t *testing.T) {
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("установка: код %d\n%s", code, out)
	}

	cfg, out = baseConfig(t, dir, publish(t, "1.5.0", clientSpec(t, "1.5.0")))
	cfg.Check = true
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("--check: код %d\n%s", code, out)
	}
	got := out.String()
	if !strings.Contains(got, "client 1.4.0") || !strings.Contains(got, "в релизе 1.5.0") {
		t.Fatalf("--check не сравнил установленное с релизом:\n%s", got)
	}
	st, err := store.OpenRead(dir)
	if err != nil {
		t.Fatal(err)
	}
	if inst := st.Installed("client"); inst != "1.4.0" {
		t.Fatalf("--check обновил клиент до %q", inst)
	}
}

func TestCheckReportsClientAnswer(t *testing.T) {
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("установка: код %d\n%s", code, out)
	}

	cfg, out = baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))
	cfg.Check = true
	// Клиент отвечает не той версией, что записана в state.json: проверка должна это сказать.
	t.Setenv("VTT_HELPER_VERSION", "1.3.0")
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("--check: код %d\n%s", code, out)
	}
	if !strings.Contains(out.String(), "отвечает версией 1.3.0") {
		t.Fatalf("--check не заметил расхождения с ответом клиента:\n%s", out)
	}
}

func TestUnknownManifestFormatLaunchesInstalled(t *testing.T) {
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("установка: код %d\n%s", code, out)
	}

	// Манифест новее формата: не применяем, работаем с установленным (правило 3).
	cfg, out = baseConfig(t, dir, publishWithFormat(t, 3, "1.4.0", clientSpec(t, "1.4.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("незнакомый формат: код %d, ожидали %d\n%s", code, ExitOK, out)
	}
	if !strings.Contains(out.String(), "формата манифеста") {
		t.Fatalf("не объяснили, почему манифест не применён:\n%s", out)
	}
	if _, err := os.Stat(markerPath(t)); err != nil {
		t.Fatalf("клиент не запущен: %v\n%s", err, out)
	}
}

func TestOptionalPackageOnlyByRequest(t *testing.T) {
	voice := pkgSpec{
		name: "voice", version: "0.3.1", file: "vtt-voice-0.3.1-ru.zip",
		payload: zipOf(t, map[string]zipItem{"voice": {body: []byte("бинарник голоса")}}),
	}
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0"), voice))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("запуск: код %d\n%s", code, out)
	}
	st, err := store.OpenRead(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Installed("voice"); got != "" {
		t.Fatalf("voice поставлен без просьбы: %q", got)
	}

	cfg, out = baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0"), voice))
	cfg.Packages = []string{"voice"}
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("с --packages voice: код %d\n%s", code, out)
	}
	if got := st.Installed("voice"); got != "0.3.1" {
		t.Fatalf("voice после просьбы: %q, ожидали 0.3.1\n%s", got, out)
	}
}

func TestMinClientSkipsPackage(t *testing.T) {
	voice := pkgSpec{
		name: "voice", version: "0.3.1", file: "vtt-voice-0.3.1-ru.zip",
		payload:   zipOf(t, map[string]zipItem{"voice": {body: []byte("голос")}}),
		minClient: "2.0.0",
	}
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0"), voice))
	cfg.Packages = []string{"voice"}
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("запуск: код %d\n%s", code, out)
	}
	if !strings.Contains(out.String(), "нужен клиент не ниже 2.0.0") {
		t.Fatalf("не сказали про минимальную версию клиента:\n%s", out)
	}
	st, err := store.OpenRead(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Installed("voice"); got != "" {
		t.Fatalf("voice поставлен, хотя клиент его не поддерживает: %q", got)
	}
}

func TestInstallFromDirectoryWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	src := writeReleaseDir(t, filepath.Join(t.TempDir(), "флешка"), "1.4.0", clientSpec(t, "1.4.0"))

	cfg, out := baseConfig(t, dir, offline())
	cfg.Source = src
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("--install: код %d\n%s", code, out)
	}
	st, err := store.OpenRead(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Installed("client"); got != "1.4.0" {
		t.Fatalf("после установки из файла client = %q\n%s", got, out)
	}
	if !strings.Contains(out.String(), "поставлено из") {
		t.Fatalf("не сказали, откуда поставлено:\n%s", out)
	}
}

func TestInstallFromDirectoryReportsBadHash(t *testing.T) {
	dir := t.TempDir()
	src := writeReleaseDir(t, filepath.Join(t.TempDir(), "флешка"), "1.4.0", clientSpec(t, "1.4.0"))
	// Файл испорчен уже после сборки манифеста: хеш в манифесте верный, файл — нет.
	if err := os.WriteFile(filepath.Join(src, "vtt-client-1.4.0-win64.zip"), []byte("мусор"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, out := baseConfig(t, dir, offline())
	cfg.Source = src
	if code := Run(context.Background(), cfg); code != ExitDelivery {
		t.Fatalf("испорченный файл: код %d, ожидали %d\n%s", code, ExitDelivery, out)
	}
	if !strings.Contains(out.String(), "sha256") && !strings.Contains(out.String(), "размер") {
		t.Fatalf("ошибка не объясняет, что не сошлось:\n%s", out)
	}
}

func TestInstallFromMissingDirectory(t *testing.T) {
	cfg, out := baseConfig(t, t.TempDir(), offline())
	cfg.Source = filepath.Join(t.TempDir(), "нет-такого")
	if code := Run(context.Background(), cfg); code != ExitDelivery {
		t.Fatalf("код %d, ожидали %d\n%s", code, ExitDelivery, out)
	}
}

func TestRunNoBuildForPlatformLaunchesInstalled(t *testing.T) {
	// Релиз собран не для этой системы: обновлять нечем, но играть можно тем, что стоит.
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("установка: код %d\n%s", code, out)
	}

	foreign := clientSpec(t, "1.5.0")
	if runtime.GOOS == "windows" {
		foreign.platform = manifest.Platform{OS: manifest.OSLinux, Arch: manifest.ArchAMD64}
	} else {
		foreign.platform = manifest.Platform{OS: manifest.OSWindows, Arch: manifest.ArchAMD64}
	}
	cfg, out = baseConfig(t, dir, publish(t, "1.5.0", foreign))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("чужая платформа: код %d, ожидали %d\n%s", code, ExitOK, out)
	}
	if !strings.Contains(out.String(), "нет сборки") {
		t.Fatalf("не сказали, что сборки для этой системы нет:\n%s", out)
	}
	if _, err := os.Stat(markerPath(t)); err != nil {
		t.Fatalf("установленный клиент не запущен: %v\n%s", err, out)
	}
}
