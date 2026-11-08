// Package app собирает лаунчер из частей: манифест → файлы → каталог установки → клиент.
//
// Откуда берутся файлы, решает источник, а не это пакет: сеть и флешка отличаются одним
// способом достать файл, дальше путь один (MANIFEST.md, «Установка из файла»). Иначе
// появился бы второй формат поставки, который надо держать в согласии с первым (D37).
package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/Saagilat/vtt/launcher/internal/fetch"
	"github.com/Saagilat/vtt/launcher/internal/launch"
	"github.com/Saagilat/vtt/launcher/internal/manifest"
	"github.com/Saagilat/vtt/launcher/internal/semver"
	"github.com/Saagilat/vtt/launcher/internal/store"
	"github.com/Saagilat/vtt/launcher/internal/unpack"
)

// Коды возврата лаунчера — закрытый список из launcher/README.md.
const (
	// ExitOK — сделали, что просили.
	ExitOK = 0
	// ExitDelivery — доставить не удалось: сети нет, хеш не сошёлся, нет места.
	ExitDelivery = 1
	// ExitLaunchFail — доставили, но запустить не удалось.
	ExitLaunchFail = 2
)

// Config — вход лаунчера: то же, что пришло из командной строки.
type Config struct {
	InstallDir  string
	ManifestURL string
	// Packages — необязательные пакеты, которые человек попросил поставить.
	// Пусто — ставится и обновляется только client (D37, Q42).
	Packages []string
	// Source — каталог или архив с latest.json и файлами (`--install`). Не пусто — сети нет.
	Source string
	// Check — только сказать, что стоит, и ничего не менять (`--check`).
	Check bool
	// Out — куда писать человеку. Пусто — os.Stdout.
	Out io.Writer
	// In — stdin клиента. Пусто — os.Stdin.
	In io.Reader
	// Downloader — сеть; нулевое значение работает как fetch.New().
	Downloader fetch.Downloader
	// ClientName — как называется программа клиента в каталоге версии.
	ClientName string
	// Platform — для какой платформы берём записи манифеста. Пусто — эта машина.
	Platform manifest.Platform
}

type runner struct {
	cfg Config
	out io.Writer
	st  *store.Store
	dl  fetch.Downloader
}

// Run выполняет то, о чём попросили, и возвращает код возврата лаунчера.
func Run(ctx context.Context, cfg Config) int {
	r := &runner{cfg: cfg, out: cfg.Out, dl: cfg.Downloader}
	if r.out == nil {
		r.out = os.Stdout
	}
	if r.cfg.ManifestURL == "" {
		r.cfg.ManifestURL = fetch.ManifestAdr
	}
	if r.cfg.Downloader.Client == nil && r.cfg.Downloader.ManifestWait == 0 {
		r.dl = fetch.New()
	}
	if r.cfg.ClientName == "" {
		r.cfg.ClientName = manifest.NameClient
	}
	if r.cfg.Platform.OS == "" {
		r.cfg.Platform = manifest.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	}
	// `--check` ничего не меняет: даже пустой каталог установки не создаётся.
	open := store.Open
	if cfg.Check {
		open = store.OpenRead
	}
	st, err := open(cfg.InstallDir)
	if err != nil {
		r.sayf("лаунчер: %v", err)
		return ExitDelivery
	}
	r.st = st

	switch {
	case cfg.Check:
		return r.check(ctx)
	case cfg.Source != "":
		return r.install(ctx)
	default:
		return r.update(ctx)
	}
}

func (r *runner) sayf(format string, args ...any) {
	fmt.Fprintf(r.out, format+"\n", args...)
}

// update — обычный запуск: сверить с манифестом, доставить недостающее, запустить клиент.
func (r *runner) update(ctx context.Context) int {
	m, err := r.manifestFromNet(ctx)
	if err != nil {
		// Тишина сети — не повод не играть: запускаем то, что стоит (D38).
		r.sayf("манифест недоступен: %v", err)
		return r.launchInstalled(ctx)
	}
	client, err := m.ClientFor(r.cfg.Platform)
	if err != nil {
		// В релизе нет сборки для этой системы — играем тем, что стоит.
		r.sayf("%v", err)
		return r.launchInstalled(ctx)
	}
	src := netSource{dl: r.dl, base: fetch.BaseURL(r.cfg.ManifestURL), dir: r.st.TmpDir()}
	if code := r.apply(ctx, m, src, client); code != ExitOK {
		return code
	}
	return r.launchInstalled(ctx)
}

// install — поставить из файла, без сети: тот же манифест, тот же путь, другой источник.
func (r *runner) install(ctx context.Context) int {
	srcDir, err := r.prepareSource(r.st.TmpDir())
	if err != nil {
		r.sayf("установка из файла: %v", err)
		return ExitDelivery
	}
	m, err := readManifestDir(srcDir)
	if err != nil {
		r.sayf("установка из файла: %v", err)
		return ExitDelivery
	}
	client, err := m.ClientFor(r.cfg.Platform)
	if err != nil {
		r.sayf("установка из файла: %v", err)
		return ExitDelivery
	}
	src := dirSource{dir: srcDir}
	if code := r.apply(ctx, m, src, client); code != ExitOK {
		return code
	}
	r.sayf("поставлено из %s: %s %s", src.dir, client.Name, client.Version)
	return ExitOK
}

// apply приводит установку к манифесту. Обязательный пакет — client; необязательные
// доставляются только по просьбе человека и только если клиент их поддерживает (D37).
func (r *runner) apply(ctx context.Context, m *manifest.Manifest, src source, client manifest.Package) int {
	if code := r.deliver(ctx, src, client, true); code != ExitOK {
		return code
	}
	for _, name := range r.cfg.Packages {
		if name == manifest.NameClient {
			continue
		}
		p, ok := m.Find(name, r.cfg.Platform)
		if !ok {
			r.sayf("%s: в релизе %s такого пакета нет — пропускаю", name, m.Release)
			continue
		}
		if p.MinClient != "" && compareVersions(client.Version, p.MinClient) < 0 {
			r.sayf("%s %s: нужен клиент не ниже %s, а в релизе %s — пропускаю",
				p.Name, p.Version, p.MinClient, client.Version)
			continue
		}
		r.deliver(ctx, src, p, false)
	}
	r.reportUnrequested(m)
	return ExitOK
}

// deliver ставит один пакет. Ошибка обязательного пакета — провал поставки; ошибка
// необязательного — предупреждение: без голоса играть можно, без клиента нельзя.
func (r *runner) deliver(ctx context.Context, src source, p manifest.Package, required bool) int {
	fail := func(format string, args ...any) int {
		r.sayf(format, args...)
		if required {
			return ExitDelivery
		}
		return ExitOK
	}

	installed := r.st.Installed(p.Name)
	if installed == p.Version {
		if _, err := os.Stat(r.st.VersionDir(p.Name, p.Version)); err == nil {
			r.sayf("%s: %s уже стоит", p.Name, p.Version)
			return ExitOK
		}
	}

	file, err := src.file(ctx, p)
	if err != nil {
		return fail("%s: %v", p.Name, err)
	}
	r.sayf("%s: ставлю %s (%s)", p.Name, p.Version, humanSize(p.Size))

	staged := r.st.StagedDir(p.Name, p.Version)
	if err := os.RemoveAll(staged); err != nil {
		return fail("%s: %v", p.Name, err)
	}
	if err := unpack.File(file, staged); err != nil {
		return fail("%s: распаковать не удалось: %v", p.Name, err)
	}
	if p.Name == manifest.NameClient {
		// Клиента лаунчер запускает сам, поэтому его точку входа проверяем до включения
		// версии: иначе «поставили» закончится отказом при первом же запуске, уже поверх
		// прошлой версии.
		if _, err := store.EntryPointIn(staged, p.Name); err != nil {
			return fail("%s: %v", p.Name, err)
		}
	}
	if err := r.st.Commit(p.Name, p.Version); err != nil {
		return fail("%s: %v", p.Name, err)
	}
	if installed == "" {
		r.sayf("%s: %s поставлен", p.Name, p.Version)
	} else {
		r.sayf("%s: %s → %s", p.Name, installed, p.Version)
	}
	return ExitOK
}

// reportUnrequested напоминает про необязательные пакеты, которые стоят не той версии:
// сам лаунчер их не трогает (молча доставляется только обязательное), но человек должен знать.
func (r *runner) reportUnrequested(m *manifest.Manifest) {
	state, err := r.st.Load()
	if err != nil {
		return
	}
	for name, inst := range state.Packages {
		if name == manifest.NameClient {
			continue
		}
		p, ok := m.Find(name, r.cfg.Platform)
		if !ok || p.Version == inst.Version || r.requested(name) {
			continue
		}
		r.sayf("%s: стоит %s, в релизе %s — обновится по --packages %s", name, inst.Version, p.Version, name)
	}
}

func (r *runner) requested(name string) bool {
	for _, p := range r.cfg.Packages {
		if p == name {
			return true
		}
	}
	return false
}

// launchInstalled запускает клиент, который стоит сейчас.
func (r *runner) launchInstalled(ctx context.Context) int {
	version := r.st.Installed(r.cfg.ClientName)
	if version == "" {
		r.sayf("клиента нет: запускать нечего")
		return ExitDelivery
	}
	path, err := r.st.EntryPoint(r.cfg.ClientName, version)
	if err != nil {
		r.sayf("%v", err)
		return ExitLaunchFail
	}
	r.sayf("запускаю %s %s", r.cfg.ClientName, version)
	code, err := launch.Run(path, []string{launch.InstallDirFlag + "=" + r.st.Dir()}, r.in(), r.out)
	if err != nil {
		r.sayf("%v", err)
		return ExitLaunchFail
	}
	if err := r.st.CleanTmp(); err != nil {
		r.sayf("убрать временные файлы не удалось: %v", err)
	}
	if code != 0 {
		r.sayf("%s завершился с кодом %d", r.cfg.ClientName, code)
	}
	return ExitOK
}

func (r *runner) in() io.Reader {
	if r.cfg.In != nil {
		return r.cfg.In
	}
	return os.Stdin
}

func compareVersions(a, b string) int {
	va, errA := semver.Parse(a)
	vb, errB := semver.Parse(b)
	if errA != nil || errB != nil {
		return 0
	}
	return va.Compare(vb)
}

// humanSize — для строки «ставлю 39.3 МБ»: человеку важен порядок, а не байт.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d Б", n)
	}
	units := []string{"КБ", "МБ", "ГБ", "ТБ"}
	size := float64(n)
	i := -1
	for size >= unit && i < len(units)-1 {
		size /= unit
		i++
	}
	return fmt.Sprintf("%.1f %s", size, units[i])
}
