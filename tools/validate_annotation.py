#!/usr/bin/env python3
"""Проверка файла разметки карты: схема плюс смысловые правила.

Зачем. Разметка карты — недоверенный вход (D11): её присылает сервер, воркер или
чужой десктопный клиент. Проверяем дважды: структурой
(schema/annotation.v1.schema.json) и смыслом — то, что схемой не выразить:
точки внутри картинки, уникальность id, ссылки на стены, стирание только
в производной копии.

Запуск:
    python3 tools/validate_annotation.py                      # пример из schema/ и все тесты
    python3 tools/validate_annotation.py schema/annotation.v1.example.json
    python3 tools/validate_annotation.py tests/               # все annotation.json в папке
    python3 tools/validate_annotation.py --self-test          # проверить сам валидатор

Код возврата: 0 — сошлось, 1 — есть ошибки, 2 — файл не прочитать.

Схема использует только эти ключевые слова JSON Schema, и валидатор понимает
именно их (SUPPORTED): type, const, enum, required, properties,
additionalProperties, items, minItems, maxItems, minimum, maximum,
exclusiveMinimum, exclusiveMaximum, minLength, maxLength, pattern, oneOf,
allOf, if/then/else, not, $ref. Появится в схеме слово вне списка — валидатор
скажет об этом вслух, а не пропустит проверку молча.
"""

import copy
import json
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCHEMA_PATH = ROOT / "schema" / "annotation.v1.schema.json"
EXAMPLE_PATH = ROOT / "schema" / "annotation.v1.example.json"

SUPPORTED = frozenset({
    "$schema", "$id", "$defs", "title", "description",
    "type", "const", "enum", "required", "properties", "additionalProperties",
    "items", "minItems", "maxItems", "minimum", "maximum", "exclusiveMinimum",
    "exclusiveMaximum", "minLength", "maxLength", "pattern",
    "oneOf", "allOf", "if", "then", "else", "not", "$ref",
})


class UnsupportedKeyword(Exception):
    """Схема использует слово, которого валидатор не знает."""


def _kind(value):
    if isinstance(value, bool):
        return "boolean"
    if isinstance(value, int):
        return "integer"
    if isinstance(value, float):
        return "number"
    if isinstance(value, str):
        return "string"
    if isinstance(value, list):
        return "array"
    if isinstance(value, dict):
        return "object"
    return "null" if value is None else type(value).__name__


def _type_matches(value, name):
    if name == "number":
        return isinstance(value, (int, float)) and not isinstance(value, bool)
    return _kind(value) == name


def _resolve(ref, root):
    if not ref.startswith("#/"):
        raise UnsupportedKeyword("внешняя ссылка " + ref)
    node = root
    for part in ref[2:].split("/"):
        node = node[part.replace("~1", "/").replace("~0", "~")]
    return node


def _short(value):
    text = json.dumps(value, ensure_ascii=False)
    return text if len(text) <= 60 else text[:57] + "..."


def _join(path, name):
    return name if not path else path + "." + name


def _label(schema):
    if "$ref" in schema:
        return schema["$ref"].rsplit("/", 1)[-1]
    if "type" in schema and isinstance(schema["type"], str):
        return schema["type"]
    return "вариант"


def validate(value, schema, path, root):
    """Вернуть список ошибок структуры. path — путь до узла для сообщений."""
    if "$ref" in schema:
        return validate(value, _resolve(schema["$ref"], root), path, root)
    for key in schema:
        if key not in SUPPORTED:
            raise UnsupportedKeyword(key)

    errors = []
    where = path or "$"

    if "type" in schema:
        expected = schema["type"]
        names = expected if isinstance(expected, list) else [expected]
        if not any(_type_matches(value, n) for n in names):
            return ["%s: ожидался %s, пришёл %s (%s)"
                    % (where, "/".join(names), _kind(value), _short(value))]

    if "const" in schema and value != schema["const"]:
        errors.append("%s: допустимо только %s, пришло %s"
                      % (where, _short(schema["const"]), _short(value)))
    if "enum" in schema and value not in schema["enum"]:
        allowed = ", ".join(json.dumps(v, ensure_ascii=False) for v in schema["enum"])
        errors.append("%s: %s не из списка (%s)" % (where, _short(value), allowed))

    kind = _kind(value)
    if kind in ("integer", "number"):
        if "minimum" in schema and value < schema["minimum"]:
            errors.append("%s: %s меньше минимума %s" % (where, value, schema["minimum"]))
        if "maximum" in schema and value > schema["maximum"]:
            errors.append("%s: %s больше максимума %s" % (where, value, schema["maximum"]))
        if "exclusiveMinimum" in schema and value <= schema["exclusiveMinimum"]:
            errors.append("%s: %s должно быть больше %s"
                          % (where, value, schema["exclusiveMinimum"]))
        if "exclusiveMaximum" in schema and value >= schema["exclusiveMaximum"]:
            errors.append("%s: %s должно быть меньше %s"
                          % (where, value, schema["exclusiveMaximum"]))

    if kind == "string":
        if "minLength" in schema and len(value) < schema["minLength"]:
            errors.append("%s: строка короче %d символов" % (where, schema["minLength"]))
        if "maxLength" in schema and len(value) > schema["maxLength"]:
            errors.append("%s: строка длиннее %d символов" % (where, schema["maxLength"]))
        if "pattern" in schema and re.search(schema["pattern"], value) is None:
            errors.append("%s: %s не подходит под шаблон %s"
                          % (where, _short(value), schema["pattern"]))

    if kind == "array":
        if "minItems" in schema and len(value) < schema["minItems"]:
            errors.append("%s: элементов %d, нужно не меньше %d"
                          % (where, len(value), schema["minItems"]))
        if "maxItems" in schema and len(value) > schema["maxItems"]:
            errors.append("%s: элементов %d, а больше %d не бывает"
                          % (where, len(value), schema["maxItems"]))
        if "items" in schema:
            for i, item in enumerate(value):
                errors += validate(item, schema["items"], "%s[%d]" % (where, i), root)

    if kind == "object":
        for name in schema.get("required", []):
            if name not in value:
                errors.append("%s: нет обязательного поля %r" % (where, name))
        props = schema.get("properties", {})
        for name, sub in props.items():
            if name in value:
                errors += validate(value[name], sub, _join(path, name), root)
        extra = schema.get("additionalProperties", True)
        if extra is False:
            for name in value:
                if name not in props:
                    errors.append("%s: лишнее поле %r — схема его не знает" % (where, name))
        elif isinstance(extra, dict):
            for name, sub in value.items():
                if name not in props:
                    errors += validate(sub, extra, _join(path, name), root)

    for sub in schema.get("allOf", []):
        errors += validate(value, sub, path, root)

    if "oneOf" in schema:
        matched, details = 0, []
        for sub in schema["oneOf"]:
            errs = validate(value, sub, path, root)
            if errs:
                details.append("%s: %s" % (_label(sub), errs[0]))
            else:
                matched += 1
        if matched == 0:
            errors.append("%s: не подходит ни под один вариант (%s)"
                          % (where, "; ".join(details)))
        elif matched > 1:
            errors.append("%s: подходит сразу под %d вариантов, нужен ровно один"
                          % (where, matched))

    if "not" in schema:
        if not validate(value, schema["not"], path, root):
            errors.append("%s: сработал запрет not" % where)

    if "if" in schema:
        holds = not validate(value, schema["if"], path, root)
        branch = "then" if holds else "else"
        if branch in schema:
            errors += validate(value, schema[branch], path, root)

    return errors



def _polygon_area2(poly):
    total = 0
    n = len(poly)
    for i in range(n):
        x1, y1 = poly[i]["x"], poly[i]["y"]
        x2, y2 = poly[(i + 1) % n]["x"], poly[(i + 1) % n]["y"]
        total += x1 * y2 - x2 * y1
    return total


def _orientation(a, b, c):
    value = (b[0] - a[0]) * (c[1] - a[1]) - (b[1] - a[1]) * (c[0] - a[0])
    return (value > 0) - (value < 0)


def _crosses(a, b, c, d):
    """Пересекаются ли отрезки ab и cd насквозь; касания не считаем."""
    return (_orientation(a, b, c) * _orientation(a, b, d) < 0
            and _orientation(c, d, a) * _orientation(c, d, b) < 0)


def check_polygon(poly, where):
    errors = []
    n = len(poly)
    for i in range(n):
        if poly[i] == poly[(i + 1) % n]:
            errors.append("%s: вершины %d и %d совпадают — в контуре дырка"
                          % (where, i, (i + 1) % n))
    area = abs(_polygon_area2(poly)) / 2.0
    if area < 1.0:
        errors.append("%s: площадь %.1f px² — это линия, а не область" % (where, area))
    points = [(p["x"], p["y"]) for p in poly]
    for i in range(n):
        for j in range(i + 1, n):
            if j == i + 1 or (i == 0 and j == n - 1):
                continue
            if _crosses(points[i], points[(i + 1) % n], points[j], points[(j + 1) % n]):
                errors.append("%s: контур сам себя пересекает (рёбра %d и %d)"
                              % (where, i, j))
                return errors
    return errors


def check_semantics(doc):
    """Правила, которые схемой не выразить. Звать только после validate."""
    errors = []
    image = doc["image"]
    width, height = image["width"], image["height"]
    grid = doc["grid"]

    if ("unit" in grid) != ("units_per_cell" in grid):
        errors.append("grid: unit и units_per_cell задаются только вместе")
    if grid["mode"] == "none" and grid.get("rotation_deg", 0) != 0:
        errors.append("grid: поворот при mode=none ничего не значит")

    erasure = doc.get("erasure")
    has_original = "original_file" in image
    applied = bool(erasure and erasure["applied"])
    if applied:
        if not has_original:
            errors.append("image.original_file: после стирания надо помнить, из чего сделана копия")
        elif image["original_file"] == image["file"]:
            errors.append("image.file: стирание не меняет оригинал — нужна производная копия с другим именем")
        if erasure.get("derived_file") != image["file"]:
            errors.append("erasure.derived_file: %s, а считали по %s"
                          % (_short(erasure.get("derived_file")), _short(image["file"])))
        check = erasure.get("check", {})
        if check.get("lines_confirmed", 0) > check.get("lines_checked", 0):
            errors.append("erasure.check: подтверждено больше линий, чем проверено")
    elif has_original:
        errors.append("image.original_file: копия указана, а стирание не применялось")

    seen = {}
    for i, element in enumerate(doc["elements"]):
        eid = element["id"]
        if eid in seen:
            errors.append("elements[%d].id: %d уже занят elements[%d]"
                          % (i, eid, seen[eid]))
        else:
            seen[eid] = i
    walls = {e["id"] for e in doc["elements"] if e["type"] == "wall"}

    def inside(point, where):
        x, y = point["x"], point["y"]
        if not (0 <= x <= width and 0 <= y <= height):
            errors.append("%s: точка (%d, %d) вне картинки %dx%d"
                          % (where, x, y, width, height))

    for i, element in enumerate(doc["elements"]):
        base = "elements[%d]" % i
        for j, point in enumerate(element.get("points", [])):
            inside(point, "%s.points[%d]" % (base, j))
        span = element.get("span")
        if span is not None:
            for j, point in enumerate(span):
                inside(point, "%s.span[%d]" % (base, j))
            if span[0] == span[1]:
                errors.append("%s.span: дверь нулевой длины" % base)
        polygon = element.get("polygon")
        if polygon is not None:
            for j, point in enumerate(polygon):
                inside(point, "%s.polygon[%d]" % (base, j))
            errors += check_polygon(polygon, base + ".polygon")
        if (element["type"] == "door" and "wall_id" in element
                and element["wall_id"] not in walls):
            errors.append("%s.wall_id: стены с id=%d нет" % (base, element["wall_id"]))

    return errors


def check(doc, schema):
    errors = validate(doc, schema, "", schema)
    if errors:
        return errors
    return check_semantics(doc)


def _iter_keywords(schema, where="schema"):
    """Обойти схему и выдать (место, ключевое слово) — для проверки SUPPORTED.

    Нужно знать, где в схеме лежат вложенные схемы: иначе имя свойства «type»
    не отличить от ключевого слова type.
    """
    for key, value in schema.items():
        yield where + "." + key, key
        if key in ("properties", "$defs"):
            for name, sub in value.items():
                yield from _iter_keywords(sub, "%s.%s.%s" % (where, key, name))
        elif key in ("items", "not", "if", "then", "else"):
            yield from _iter_keywords(value, "%s.%s" % (where, key))
        elif key in ("allOf", "oneOf"):
            for i, sub in enumerate(value):
                yield from _iter_keywords(sub, "%s.%s[%d]" % (where, key, i))


def _erasure(derived, checked=10, confirmed=9):
    """Кусок документа «стирание печатной сетки» для самопроверки."""
    return {
        "applied": True,
        "of": "printed-grid",
        "changed_pixels": 1234,
        "derived_file": derived,
        "check": {"lines_checked": checked, "lines_confirmed": confirmed,
                  "confirmed_ratio": 0.9},
    }


def self_test():
    """Проверить сам валидатор: он должен ловить то, что обязан ловить."""
    problems = []
    schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    base = json.loads(EXAMPLE_PATH.read_text(encoding="utf-8"))

    for where, keyword in _iter_keywords(schema):
        if keyword not in SUPPORTED:
            problems.append("%s: схема использует %r, а валидатор его не знает"
                            % (where, keyword))
    if problems:
        return problems

    def case(name, mutate, needle):
        doc = copy.deepcopy(base)
        mutate(doc)
        errors = check(doc, schema)
        if not errors:
            problems.append("%s: ошибку не заметили" % name)
        elif not any(needle in e for e in errors):
            problems.append("%s: ждали %r, получили %r" % (name, needle, errors[0]))

    def case_ok(name, mutate):
        doc = copy.deepcopy(base)
        mutate(doc)
        errors = check(doc, schema)
        if errors:
            problems.append("%s: ругается на исправный документ — %s" % (name, errors[0]))

    def apply_erasure(doc, original, derived, checked=10, confirmed=9):
        if original is not None:
            doc["image"]["original_file"] = original
        doc["erasure"] = _erasure(derived=derived, checked=checked, confirmed=confirmed)

    def no_grid_but_rotated(doc):
        grid = doc["grid"]
        grid.pop("origin", None)
        grid.pop("square", None)
        grid.update({"mode": "none", "rotation_deg": 12})

    case_ok("эталонный пример", lambda d: None)
    case_ok("поворот сетки", lambda d: d["grid"].update({"rotation_deg": 12}))
    case_ok("стирание в копии", lambda d: apply_erasure(d, "crypt-00.png", "crypt-01.png"))
    case_ok("сетки нет", lambda d: d["grid"].clear() or d["grid"].update({"mode": "none"}))
    case_ok("элементов меньше", lambda d: d["elements"].pop())

    case("нет обязательного поля",
         lambda d: d.pop("grid"), "нет обязательного поля 'grid'")
    case("чужое поле",
         lambda d: d.update({"comment": "привет"}), "лишнее поле 'comment'")
    case("толщина стены в json",
         lambda d: d["elements"][0].update({"thickness_px": 6}),
         "лишнее поле 'thickness_px'")
    case("неизвестный режим сетки",
         lambda d: d["grid"].update({"mode": "diagonal"}), "не из списка")
    case("квадрат без стороны",
         lambda d: d["grid"].pop("square"), "нет обязательного поля 'square'")
    case("гекс без гекса",
         lambda d: d["grid"].update({"mode": "hex"}), "нет обязательного поля 'hex'")
    case("origin при mode=none",
         lambda d: d["grid"].update({"mode": "none"}), "сработал запрет not")
    case("поворот без сетки", no_grid_but_rotated, "ничего не значит")
    case("единицы без размера клетки",
         lambda d: d["grid"].pop("units_per_cell"), "задаются только вместе")
    case("два элемента с одним id",
         lambda d: d["elements"][1].update({"id": d["elements"][0]["id"]}), "уже занят")
    case("точка вне картинки",
         lambda d: d["elements"][0]["points"][0].update({"x": 5000}), "вне картинки")
    case("дверь на несуществующей стене",
         lambda d: d["elements"][4].update({"wall_id": 999}), "стены с id=999 нет")
    def zero_length_door(doc):
        span = doc["elements"][4]["span"]
        span[1] = dict(span[0])

    case("дверь нулевой длины", zero_length_door, "нулевой длины")
    case("auto без уверенности",
         lambda d: d["elements"][0].pop("confidence"), "нет обязательного поля 'confidence'")
    case("элемент непонятного типа",
         lambda d: d["elements"][0].update({"type": "column"}), "не подходит ни под один вариант")
    case("стирание без исходника",
         lambda d: apply_erasure(d, None, "crypt-01.png"), "надо помнить, из чего сделана копия")
    case("стирание поверх оригинала",
         lambda d: apply_erasure(d, "crypt-01.png", "crypt-01.png"), "нужна производная копия")
    case("стирание не в той копии",
         lambda d: apply_erasure(d, "crypt-00.png", "crypt-99.png"), "а считали по")
    case("подтверждено больше, чем проверено",
         lambda d: apply_erasure(d, "crypt-00.png", "crypt-01.png", 10, 11),
         "подтверждено больше линий")
    case("полигон-бабочка",
         lambda d: d["elements"][5].update({"polygon": [
             {"x": 100, "y": 100}, {"x": 310, "y": 260},
             {"x": 310, "y": 100}, {"x": 100, "y": 260}]}),
         "сам себя пересекает")
    return problems


def _targets(argv):
    """Что проверять: файлы и папки (в папке ищем annotation.json на любом уровне)."""
    paths = [Path(a) for a in argv] or [EXAMPLE_PATH, ROOT / "tests"]
    files, missing = [], []
    for path in paths:
        if path.is_dir():
            files += sorted(path.rglob("annotation.json"))
        elif path.is_file():
            files.append(path)
        else:
            missing.append(str(path))
    return files, missing


def main(argv):
    if "--self-test" in argv:
        problems = self_test()
        for problem in problems:
            print("НЕ ТАК: " + problem, file=sys.stderr)
        print("самопроверка: %s" % ("провалена" if problems else "пройдена"))
        return 1 if problems else 0

    files, missing = _targets([a for a in argv if not a.startswith("-")])
    for name in missing:
        print("%s: нет такого файла или папки" % name, file=sys.stderr)
    if missing:
        return 2
    try:
        schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        print("схема не читается: %s (%s)" % (SCHEMA_PATH, exc), file=sys.stderr)
        return 2
    if not files:
        print("нечего проверять: ни одного annotation.json не нашлось", file=sys.stderr)
        return 0

    failed = 0
    for path in files:
        try:
            doc = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, ValueError) as exc:
            print("%s: не читается (%s)" % (path, exc), file=sys.stderr)
            failed += 1
            continue
        try:
            errors = check(doc, schema)
        except UnsupportedKeyword as exc:
            print("%s: валидатор не знает ключевого слова %r"
                  % (path, str(exc)), file=sys.stderr)
            failed += 1
            continue
        for error in errors:
            print("%s: %s" % (path, error), file=sys.stderr)
        if errors:
            failed += 1

    print("проверено файлов: %d, с ошибками: %d" % (len(files), failed))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))

