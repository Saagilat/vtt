package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// schemaPath — схема контракта от каталога пакета (server/cmd/vtt).
var schemaPath = filepath.Join("..", "..", "..", "contract", "annotation.v1.schema.json")

// runOnce запускает команду и возвращает код возврата, stdout и stderr.
func runOnce(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestVersion — `--version` отвечает версией и не требует режима.
func TestVersion(t *testing.T) {
	code, stdout, stderr := runOnce("--version")
	if code != exitOK {
		t.Fatalf("код %d, ждали %d (%s)", code, exitOK, stderr)
	}
	if !strings.HasPrefix(stdout, "vtt ") {
		t.Fatalf("в stdout ждали версию, получили %q", stdout)
	}
}

// TestModes — оба режима стартуют и читают схему контракта: сервер, который не понимает
// контракт, не должен делать вид, что жив.
func TestModes(t *testing.T) {
	for _, mode := range []string{"game", "admin"} {
		t.Run(mode, func(t *testing.T) {
			code, stdout, stderr := runOnce("--mode="+mode, "--schema="+schemaPath)
			if code != exitOK {
				t.Fatalf("код %d, ждали %d (%s)", code, exitOK, stderr)
			}
			if !strings.Contains(stdout, mode) {
				t.Fatalf("в stdout ждали режим %s, получили %q", mode, stdout)
			}
		})
	}
}

// TestUnknownMode — незнакомый режим назван вслух, и перечислены те, что бывают: молча
// подняться в чужом режиме хуже, чем не подняться вовсе.
func TestUnknownMode(t *testing.T) {
	code, _, stderr := runOnce("--mode=nope", "--schema="+schemaPath)
	if code != exitLaunch {
		t.Fatalf("код %d, ждали %d", code, exitLaunch)
	}
	for _, word := range []string{"nope", "game", "admin"} {
		if !strings.Contains(stderr, word) {
			t.Fatalf("в отказе ждали слово %q: %s", word, stderr)
		}
	}
}

// TestNoMode — без режима команда говорит, чего не хватает, и не выбирает режим за человека.
func TestNoMode(t *testing.T) {
	code, _, stderr := runOnce()
	if code != exitLaunch {
		t.Fatalf("код %d, ждали %d", code, exitLaunch)
	}
	if !strings.Contains(stderr, "--mode") {
		t.Fatalf("в отказе ждали --mode: %s", stderr)
	}
}

// TestBrokenSchema — нечитаемая схема не даёт серверу стартовать: иначе он примет карту,
// которую не понимает.
func TestBrokenSchema(t *testing.T) {
	code, _, stderr := runOnce("--mode=game", "--schema="+filepath.Join(t.TempDir(), "нет.json"))
	if code != exitLaunch {
		t.Fatalf("код %d, ждали %d", code, exitLaunch)
	}
	if !strings.Contains(stderr, "схема") {
		t.Fatalf("в отказе ждали слово про схему: %s", stderr)
	}
}

// TestExtraArguments — лишний аргумент назван, а не проглочен.
func TestExtraArguments(t *testing.T) {
	code, _, stderr := runOnce("--mode=game", "лишний")
	if code != exitLaunch {
		t.Fatalf("код %d, ждали %d", code, exitLaunch)
	}
	if !strings.Contains(stderr, "лишний") {
		t.Fatalf("в отказе ждали лишний аргумент: %s", stderr)
	}
}
