"""Командная строка подготовки: то, о чём договорились с клиентом — `prep/README.md`.

Проверяется без установки пакета и без единой внешней зависимости: `python3 -m vtt_prep`.
"""

from __future__ import annotations

import subprocess
import sys
import unittest
from pathlib import Path

PROJECT = Path(__file__).resolve().parents[1]


def run(*args: str) -> subprocess.CompletedProcess:
    """Прогон командной строки из каталога проекта."""
    return subprocess.run(
        [sys.executable, "-m", "vtt_prep", *args],
        cwd=PROJECT,
        capture_output=True,
        text=True,
    )


class CommandLine(unittest.TestCase):
    """Флаги, коды возврата и чистота stdout."""

    def test_version(self):
        result = run("--version")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(result.stdout.startswith("vtt-prep "), result.stdout)

    def test_help_names_both_flags(self):
        result = run("--help")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--in", result.stdout)
        self.assertIn("--out", result.stdout)

    def test_no_arguments_is_bad_input(self):
        result = run()
        self.assertEqual(result.returncode, 1)
        self.assertIn("нужны --in и --out", result.stderr)

    def test_half_of_arguments_is_bad_input(self):
        result = run("--in", "map.png")
        self.assertEqual(result.returncode, 1)
        self.assertIn("нужны --in и --out", result.stderr)

    def test_not_implemented_is_internal_error(self):
        result = run("--in", "map.png", "--out", "map.jpg")
        self.assertEqual(result.returncode, 2)
        self.assertIn("ещё не реализована", result.stderr)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
