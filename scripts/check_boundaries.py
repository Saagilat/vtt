#!/usr/bin/env python3
"""Правила независимости проектов — `docs/projects.md`, «Правила независимости».

Что проверяется:

  * в корне репозитория нет манифестов: у каждого проекта свой;
  * список папок-проектов совпадает с таблицей «Части и папки» в `docs/projects.md`;
  * Go-модуль одного проекта не импортирует модуль другого;
  * Python одного проекта не импортирует пакет другого и не ходит в чужой каталог;
  * `sys.path` в проекте правится только на общую инструментальную папку `scripts/`.

`scripts/` — не проект: у неё нет сборки и манифеста, и её не импортирует никто, кроме
проверок, которым нужен общий Python-валидатор контракта.

Использование: python3 scripts/check_boundaries.py
Код возврата: 0 — границы на месте, 1 — нет, 2 — беда с запуском (нет docs/projects.md).
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
PROJECTS_DOC = ROOT / "docs" / "projects.md"
ROOT_MANIFESTS = ("pyproject.toml", "go.mod", "go.work", "package.json", "Cargo.toml")
SHARED = "scripts"
TABLE_ROW = re.compile(r"^\|\s*`([a-z][a-z0-9_-]*)/`\s*\|")
GO_MODULE = re.compile(r"^module\s+(\S+)", re.MULTILINE)
GO_IMPORT_BLOCK = re.compile(r"^import\s*\((.*?)^\)", re.MULTILINE | re.DOTALL)
PY_IMPORT = re.compile(r"^\s*(?:from|import)\s+([A-Za-z_][\w.]*)", re.MULTILINE)
QUOTED = re.compile(r'"([^"]*)"' + r"|'([^']*)'")


def projects() -> list[str]:
    """Папки-проекты из таблицы «Части и папки»."""
    found = []
    for line in PROJECTS_DOC.read_text(encoding="utf-8").split("\n"):
        m = TABLE_ROW.match(line)
        if m:
            found.append(m.group(1))
    return found


def no_root_manifests(bad: list[str]) -> None:
    """Манифест в корне — значит проект не один, а «всё вместе»."""
    for name in ROOT_MANIFESTS:
        if (ROOT / name).exists():
            bad.append("в корне лежит манифест %s: он должен быть у проекта" % name)


# Локальные каталоги, которых нет в git (тестовые карты, результаты стендов), не проекты:
# их имена перечислены в .gitignore с косой чертой на конце.
def ignored_top_level() -> set[str]:
    gitignore = ROOT / ".gitignore"
    names: set[str] = set()
    if not gitignore.exists():
        return names
    for line in gitignore.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or not line.endswith("/"):
            continue
        name = line.rstrip("/")
        if name and "/" not in name:
            names.add(name)
    return names


def folder_list(bad: list[str], expected: list[str]) -> None:
    """Папки проектов и таблица в документе не должны расходиться."""
    ignored = ignored_top_level()
    actual = {
        p.name
        for p in ROOT.iterdir()
        if p.is_dir() and not p.name.startswith(".") and p.name != SHARED
        and p.name not in ignored
    }
    for name in sorted(set(expected) - actual):
        bad.append("docs/projects.md называет папку %s/, а её нет" % name)
    for name in sorted(actual - set(expected)):
        bad.append("папки %s/ нет в таблице docs/projects.md" % name)


def go_modules() -> dict[str, tuple[str, Path]]:
    """Модуль Go → проект, которому он принадлежит."""
    found = {}
    for name in projects():
        gomod = ROOT / name / "go.mod"
        if not gomod.exists():
            continue
        m = GO_MODULE.search(gomod.read_text(encoding="utf-8"))
        if m:
            found[m.group(1)] = (name, ROOT / name)
    return found


def go_imports(path: Path) -> list[str]:
    """Пути импорта из одного Go-файла."""
    text = path.read_text(encoding="utf-8")
    paths = [m.group(1) for m in GO_IMPORT_BLOCK.finditer(text)]
    block = GO_IMPORT_BLOCK.sub("", text)
    for m in re.finditer(r'^import\s+(?:\w+\s+)?"([^"]+)"', block, re.MULTILINE):
        paths.append(m.group(1))
    return paths


def check_go(bad: list[str]) -> None:
    """Go одного проекта не знает про Go другого."""
    modules = go_modules()
    for module, (owner, base) in modules.items():
        for path in base.rglob("*.go"):
            for imported in go_imports(path):
                for other, (other_owner, _) in modules.items():
                    if other_owner == owner:
                        continue
                    if imported == other or imported.startswith(other + "/"):
                        bad.append(
                            "%s импортирует %s из проекта %s"
                            % (path.relative_to(ROOT), imported, other_owner)
                        )


def python_packages(base: Path) -> set[str]:
    """Имена пакетов Python, которые принадлежат проекту."""
    return {p.parent.name for p in base.rglob("__init__.py") if p.parent != base}


def check_python(bad: list[str], expected: list[str]) -> None:
    """Python одного проекта не импортирует Python другого и не правит sys.path."""
    owners: dict[str, str] = {}
    for name in expected:
        for package in python_packages(ROOT / name):
            owners[package] = name
    for name in expected:
        base = ROOT / name
        for path in base.rglob("*.py"):
            text = path.read_text(encoding="utf-8")
            for imported in PY_IMPORT.findall(text):
                owner = owners.get(imported.split(".")[0])
                if owner and owner != name:
                    bad.append(
                        "%s импортирует %s из проекта %s"
                        % (path.relative_to(ROOT), imported, owner)
                    )
            for line in text.split("\n"):
                if "sys.path" not in line:
                    continue
                for m in QUOTED.finditer(line):
                    value = m.group(1) if m.group(1) is not None else m.group(2)
                    for part in Path(value).parts:
                        if part in expected and part != name:
                            bad.append(
                                "%s правит sys.path в сторону проекта %s"
                                % (path.relative_to(ROOT), part)
                            )


def main() -> int:
    if not PROJECTS_DOC.exists():
        print("нет %s" % PROJECTS_DOC, file=sys.stderr)
        return 2
    expected = projects()
    if not expected:
        print("в %s нет таблицы «Части и папки»" % PROJECTS_DOC, file=sys.stderr)
        return 2
    bad: list[str] = []
    no_root_manifests(bad)
    folder_list(bad, expected)
    check_go(bad)
    check_python(bad, expected)
    print("broken:", len(bad))
    for item in bad:
        print("  ", item)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
