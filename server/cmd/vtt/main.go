// Command vtt — игровой сервер и панель администратора в одном бинаре.
//
// Два режима и общая база: падение игры не должно отбирать панель, которой эту игру чинят
// (server/README.md). Сейчас это скелет — режимы и флаги проверяются, а игры, панели
// и базы ещё нет, и команда честно об этом говорит.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Saagilat/vtt/server/internal/annotation"
)

// defaultSchema — где схема контракта лежит относительно каталога запуска: в развёрнутом
// виде рядом с бинарём лежит contract/, и второй копии схемы в репозитории нет — копии
// расходятся молча.
const defaultSchema = "contract/annotation.v1.schema.json"

// version подставляется сборкой: -ldflags "-X main.version=v0.1.0".
var version = "dev"

// Коды возврата: 0 — сделали, 2 — запуск не тот (незнакомый режим, лишние аргументы,
// нечитаемая схема). Единицу не занимаем: ею будут отвечать падения по делу.
const (
	exitOK     = 0
	exitLaunch = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vtt", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		mode        = fs.String("mode", "", "режим работы: game или admin")
		showVersion = fs.Bool("version", false, "показать версию и выйти")
		schema      = fs.String("schema", defaultSchema, "схема контракта для проверки карт")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "vtt — сервер VTT: игра и панель администратора одним бинарём\n\n")
		fmt.Fprintf(fs.Output(), "  vtt --mode=game    игровой сервер: кампании, столы, туман, фишки\n")
		fmt.Fprintf(fs.Output(), "  vtt --mode=admin   панель: вход, нагрузка, обновление, логи\n")
		fmt.Fprintf(fs.Output(), "  vtt --version      версия и выход\n\n")
		fmt.Fprintf(fs.Output(), "Флаги:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return exitLaunch
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "vtt: лишние аргументы: %s\n", strings.Join(fs.Args(), " "))
		fs.Usage()
		return exitLaunch
	}
	if *showVersion {
		fmt.Fprintf(stdout, "vtt %s\n", version)
		return exitOK
	}

	switch *mode {
	case "":
		fmt.Fprintf(stderr, "vtt: нужен режим: --mode=game или --mode=admin\n")
		fs.Usage()
		return exitLaunch
	case "game", "admin":
	default:
		fmt.Fprintf(stderr, "vtt: незнакомый режим %q: бывают game (игра) и admin (панель)\n", *mode)
		return exitLaunch
	}

	// Схему читаем на старте: сервер, который не понимает контракт, не должен делать вид,
	// что жив. Импорта карт ещё нет, проверять пока нечего, но путь до схемы обязан работать
	// с первого коммита.
	if _, err := annotation.LoadSchema(*schema); err != nil {
		fmt.Fprintf(stderr, "vtt: схема контракта не читается (%s): %v\n", *schema, err)
		return exitLaunch
	}
	fmt.Fprintf(stdout,
		"vtt %s: режим %s, схема контракта прочитана; игры и панели ещё нет — это скелет\n",
		version, *mode)
	return exitOK
}
