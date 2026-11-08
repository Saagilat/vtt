#!/usr/bin/env python3
"""Контракт меняется только с версией — `docs/projects.md`, правило 5.

Что проверяется всегда:

  * номер версии внутри схемы совпадает с номером в имени файла `annotation.vN.schema.json`;
  * пример `annotation.vN.example.json` есть у каждой версии и его `format_version` тот же;
  * самая свежая запись в `contract/CHANGELOG.md` — про эту версию.

Что проверяется по истории (`--base <ref>`):

  * правка содержимого контракта без строки в `contract/CHANGELOG.md` — отказ;
  * несовместимое изменение делается новым файлом версии, а старый остаётся читаемым.

Использование:
    python3 scripts/check_contract_version.py
    python3 scripts/check_contract_version.py --base <ref>

Код возврата: 0 — версия и журнал на месте, 1 — нет, 2 — беда с запуском.
"""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CONTRACT = ROOT / "contract"
CHANGELOG = CONTRACT / "CHANGELOG.md"
SCHEMA = re.compile(r"^annotation\.v(\d+)\.schema\.json$")
EXAMPLE = "annotation.v%d.example.json"
CHANGELOG_VERSION = re.compile(r"^##\s+v(\d+)\b", re.MULTILINE)
# Что считается содержимым контракта: журнал и README описывают его, остальное — сам контракт.
SERVICE = {"CHANGELOG.md", "README.md"}


def schemas() -> list[tuple[int, Path]]:
    """Все версии схемы, которые лежат в контракте."""
    found = []
    for path in sorted(CONTRACT.glob("annotation.v*.schema.json")):
        m = SCHEMA.match(path.name)
        if m:
            found.append((int(m.group(1)), path))
    return found


def schema_version(path: Path) -> int | None:
    """`format_version` из схемы: единственное число, которым версия живёт в файле."""
    document = json.loads(path.read_text(encoding="utf-8"))
    version = document.get("properties", {}).get("format_version", {}).get("const")
    return version if isinstance(version, int) else None


def changelog_versions() -> list[int]:
    """Версии, про которые есть запись в журнале, от новой к старой."""
    return [int(m.group(1)) for m in CHANGELOG_VERSION.finditer(CHANGELOG.read_text(encoding="utf-8"))]


def check_versions(bad: list[str]) -> None:
    """Версия в схеме, в имени файла, в примере и в журнале — одно и то же число."""
    if not CHANGELOG.exists():
        bad.append("нет contract/CHANGELOG.md")
        return
    found = schemas()
    if not found:
        bad.append("в contract/ нет ни одной схемы annotation.vN.schema.json")
        return
    for number, path in found:
        name = path.relative_to(ROOT)
        version = schema_version(path)
        if version != number:
            bad.append("%s: format_version %s, а имя файла говорит v%d" % (name, version, number))
        example = CONTRACT / (EXAMPLE % number)
        if not example.exists():
            bad.append("%s: нет примера %s" % (name, example.name))
            continue
        document = json.loads(example.read_text(encoding="utf-8"))
        if document.get("format_version") != number:
            bad.append("%s: format_version %s, а версия v%d"
                       % (example.relative_to(ROOT), document.get("format_version"), number))
    logged = changelog_versions()
    newest = max(number for number, _ in found)
    if not logged:
        bad.append("в contract/CHANGELOG.md нет ни одной записи вида «## vN»")
    elif logged[0] != newest:
        bad.append("в журнале сверху v%d, а самая свежая схема — v%d" % (logged[0], newest))


def git(*args: str) -> str | None:
    """Вывод git или None, если команда не отработала."""
    result = subprocess.run(
        ["git", "-C", str(ROOT), *args],
        capture_output=True,
        text=True,
    )
    return result.stdout if result.returncode == 0 else None


def check_history(bad: list[str], base: str) -> None:
    """Правка контракта приносит строку в журнал; версия не меняется молча."""
    if not base.strip("0"):
        print("base — нулевой коммит, историю не смотрим")
        return
    diff = git("diff", "--name-only", base)
    if diff is None:
        print("не удалось сравнить с %s" % base, file=sys.stderr)
        raise SystemExit(2)
    changed = [line for line in diff.splitlines() if line]
    contract = [p for p in changed if p.startswith("contract/") and p.split("/")[-1] not in SERVICE]
    if not contract:
        return
    if "contract/CHANGELOG.md" not in changed:
        bad.append("правка контракта без строки в contract/CHANGELOG.md: %s" % ", ".join(contract))
    removed = [p for p in changed if p.startswith("contract/") and p.endswith(".schema.json")]
    for path in removed:
        if not (ROOT / path).exists():
            bad.append("схема %s удалена: старая версия должна остаться читаемой" % path)


def main() -> int:
    parser = argparse.ArgumentParser(description="Версия контракта и журнал изменений")
    parser.add_argument("--base", default="", help="ref, с которым сравнивать историю")
    args = parser.parse_args()
    if not CONTRACT.is_dir():
        print("нет каталога contract/", file=sys.stderr)
        return 2
    bad: list[str] = []
    try:
        check_versions(bad)
    except (json.JSONDecodeError, OSError) as error:
        print("не читается контракт: %s" % error, file=sys.stderr)
        return 2
    if args.base:
        check_history(bad, args.base)
    print("broken:", len(bad))
    for item in bad:
        print("  ", item)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
