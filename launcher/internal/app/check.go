package app

import (
	"context"
	"os"
	"sort"
	"time"

	"github.com/Saagilat/vtt/launcher/internal/launch"
	"github.com/Saagilat/vtt/launcher/internal/manifest"
	"github.com/Saagilat/vtt/launcher/internal/store"
)

// check — сказать версии и чего не хватает, ничего не меняя.
//
// Сеть для этого не нужна: установленное видно и без неё. Манифест добавляет к ответу
// «есть новее», а его недоступность — не ошибка проверки, а факт, о котором сообщаем.
func (r *runner) check(ctx context.Context) int {
	state, err := r.st.Load()
	if err != nil {
		r.sayf("состояние не читается: %v", err)
		return ExitDelivery
	}
	r.sayf("каталог установки: %s", r.st.Dir())
	r.sayf("состояние: %s", r.st.StatePath())
	if len(state.Packages) == 0 {
		r.sayf("установлено: ничего")
	} else {
		r.sayf("установлено:")
		for _, name := range sortedKeys(state.Packages) {
			version := state.Packages[name].Version
			note := ""
			if _, err := r.st.EntryPoint(name, version); err != nil {
				note = " (программы в каталоге версии нет)"
			}
			r.sayf("  %s %s%s", name, version, note)
		}
	}
	if version := state.Packages[r.cfg.ClientName].Version; version != "" {
		r.reportClientAnswer(ctx, version)
	}

	m, err := r.manifestForCheck(ctx)
	if err != nil {
		r.sayf("манифест недоступен (%v) — версии только по установке", err)
		return ExitOK
	}
	r.sayf("в релизе %s:", m.Release)
	for _, p := range m.Packages {
		have := state.Packages[p.Name].Version
		switch {
		case have == "":
			r.sayf("  %s %s — не установлен", p.Name, p.Version)
		case have == p.Version:
			r.sayf("  %s %s — стоит", p.Name, p.Version)
		default:
			r.sayf("  %s %s — стоит %s", p.Name, p.Version, have)
		}
	}
	return ExitOK
}

// reportClientAnswer спрашивает у клиента его версию: `state.json` помнит, что мы поставили,
// а `--version` говорит, что из этого действительно работает. Расхождение — не ошибка проверки,
// а то, ради чего её и запускают.
func (r *runner) reportClientAnswer(ctx context.Context, version string) {
	path, err := r.st.EntryPoint(r.cfg.ClientName, version)
	if err != nil {
		return
	}
	got, err := launch.Version(ctx, path)
	switch {
	case err != nil:
		r.sayf("  %s не отвечает на --version: %v", r.cfg.ClientName, err)
	case got != "" && got != version:
		r.sayf("  %s отвечает версией %s, а включена %s", r.cfg.ClientName, got, version)
	}
}

// manifestForCheck берёт манифест из источника, если он задан, иначе из сети — но коротко:
// проверка не должна заставлять человека ждать недоступный хостинг.
func (r *runner) manifestForCheck(ctx context.Context) (*manifest.Manifest, error) {
	if r.cfg.Source != "" {
		root, err := os.MkdirTemp("", "vtt-check-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(root)
		dir, err := r.prepareSource(root)
		if err != nil {
			return nil, err
		}
		return readManifestDir(dir)
	}
	dl := r.dl
	dl.ManifestWait = 3 * time.Second
	data, err := dl.Manifest(ctx, r.cfg.ManifestURL)
	if err != nil {
		return nil, err
	}
	return manifest.Parse(data)
}

func sortedKeys(m map[string]store.Installed) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
