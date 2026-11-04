package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Saagilat/vtt/launcher/internal/store"
)

func clientSpec(t *testing.T, version string) pkgSpec {
	t.Helper()
	return pkgSpec{
		name: "client", version: version,
		file:    "vtt-client-" + version + "-win64.zip",
		payload: clientPayload(t),
	}
}

func TestRunInstallsAndLaunches(t *testing.T) {
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))

	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("Run = %d, ожидали %d\n%s", code, ExitOK, out)
	}
	st, err := store.OpenRead(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Installed("client"); got != "1.4.0" {
		t.Fatalf("установлено %q, ожидали 1.4.0\n%s", got, out)
	}
	marker, err := os.ReadFile(markerPath(t))
	if err != nil {
		t.Fatalf("клиент не запущен: %v\n%s", err, out)
	}
	if !strings.Contains(string(marker), "--install-dir=") || !strings.Contains(string(marker), dir) {
		t.Fatalf("клиенту передан не каталог установки: %q", marker)
	}
	if !strings.Contains(out.String(), "поставлен") || !strings.Contains(out.String(), "запускаю client 1.4.0") {
		t.Fatalf("вывод не сказал, что произошло:\n%s", out)
	}
}

func TestRunUpdatesKeepsPrevious(t *testing.T) {
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("первая установка: код %d\n%s", code, out)
	}

	cfg, out = baseConfig(t, dir, publish(t, "1.5.0", clientSpec(t, "1.5.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("обновление: код %d\n%s", code, out)
	}
	if !strings.Contains(out.String(), "1.4.0 → 1.5.0") {
		t.Fatalf("обновление не сказало про смену версии:\n%s", out)
	}
	st, err := store.OpenRead(dir)
	if err != nil {
		t.Fatal(err)
	}
	for version, want := range map[string]bool{"1.4.0": true, "1.5.0": true} {
		if _, err := os.Stat(st.VersionDir("client", version)); (err == nil) != want {
			t.Errorf("версия %s на диске: %v, ожидали %v", version, err, want)
		}
	}
}

func TestRunWithoutManifestLaunchesInstalled(t *testing.T) {
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("первая установка: код %d\n%s", code, out)
	}

	// Сети нет: лаунчер молча запускает то, что стоит (D38).
	cfg, out = baseConfig(t, dir, offline())
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("без манифеста: код %d, ожидали %d\n%s", code, ExitOK, out)
	}
	if !strings.Contains(out.String(), "манифест недоступен") {
		t.Fatalf("человеку не сказали, что манифест недоступен:\n%s", out)
	}
	if _, err := os.Stat(markerPath(t)); err != nil {
		t.Fatalf("клиент не запущен без сети: %v\n%s", err, out)
	}
}

func TestRunWithoutManifestAndClientFails(t *testing.T) {
	cfg, out := baseConfig(t, t.TempDir(), offline())
	if code := Run(context.Background(), cfg); code != ExitDelivery {
		t.Fatalf("код %d, ожидали %d\n%s", code, ExitDelivery, out)
	}
	if !strings.Contains(out.String(), "запускать нечего") {
		t.Fatalf("не сказали, что клиента нет:\n%s", out)
	}
}

func TestArchiveWithoutClientEntryFails(t *testing.T) {
	// Архив без client внутри: поставлять нечего, и включать такую версию нельзя —
	// иначе следующий запуск упрётся в «запускать нечего».
	broken := pkgSpec{
		name: "client", version: "1.4.0", file: "vtt-client-1.4.0-win64.zip",
		payload: zipOf(t, map[string]zipItem{"заметка": {body: []byte("не клиент")}}),
	}
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", broken))
	if code := Run(context.Background(), cfg); code != ExitDelivery {
		t.Fatalf("код %d, ожидали %d\n%s", code, ExitDelivery, out)
	}
	if !strings.Contains(out.String(), "нет ни client") {
		t.Fatalf("не сказали, что в архиве нет клиента:\n%s", out)
	}
	st, err := store.OpenRead(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Installed("client"); got != "" {
		t.Fatalf("после неудачи включена версия %q", got)
	}
}

func TestRunBadHashKeepsPrevious(t *testing.T) {
	dir := t.TempDir()
	cfg, out := baseConfig(t, dir, publish(t, "1.4.0", clientSpec(t, "1.4.0")))
	if code := Run(context.Background(), cfg); code != ExitOK {
		t.Fatalf("первая установка: код %d\n%s", code, out)
	}

	broken := clientSpec(t, "1.5.0")
	broken.serve = []byte("не тот архив")
	cfg, out = baseConfig(t, dir, publish(t, "1.5.0", broken))
	if code := Run(context.Background(), cfg); code != ExitDelivery {
		t.Fatalf("битый архив: код %d, ожидали %d\n%s", code, ExitDelivery, out)
	}
	st, err := store.OpenRead(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Installed("client"); got != "1.4.0" {
		t.Fatalf("после неудачи включена %q, а должна остаться 1.4.0", got)
	}
	if _, err := os.Stat(markerPath(t)); err == nil {
		t.Fatalf("после неудачи клиент всё равно запущен")
	}
	if !strings.Contains(out.String(), "sha256") && !strings.Contains(out.String(), "размер") {
		t.Fatalf("ошибка не объясняет, что не сошлось:\n%s", out)
	}
}
