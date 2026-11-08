"""Командная строка разметчика: то, о чём договорились с клиентом — `annotator/README.md`.

Проверяется без установки пакета и без внешних зависимостей: `python3 -m vtt_annotator`.
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
        [sys.executable, "-m", "vtt_annotator", *args],
        cwd=PROJECT,
        capture_output=True,
        text=True,
    )


class CommandLine(unittest.TestCase):
    """Три режима, порог уверенности и коды возврата."""

    def test_version(self):
        result = run("--version")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(result.stdout.startswith("vtt-annotate "), result.stdout)

    def test_help_names_every_mode(self):
        result = run("--help")
        self.assertEqual(result.returncode, 0, result.stderr)
        for flag in ("--image", "--out", "--render", "--check", "--min-confidence"):
            self.assertIn(flag, result.stdout)

    def test_no_arguments_is_bad_input(self):
        result = run()
        self.assertEqual(result.returncode, 1)
        self.assertIn("нужны --image и --out или --check", result.stderr)

    def test_half_of_arguments_is_bad_input(self):
        result = run("--image", "map.jpg")
        self.assertEqual(result.returncode, 1)
        self.assertIn("нужны --image и --out или --check", result.stderr)

    def test_check_and_image_together_are_rejected(self):
        result = run("--check", "annotation.json", "--image", "map.jpg", "--out", "x.json")
        self.assertEqual(result.returncode, 1)
        self.assertIn("не сочетается", result.stderr)

    def test_confidence_out_of_range_is_bad_input(self):
        result = run("--image", "map.jpg", "--out", "annotation.json", "--min-confidence", "1.5")
        self.assertEqual(result.returncode, 1)
        self.assertIn("от 0 до 1", result.stderr)

    def test_not_implemented_is_internal_error(self):
        result = run("--image", "map.jpg", "--out", "annotation.json")
        self.assertEqual(result.returncode, 2)
        self.assertIn("ещё не реализована", result.stderr)
        self.assertEqual(result.stdout, "")

    def test_check_is_not_implemented(self):
        result = run("--check", "annotation.json")
        self.assertEqual(result.returncode, 2)
        self.assertIn("ещё не реализована", result.stderr)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
