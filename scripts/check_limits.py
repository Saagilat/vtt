#!/usr/bin/env python3
"""Пределы: «ровно столько — можно», «на один больше — отказ».

Случаи строятся в памяти: держать в репозитории файл на 20 001 стену ради одного отказа
незачем. Проверять предел всё равно надо — именно на нём два валидатора разойдутся, если
один из них забудет число: числа повторяются в FORMAT.md, в схеме и в коде, а сверяет их
этот прогон.

Использование: python3 scripts/check_limits.py
Код возврата: 0 — все пределы ведут себя как обещано, 1 — нет.
"""

from __future__ import annotations

import copy
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import validate_annotation as contract  # noqa: E402

EXAMPLE = contract.REPO_ROOT / "contract" / "annotation.v1.example.json"


def doors(count: int) -> list[dict]:
    return [
        {"id": f"d{index}", "wall_id": "w2", "from": [850, 800], "to": [920, 800], "type": "normal"}
        for index in range(1, count + 1)
    ]


def areas(count: int) -> list[dict]:
    polygon = [[1200, 900], [1500, 900], [1500, 1200], [1200, 1200], [1200, 900]]
    return [{"id": f"a{index}", "type": "water", "polygon": polygon} for index in range(1, count + 1)]


def set_image_side(side: int):
    def mutate(document: dict) -> None:
        document["image"]["width"] = side
        document["image"]["height"] = side

    return mutate


def set_walls(count: int):
    """Ровно `count` стен: базовые w1 и w2 остаются — на w2 ссылается дверь примера."""

    def mutate(document: dict) -> None:
        extra = [
            {"id": f"w{index}", "points": [[100, 100], [300, 100 + index % 500]]}
            for index in range(3, count + 1)
        ]
        document["walls"] = document["walls"] + extra

    return mutate


def set_doors(count: int):
    return lambda document: document.__setitem__("doors", doors(count))


def set_areas(count: int):
    return lambda document: document.__setitem__("areas", areas(count))


# Имя, правка, что сервер получил картинкой, ожидаемый вердикт, ожидаемый код.
CASES = [
    ("стен 20 000", set_walls(20000), None, "ok", None),
    ("стен 20 001", set_walls(20001), None, "error", "too_many_elements"),
    ("дверей 5 000", set_doors(5000), None, "ok", None),
    ("дверей 5 001", set_doors(5001), None, "error", "too_many_elements"),
    ("областей 2 000", set_areas(2000), None, "ok", None),
    ("областей 2 001", set_areas(2001), None, "error", "too_many_elements"),
    ("картинка 8192", set_image_side(8192), contract.ImageInfo(8192, 8192), "ok", None),
    ("картинка 8193", set_image_side(8193), None, "error", "image_too_large"),
]


def main() -> int:
    schema = contract.load_schema(contract.DEFAULT_SCHEMA)
    base = json.loads(EXAMPLE.read_text(encoding="utf-8"))
    failures = 0
    for name, mutate, image, expected_verdict, expected_code in CASES:
        document = copy.deepcopy(base)
        mutate(document)
        verdict = contract.check(document, image, schema)
        matched = verdict.verdict == expected_verdict and verdict.code == expected_code
        print(f"{'ok  ' if matched else 'FAIL'} {name} — {verdict.verdict}/{verdict.code or '—'}")
        if not matched:
            print(
                f"     ждали {expected_verdict}/{expected_code or '—'}: {verdict.message}"
            )
            failures += 1
    print(f"пределов проверено: {len(CASES)}, расхождений: {failures}")
    return 0 if failures == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
