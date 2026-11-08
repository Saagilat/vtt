// Command vtt — игровой сервер и панель администратора в одном бинаре.
//
// Два режима и общая база: падение игры не должно отбирать панель, которой эту игру чинят
// (server/README.md). Сейчас это скелет — режимы и флаги проверяются, а игры, панели
// и базы ещё нет, и команда честно об этом говорит.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Saagilat/vtt/server/internal/annotation"
	"github.com/Saagilat/vtt/server/internal/api"
	"github.com/Saagilat/vtt/server/internal/storage"
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
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vtt", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		mode        = fs.String("mode", "", "режим работы: game или admin")
		showVersion = fs.Bool("version", false, "показать версию и выйти")
		schema      = fs.String("schema", defaultSchema, "схема контракта для проверки карт")
		dbPath      = fs.String("db", "vtt.db", "файл базы SQLite")
		listen      = fs.String("listen", "", "адрес HTTP для режима game (пусто — не слушать)")
		createAcct  = fs.String("create-account", "", "завести аккаунт с этим логином и выйти; пароль — из stdin")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "vtt — сервер VTT: игра и панель администратора одним бинарём\n\n")
		fmt.Fprintf(fs.Output(), "  vtt --mode=game    игровой сервер: кампании, столы, туман, фишки\n")
		fmt.Fprintf(fs.Output(), "  vtt --mode=admin   панель: вход, нагрузка, обновление, логи\n")
		fmt.Fprintf(fs.Output(), "  vtt --mode=game --listen=:8080  поднять HTTP игры\n")
		fmt.Fprintf(fs.Output(), "  vtt --create-account alice      завести аккаунт (пароль из stdin)\n")
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

	if *createAcct != "" {
		return addAccount(*dbPath, *createAcct, in, stdout, stderr)
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
	validator, err := annotation.LoadSchema(*schema)
	if err != nil {
		fmt.Fprintf(stderr, "vtt: схема контракта не читается (%s): %v\n", *schema, err)
		return exitLaunch
	}
	if *mode == "game" && *listen != "" {
		return serveGame(*dbPath, *listen, validator, stdout, stderr)
	}
	fmt.Fprintf(stdout,
		"vtt %s: режим %s, схема контракта прочитана; HTTP не слушается без --listen\n",
		version, *mode)
	return exitOK
}

// serveGame — режим игры: база, API и HTTP на --listen.
func serveGame(dbPath, listen string, validator *annotation.Validator, stdout, stderr io.Writer) int {
	db, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "vtt: база: %v\n", err)
		return exitLaunch
	}
	defer db.Close()
	handler, err := api.New(api.Config{Directory: db, Sessions: db, Content: db, Validator: validator})
	if err != nil {
		fmt.Fprintf(stderr, "vtt: API: %v\n", err)
		return exitLaunch
	}
	server := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	fmt.Fprintf(stdout, "vtt %s: слушаю %s, база %s\n", version, listen, dbPath)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(stderr, "vtt: %v\n", err)
		return exitLaunch
	}
	return exitOK
}

// addAccount — завести аккаунт из командной строки. Это временный путь до панели
// администратора: пароль читается из stdin, чтобы не остаться в истории команд.
func addAccount(dbPath, login string, in io.Reader, stdout, stderr io.Writer) int {
	if strings.TrimSpace(login) == "" {
		fmt.Fprintf(stderr, "vtt: пустой логин\n")
		return exitLaunch
	}
	fmt.Fprintf(stderr, "пароль для %s (ввод виден): ", login)
	password, err := readPassword(in)
	if err != nil {
		fmt.Fprintf(stderr, "\nvtt: %v\n", err)
		return exitLaunch
	}
	db, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintf(stderr, "vtt: база: %v\n", err)
		return exitLaunch
	}
	defer db.Close()
	account, err := db.CreateAccount(login, password)
	if err != nil {
		fmt.Fprintf(stderr, "vtt: %v\n", err)
		return exitLaunch
	}
	fmt.Fprintf(stdout, "аккаунт %s заведён: %s\n", account.Login, account.ID)
	return exitOK
}

func readPassword(in io.Reader) (string, error) {
	if in == nil {
		return "", errors.New("пароль неоткуда прочитать")
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	password := strings.TrimRight(line, "\r\n")
	if password == "" {
		return "", errors.New("пустой пароль")
	}
	return password, nil
}
