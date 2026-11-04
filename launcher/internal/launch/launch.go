// Package launch запускает клиент и возвращает его код.
//
// Между лаунчером и клиентом нет ни сокета, ни порта: лаунчер передаёт каталог установки,
// клиент отвечает на `--version`. Это две программы, а не одна (launcher/README.md).
package launch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// InstallDirFlag — как лаунчер сообщает клиенту, где лежит установка.
const InstallDirFlag = "--install-dir"

// Run запускает программу и ждёт её выхода. Код возврата клиента — его дело, а не ошибка
// лаунчера: ошибка здесь только тогда, когда запустить не удалось.
func Run(path string, args []string, in io.Reader, out io.Writer) (int, error) {
	cmd := exec.Command(path, args...)
	cmd.Stdin = in
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return -1, fmt.Errorf("запустить %s: %w", path, err)
}

// Version спрашивает у клиента его версию. Пустой ответ — не ошибка: старый клиент может
// не понимать `--version`, и лаунчер тогда говорит только то, что записано в state.json.
func Version(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Stdin = nil
	cmd.Stderr = nil
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("%s --version: код %d", path, exit.ExitCode())
		}
		return "", fmt.Errorf("%s --version: %w", path, err)
	}
	line, _ := bufio.NewReader(strings.NewReader(string(out))).ReadString('\n')
	return strings.TrimSpace(line), nil
}
