// Command vtt-launcher ставит клиент, приводит его к версии из релиза и запускает.
//
// Интерфейс зафиксирован в launcher/README.md; коды возврата — 0 сделали, 1 не доставили,
// 2 доставили, но не запустили. Больше команд нет: лаунчер запускается человеком,
// а не сервером, и не знает адреса сервера (LAU-01).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Saagilat/vtt/launcher/internal/app"
	"github.com/Saagilat/vtt/launcher/internal/fetch"
	"github.com/Saagilat/vtt/launcher/internal/manifest"
)

// version подставляется сборкой: -ldflags "-X main.version=v0.1.0".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("vtt-launcher", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		showVersion = fs.Bool("version", false, "показать версию лаунчера и выйти")
		check       = fs.Bool("check", false, "сказать версии и чего не хватает, ничего не менять")
		installFrom = fs.String("install", "", "поставить из `каталога или архива`, без сети")
		packages    = fs.String("packages", "", "доставить необязательные пакеты: voice,annotator")
		installDir  = fs.String("install-dir", "", "каталог установки (по умолчанию — рядом с лаунчером)")
		manifestURL = fs.String("manifest", fetch.ManifestAdr, "адрес latest.json")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "vtt-launcher — установка и обновление клиента VTT\n\n")
		fmt.Fprintf(fs.Output(), "  vtt-launcher                            доставить недостающее и запустить клиент\n")
		fmt.Fprintf(fs.Output(), "  vtt-launcher --check                    сказать версии и чего не хватает\n")
		fmt.Fprintf(fs.Output(), "  vtt-launcher --install <каталог|архив>  поставить из файла, без сети\n")
		fmt.Fprintf(fs.Output(), "  vtt-launcher --packages voice,annotator доставить необязательные пакеты\n\n")
		fmt.Fprintf(fs.Output(), "Флаги:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return app.ExitDelivery
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "vtt-launcher: лишние аргументы: %s\n", strings.Join(fs.Args(), " "))
		fs.Usage()
		return app.ExitDelivery
	}
	if *showVersion {
		fmt.Printf("vtt-launcher %s\n", version)
		return app.ExitOK
	}

	dir := *installDir
	if dir == "" {
		// Лаунчер живёт рядом с клиентом в одном каталоге установки (D37): по умолчанию
		// ставим туда, откуда его запустили.
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "vtt-launcher: не понять, где я лежу: %v\n", err)
			return app.ExitDelivery
		}
		dir = filepath.Dir(exe)
	}

	requested, err := parsePackages(*packages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vtt-launcher: %v\n", err)
		return app.ExitDelivery
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return app.Run(ctx, app.Config{
		InstallDir:  dir,
		ManifestURL: *manifestURL,
		Packages:    requested,
		Source:      *installFrom,
		Check:       *check,
		Out:         os.Stdout,
		In:          os.Stdin,
	})
}

// parsePackages разбирает «voice, annotator». Имя пакета проверяется здесь же: незнакомое
// лучше назвать сразу, чем молча ничего не поставить (MANIFEST.md, правило 2).
func parsePackages(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var names []string
	for _, part := range strings.Split(s, ",") {
		name := strings.TrimSpace(part)
		switch name {
		case "":
			continue
		case manifest.NameClient, manifest.NameVoice, manifest.NameAnnotator:
			names = append(names, name)
		default:
			return nil, fmt.Errorf("незнакомый пакет %q: бывают %s, %s, %s",
				name, manifest.NameClient, manifest.NameVoice, manifest.NameAnnotator)
		}
	}
	return names, nil
}
