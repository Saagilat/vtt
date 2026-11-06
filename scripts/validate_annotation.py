#!/usr/bin/env python3
"""Проверка разметки карты по контракту v1 (contract/FORMAT.md).

Один вход — один вердикт: `{"verdict": "ok" | "error", "code": ..., "message": ...}`.
Сначала форма по схеме `contract/annotation.v1.schema.json`, потом смысл. Порядок не
случаен: пока не сошлась форма, говорить «стена за краем» бессмысленно.

Что проверяет только этот скрипт (схема такого выразить не может):
  * `id` не повторяются внутри коллекции;
  * у стены и у двери концы не совпадают, у стены и соседние точки тоже;
  * полигон замкнут: последняя пара повторяет первую;
  * координаты строго внутри картинки: `0 <= x < width`, `0 <= y < height`;
  * `wall_id` двери указывает на существующую стену;
  * дверь лежит на своей стене (концы и середина, допуск 2 пикселя);
  * размер и `sha256` в файле совпадают с тем, что сервер получил картинкой.

Коды отказов — закрытый список из contract/README.md.

Использование:
    python3 scripts/validate_annotation.py contract/annotation.v1.example.json
    python3 scripts/validate_annotation.py contract/annotation.v1.example.json \
        --image-meta path/to/image.json
    python3 scripts/validate_annotation.py --cases contract/cases

Код возврата: 0 — вердикт ok, 1 — отказ, 2 — беда с самим запуском (нет файла, нет схемы).
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from dataclasses import dataclass
from pathlib import Path

try:
    import jsonschema
except ModuleNotFoundError:  # без библиотеки схемы проверять нечего — говорим об этом прямо
    sys.exit("нужна библиотека схемы: pip install 'jsonschema==4.*'")

REPO_ROOT = Path(__file__).resolve().parent.parent
DEFAULT_SCHEMA = REPO_ROOT / "contract" / "annotation.v1.schema.json"

# Пределы повторяют FORMAT.md §12 и схему. Расхождение ловится набором случаев:
# «на один элемент больше предела» обязано падать у обоих валидаторов одинаково.
LIMITS = {"walls": 20000, "doors": 5000, "areas": 2000}
DOOR_WALL_TOLERANCE_PX = 2.0

# Что значит нарушенное слово схемы — по-русски, для текста отказа.
KEYWORD_HINTS = {
    "additionalProperties": "лишнее поле — такого в формате нет",
    "required": "не хватает обязательного поля",
    "type": "не тот тип значения",
    "enum": "значение не из списка",
    "const": "не то значение",
    "pattern": "строка не того вида",
    "minimum": "значение меньше допустимого",
    "maximum": "значение больше допустимого",
    "exclusiveMinimum": "значение должно быть строго больше допустимого",
    "exclusiveMaximum": "значение должно быть строго меньше допустимого",
    "minItems": "слишком мало элементов",
    "maxItems": "слишком много элементов",
    "prefixItems": "элемент пары не того типа",
    "items": "лишние элементы",
    "dependentRequired": "поле требует пары и без неё не значит ничего",
    "not": "поле запрещено в этом режиме",
    "anyOf": "не подходит ни один из вариантов",
    "allOf": "нарушено одно из условий",
}


@dataclass(frozen=True)
class ImageInfo:
    """Что сервер получил картинкой: размер после разбора и хеш принятых байт."""

    width: int
    height: int
    sha256: str | None = None


@dataclass(frozen=True)
class Verdict:
    verdict: str
    code: str | None
    message: str

    def as_dict(self) -> dict:
        return {"verdict": self.verdict, "code": self.code, "message": self.message}


def ok(message: str = "разметка соответствует контракту v1") -> Verdict:
    return Verdict("ok", None, message)


def fail(code: str, message: str) -> Verdict:
    return Verdict("error", code, message)


class NonFiniteNumber(ValueError):
    """В JSON приехало NaN или Infinity: такого в формате быть не может."""


def load_json(path: Path) -> dict:
    """Разбор без поблажек: NaN и Infinity — отказ, а не «число»."""

    def reject(token: str) -> float:
        raise NonFiniteNumber(token)

    text = path.read_text(encoding="utf-8")
    return json.loads(text, parse_constant=reject)


def load_schema(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def _format_path(path) -> str:
    parts = [str(part) for part in path]
    return "/" + "/".join(parts) if parts else "файл"


def _schema_code(error: jsonschema.ValidationError) -> str:
    """Код для нарушенного слова схемы: у пределов он свой, у остального — `schema`."""

    path = list(error.absolute_path)
    if path and path[0] in LIMITS and error.validator == "maxItems":
        return "too_many_elements"
    if path and path[0] == "image" and error.validator == "maximum":
        return "image_too_large"
    return "schema"


def check_schema(annotation: dict, schema: dict) -> Verdict:
    validator = jsonschema.Draft202012Validator(schema)
    errors = sorted(validator.iter_errors(annotation), key=lambda err: list(err.absolute_path))
    if not errors:
        return ok()
    error = errors[0]
    hint = KEYWORD_HINTS.get(error.validator, error.message)
    where = _format_path(error.absolute_path)
    return fail(_schema_code(error), f"{where}: {hint} ({error.message})")


def check_image(annotation: dict, image: ImageInfo | None) -> Verdict:
    """Размер и хеш в файле против того, что сервер получил картинкой.

    Размер — раньше геометрии: иначе стена за краем получит код «за край»,
    хотя настоящая причина в другой картинке.
    """

    if image is None:
        return ok()
    declared = annotation.get("image") or {}
    width, height = declared.get("width"), declared.get("height")
    if width != image.width or height != image.height:
        return fail(
            "image_size_mismatch",
            f"в файле {width}×{height}, а картинка {image.width}×{image.height}: "
            "координаты считались от другого размера, вся геометрия смещена",
        )
    declared_hash = declared.get("sha256")
    if declared_hash is not None and image.sha256 is not None and declared_hash != image.sha256:
        return fail(
            "image_sha256_mismatch",
            f"в файле sha256 {declared_hash}, у картинки {image.sha256}: "
            "разметку сделали по другой картинке того же размера, и это единственное место, где такое видно",
        )
    return ok()


def _distance_to_segment(px: float, py: float, ax: int, ay: int, bx: int, by: int) -> float:
    dx, dy = bx - ax, by - ay
    if dx == 0 and dy == 0:
        return math.hypot(px - ax, py - ay)
    t = ((px - ax) * dx + (py - ay) * dy) / (dx * dx + dy * dy)
    t = max(0.0, min(1.0, t))
    return math.hypot(px - (ax + t * dx), py - (ay + t * dy))


def _distance_to_wall(point: tuple[float, float], points: list[tuple[int, int]]) -> float:
    return min(
        _distance_to_segment(point[0], point[1], a[0], a[1], b[0], b[1])
        for a, b in zip(points, points[1:])
    )


def check_geometry(annotation: dict) -> Verdict:
    width = annotation["image"]["width"]
    height = annotation["image"]["height"]
    walls = annotation["walls"]
    doors = annotation["doors"]
    areas = annotation["areas"]

    for name, items in (("walls", walls), ("doors", doors), ("areas", areas)):
        seen: set[str] = set()
        for item in items:
            if item["id"] in seen:
                return fail(
                    "duplicate_id",
                    f"{name}: id {item['id']} повторяется — ссылка должна указывать на один элемент",
                )
            seen.add(item["id"])

    for wall in walls:
        points = [tuple(point) for point in wall["points"]]
        for first, second in zip(points, points[1:]):
            if first == second:
                return fail(
                    "zero_length_segment",
                    f"стена {wall['id']}: соседние точки {list(first)} совпадают — "
                    "такой участок ничего не ограничивает и портит нормали видимости",
                )
    for door in doors:
        if tuple(door["from"]) == tuple(door["to"]):
            return fail(
                "zero_length_segment",
                f"дверь {door['id']}: {door['from']} — начало и конец совпадают, проёма нулевой длины не бывает",
            )

    for area in areas:
        polygon = [tuple(point) for point in area["polygon"]]
        if polygon[0] != polygon[-1]:
            return fail(
                "polygon_not_closed",
                f"область {area['id']}: последняя пара {list(polygon[-1])} не повторяет первую "
                f"{list(polygon[0])} — незамкнутый полигон молча меняет форму",
            )

    def outside(point: tuple[float, float]) -> bool:
        return not (0 <= point[0] < width and 0 <= point[1] < height)

    def outside_verdict(where: str, point: tuple[float, float]) -> Verdict:
        return fail(
            "point_outside_image",
            f"{where}: точка {list(point)} вне картинки {width}×{height}; "
            f"допустимо x 0…{width - 1}, y 0…{height - 1}",
        )

    for wall in walls:
        for point in (tuple(point) for point in wall["points"]):
            if outside(point):
                return outside_verdict(f"стена {wall['id']}", point)
    for door in doors:
        for field in ("from", "to"):
            point = tuple(door[field])
            if outside(point):
                return outside_verdict(f"дверь {door['id']} ({field})", point)
    for area in areas:
        for point in (tuple(point) for point in area["polygon"]):
            if outside(point):
                return outside_verdict(f"область {area['id']}", point)

    walls_by_id = {wall["id"]: wall for wall in walls}
    for door in doors:
        wall_id = door.get("wall_id")
        if wall_id is not None and wall_id not in walls_by_id:
            return fail(
                "door_wall_missing",
                f"дверь {door['id']}: стены {wall_id} нет в файле — номер перепутан или взят из другой карты",
            )

    for door in doors:
        wall_id = door.get("wall_id")
        if wall_id is None:
            continue
        points = [tuple(point) for point in walls_by_id[wall_id]["points"]]
        start, end = tuple(door["from"]), tuple(door["to"])
        middle = ((start[0] + end[0]) / 2, (start[1] + end[1]) / 2)
        for name, point in (("from", start), ("to", end), ("середина", middle)):
            distance = _distance_to_wall(point, points)
            if distance > DOOR_WALL_TOLERANCE_PX:
                return fail(
                    "door_not_on_wall",
                    f"дверь {door['id']}: {name} {list(point)} отстоит от стены {wall_id} "
                    f"на {distance:.1f} px, а допуск {DOOR_WALL_TOLERANCE_PX:g} px — "
                    "такая дверь открывает проход мимо стены",
                )
    return ok()


def check(annotation: dict, image: ImageInfo | None = None, schema: dict | None = None) -> Verdict:
    """Полный вердикт в порядке FORMAT.md §12: форма, картинка, геометрия."""

    if schema is None:
        schema = load_schema(DEFAULT_SCHEMA)
    verdict = check_schema(annotation, schema)
    if verdict.verdict != "ok":
        return verdict
    verdict = check_image(annotation, image)
    if verdict.verdict != "ok":
        return verdict
    return check_geometry(annotation)


def check_file(path: Path, image_meta: Path | None = None, schema_path: Path = DEFAULT_SCHEMA) -> Verdict:
    try:
        annotation = load_json(path)
    except NonFiniteNumber:
        return fail(
            "non_finite_number",
            f"{path.name}: в файле NaN или Infinity — такое значение ломает любой расчёт",
        )
    except json.JSONDecodeError as error:
        return fail("not_json", f"{path.name}: это не JSON ({error.msg}, строка {error.lineno})")

    image = None
    if image_meta is not None:
        meta = load_json(image_meta)
        image = ImageInfo(width=meta["width"], height=meta["height"], sha256=meta.get("sha256"))
    return check(annotation, image, load_schema(schema_path))


def run_cases(root: Path, schema_path: Path) -> int:
    """Прогон набора: папка на случай, внутри annotation.json, expect.json и иногда image.json."""

    case_dirs = sorted(path for path in root.iterdir() if path.is_dir())
    if not case_dirs:
        print(f"в {root} нет ни одного случая", file=sys.stderr)
        return 2

    failures = 0
    for case_dir in case_dirs:
        annotation_path = case_dir / "annotation.json"
        expect_path = case_dir / "expect.json"
        if not annotation_path.exists() or not expect_path.exists():
            print(f"FAIL {case_dir.name}: нужны annotation.json и expect.json")
            failures += 1
            continue

        image_meta = case_dir / "image.json"
        verdict = check_file(
            annotation_path,
            image_meta if image_meta.exists() else None,
            schema_path,
        )
        expected = load_json(expect_path)
        expected_code = expected.get("code")
        matched = verdict.verdict == expected.get("verdict") and verdict.code == expected_code
        print(f"{'ok  ' if matched else 'FAIL'} {case_dir.name} — {verdict.verdict}/{verdict.code or '—'}")
        if not matched:
            print(f"     ждали {expected.get('verdict')}/{expected_code or '—'}: {verdict.message}")
            failures += 1

    print(f"случаев: {len(case_dirs)}, расхождений: {failures}")
    return 0 if failures == 0 else 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Проверка разметки карты по контракту v1")
    parser.add_argument("annotation", nargs="?", type=Path, help="файл annotation.json")
    parser.add_argument("--cases", type=Path, help="папка с набором случаев")
    parser.add_argument(
        "--image-meta",
        type=Path,
        help="что сервер получил картинкой: {\"width\": …, \"height\": …, \"sha256\": …}",
    )
    parser.add_argument(
        "--schema",
        type=Path,
        default=DEFAULT_SCHEMA,
        help="схема (по умолчанию contract/annotation.v1.schema.json)",
    )
    args = parser.parse_args(argv)

    try:
        if args.cases is not None:
            return run_cases(args.cases, args.schema)
        if args.annotation is None:
            parser.error("нужен файл разметки или --cases")
        verdict = check_file(args.annotation, args.image_meta, args.schema)
    except FileNotFoundError as error:
        print(f"нет файла: {error.filename}", file=sys.stderr)
        return 2

    print(json.dumps(verdict.as_dict(), ensure_ascii=False))
    return 0 if verdict.verdict == "ok" else 1


if __name__ == "__main__":
    sys.exit(main())
