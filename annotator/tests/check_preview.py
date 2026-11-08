#!/usr/bin/env python3
"""Инвариант предварительного файла — `annotator/PREVIEW.md`.

Что проверяется:

  * `confidence` есть у каждого элемента и это число от 0 до 1;
  * файл без `confidence` проходит контракт v1: форма, картинка, геометрия;
  * каждый элемент результата есть в предварительном файле с той же геометрией и `type`;
  * `format_version`, `image` и `grid` у двух файлов совпадают;
  * хотя бы один элемент в файле есть, а в результате его нет — иначе пример перестал
    показывать то, ради чего написан.

Результатом прогона считается пример из контракта: `contract/annotation.v1.example.json`. Так
инвариант проверяется на том же документе, который читают оба валидатора, а не на выдуманном рядом.

Использование: python3 annotator/tests/check_preview.py
Код возврата: 0 — инвариант держится, 1 — нет, 2 — беда с запуском (нет файла, файл не JSON).
"""

from __future__ import annotations

import copy
import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "scripts"))

import validate_annotation as contract  # noqa: E402

PREVIEW_PATH = REPO_ROOT / "annotator" / "examples" / "annotation.preview.json"
RESULT_PATH = REPO_ROOT / "contract" / "annotation.v1.example.json"
COLLECTIONS = ("walls", "doors", "areas")
SHARED = ("format_version", "image", "grid")


def by_id(document: dict, collection: str) -> dict[str, dict]:
    """Элементы коллекции по `id`: сравнение идёт по номеру, а не по порядку в файле."""

    return {element["id"]: element for element in document.get(collection, [])}


def geometry(element: dict) -> dict:
    """Элемент без числа уверенности — то, что обязано совпасть с результатом."""

    return {key: value for key, value in element.items() if key != "confidence"}


def without_confidence(preview: dict) -> dict:
    """Предварительный файл в том виде, в каком его принял бы сервер."""

    stripped = copy.deepcopy(preview)
    for collection in COLLECTIONS:
        stripped[collection] = [geometry(element) for element in stripped.get(collection, [])]
    return stripped


def check_confidence(preview: dict) -> list[str]:
    problems = []
    for collection in COLLECTIONS:
        for element in preview.get(collection, []):
            where = f"{collection}/{element.get('id')}"
            value = element.get("confidence")
            if value is None:
                problems.append(f"{where}: нет confidence — клиенту нечем подсветить элемент")
            elif isinstance(value, bool) or not isinstance(value, (int, float)):
                problems.append(f"{where}: confidence {value!r} — не число")
            elif not 0 <= value <= 1:
                problems.append(f"{where}: confidence {value} вне 0…1 — это уже не уверенность")
    return problems


def check_contract(stripped: dict) -> list[str]:
    verdict = contract.check(stripped)
    if verdict.verdict == "ok":
        return []
    return [f"без confidence файл не проходит контракт v1: {verdict.code} — {verdict.message}"]


def check_subset(preview: dict, result: dict) -> list[str]:
    problems = []
    for collection in COLLECTIONS:
        in_preview = by_id(preview, collection)
        for element_id, element in by_id(result, collection).items():
            found = in_preview.get(element_id)
            if found is None:
                problems.append(
                    f"{collection}/{element_id}: есть в результате, нет в предварительном файле — "
                    "клиенту нечего подсветить"
                )
            elif geometry(found) != element:
                problems.append(
                    f"{collection}/{element_id}: в предварительном файле другой элемент — "
                    "подсветка встанет не туда"
                )
    return problems


def check_shared(preview: dict, result: dict) -> list[str]:
    return [
        f"{field}: у файлов разные значения — значит, они сделаны не одним прогоном"
        for field in SHARED
        if preview.get(field) != result.get(field)
    ]


def check_cut(preview: dict, result: dict) -> list[str]:
    """Порог обязан что-то отрезать: без этого пример не показывает разницу двух файлов."""

    extra = []
    for collection in COLLECTIONS:
        in_result = by_id(result, collection)
        extra += [
            f"{collection}/{element_id}"
            for element_id in by_id(preview, collection)
            if element_id not in in_result
        ]
    if extra:
        return []
    return ["порог ничего не отрезал: предварительный файл совпадает с результатом"]


def main() -> int:
    try:
        preview = contract.load_json(PREVIEW_PATH)
        result = contract.load_json(RESULT_PATH)
    except FileNotFoundError as error:
        print(f"нет файла: {error.filename}", file=sys.stderr)
        return 2
    except (contract.NonFiniteNumber, json.JSONDecodeError) as error:
        print(f"файл не разбирается: {error}", file=sys.stderr)
        return 2

    checks = (
        ("confidence у каждого элемента — число 0…1", check_confidence(preview)),
        ("без confidence файл проходит контракт v1", check_contract(without_confidence(preview))),
        ("элементы результата есть в файле с той же геометрией", check_subset(preview, result)),
        ("format_version, image и grid совпадают", check_shared(preview, result)),
        ("порог что-то отрезал", check_cut(preview, result)),
    )

    failures = 0
    for name, problems in checks:
        print(f"{'ok  ' if not problems else 'FAIL'} {name}")
        for problem in problems:
            print(f"     {problem}")
        failures += len(problems)

    print(f"проверок: {len(checks)}, расхождений: {failures}")
    return 0 if failures == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
