package launch

import (
	"io"
	"os"
	"testing"
)

func TestRunOnMissingProgram(t *testing.T) {
	// Программы нет — это провал запуска, а не «клиент вышел с кодом».
	code, err := Run("/нет/такой/программы", nil, nil, io.Discard)
	if err == nil {
		t.Fatalf("запуск несуществующей программы прошёл без ошибки, код %d", code)
	}
	if code != -1 {
		t.Fatalf("код = %d, ожидали -1: своего кода у неудавшегося запуска нет", code)
	}
}

func TestRunReturnsClientExitCode(t *testing.T) {
	// Клиентом служит сам тестовый бинарь: с -test.run=^$ он ничего не делает и выходит с 0.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if code, err := Run(self, []string{"-test.run=^$"}, nil, io.Discard); err != nil || code != 0 {
		t.Fatalf("Run = %d, %v; ожидали 0 без ошибки", code, err)
	}
	// Неизвестный флаг — тестовый бинарь выходит не с нулём: этот код лаунчер обязан вернуть
	// как есть, чтобы «клиент упал» было видно снаружи.
	if code, err := Run(self, []string{"-test.run=^$", "-нет-такого-флага"}, nil, io.Discard); err != nil || code == 0 {
		t.Fatalf("Run = %d, %v; ожидали ненулевой код клиента без ошибки запуска", code, err)
	}
}

func TestVersionOnMissingProgram(t *testing.T) {
	if _, err := Version(t.Context(), "/нет/такой/программы"); err == nil {
		t.Fatalf("Version несуществующей программы прошёл без ошибки")
	}
}
