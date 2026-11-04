#!/usr/bin/env python3
"""Три самодельные карты для эксперимента: original.jpg + annotation.json + overlay.png.

Зачем. На настоящих картах эталона нет, поэтому «сколько стен нашли из всех»
не посчитать. Синтетика закрывает ровно это: карту рисует скрипт, он же знает,
где провёл стены. Картинка и разметка растут из одного описания, поэтому
разойтись не могут.

Что получается в каждой папке tests/NN-...:
    original.jpg     — карта как её увидит разметчик (с напечатанной сеткой)
    annotation.json  — эталон в формате vtt.annotation, проверен валидатором
    overlay.png      — та же карта с нарисованным эталоном (tools/render_overlay.py)

Запуск:
    python3 tools/make_synthetic.py                 # перерисовать все три в tests/
    python3 tools/make_synthetic.py --out /tmp/x    # в другую папку
    python3 tools/make_synthetic.py --only 02       # только одну

Нужны Pillow и numpy: это локальный инструмент, на сервер он не едет.
"""

import argparse
import hashlib
import json
import math
import random
import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageChops, ImageDraw

import render_overlay
import validate_annotation

ROOT = Path(__file__).resolve().parent.parent
SCHEMA_PATH = ROOT / "schema" / "annotation.v1.schema.json"

SIZE = 840
FLOOR = (126, 120, 106)
SHEET = (46, 43, 38)
PRINTED = (152, 146, 128)
WALL_DARK = (36, 32, 28)
WALL_TOP = (104, 96, 82)
WALL_WIDTH = 14
DOOR_SILL = (132, 124, 108)
WATER = (56, 100, 136)
WATER_LIGHT = (108, 152, 182)
LAVA = (156, 62, 34)
LAVA_LIGHT = (228, 142, 62)
RUBBLE = (98, 92, 80)


def snap(points):
    """Точки в целые пиксели — то, что уйдёт в json, и то, чем нарисуем."""
    return [(int(round(x)), int(round(y))) for x, y in points]


def as_json(points):
    return [{"x": x, "y": y} for x, y in snap(points)]


def mask_of(size, polygon):
    mask = Image.new("L", size, 0)
    ImageDraw.Draw(mask).polygon(polygon, fill=255)
    return mask


def axial_round(q, r):
    """Кубическое округление дробных аксиальных координат (docs/05 §5.1)."""
    x, z = q, r
    y = -x - z
    rx, ry, rz = round(x), round(y), round(z)
    dx, dy, dz = abs(rx - x), abs(ry - y), abs(rz - z)
    if dx > dy and dx > dz:
        rx = -ry - rz
    elif dy > dz:
        ry = -rx - rz
    else:
        rz = -rx - ry
    return (int(rx), int(rz))


def merge_chains(segments):
    """Склеить соседние рёбра в ломаные: стена — ломаная, а не 40 обрезков.

    Склеиваем через точку, где сходятся ровно два ребра (обычный поворот стены).
    Где сходятся три и больше — развилка; её оставляем разрывом, это разные стены.
    """
    edges = [(tuple(snap([a])[0]), tuple(snap([b])[0])) for a, b in segments]
    used = [False] * len(edges)
    degree = {}
    for a, b in edges:
        degree[a] = degree.get(a, 0) + 1
        degree[b] = degree.get(b, 0) + 1

    def another(point):
        for i, (a, b) in enumerate(edges):
            if used[i]:
                continue
            if a == point:
                return i, b
            if b == point:
                return i, a
        return None, None

    def walk(index):
        used[index] = True
        a, b = edges[index]
        poly = [a, b]
        while degree.get(poly[-1], 0) == 2:
            nxt, other = another(poly[-1])
            if nxt is None:
                break
            used[nxt] = True
            poly.append(other)
        while degree.get(poly[0], 0) == 2:
            nxt, other = another(poly[0])
            if nxt is None:
                break
            used[nxt] = True
            poly.insert(0, other)
        return poly

    lines = []
    for i, (a, b) in enumerate(edges):
        if not used[i] and (degree[a] != 2 or degree[b] != 2):
            lines.append(walk(i))
    for i in range(len(edges)):
        if not used[i]:          # остались только кольца: все точки степени 2
            lines.append(walk(i))
    return lines


class Sheet:
    """Холст карты: сетка задаёт поворот и начало координат, на нём же всё считаем."""

    def __init__(self, grid, seed=1, width=SIZE, height=SIZE):
        self.width, self.height = width, height
        self.grid = grid
        self.tf = render_overlay.Grid(grid)
        self.img = Image.new("RGB", (width, height), SHEET)
        self.rng = random.Random(seed)
        self.grain = np.random.default_rng(seed)

    def px(self, local_points):
        """Точки в локальных координатах сетки -> целые пиксели картинки."""
        return snap([self.tf.to_px(x, y) for x, y in local_points])

    def pixel(self, local_point):
        return self.px([local_point])[0]

    def cells(self, cell_points):
        """Точки в координатах клеток -> пиксели. Клетка (i, j) занимает i±0.5, j±0.5."""
        side = self.tf.side
        return self.px([(x * side, y * side) for x, y in cell_points])

    def cell(self, i, j):
        """Центр клетки (i, j) в пикселях картинки."""
        return self.cells([(i, j)])[0]


def paint_sheet(sheet, polygon_px, color):
    size = sheet.img.size
    sheet.img.paste(Image.new("RGB", size, color), (0, 0), mask_of(size, polygon_px))


def paint_cells(sheet, cells_px, spread=8):
    """Каждая клетка чуть другого оттенка: пол из плит, а не ровная заливка."""
    draw = ImageDraw.Draw(sheet.img)
    for cell in cells_px:
        shift = sheet.rng.randint(-spread, spread)
        color = tuple(max(0, min(255, channel + shift)) for channel in FLOOR)
        draw.polygon(cell, fill=color)


def paint_area(sheet, polygon_px, base, light, ripples=16):
    """Вода, лава, обломки: заливка, штрихи-блики внутри и тёмная кромка."""
    size = sheet.img.size
    area_mask = mask_of(size, polygon_px)
    sheet.img.paste(Image.new("RGB", size, base), (0, 0), area_mask)

    strokes = Image.new("L", size, 0)
    draw = ImageDraw.Draw(strokes)
    xs = [p[0] for p in polygon_px]
    ys = [p[1] for p in polygon_px]
    for _ in range(ripples):
        x = sheet.rng.uniform(min(xs), max(xs))
        y = sheet.rng.uniform(min(ys), max(ys))
        length = sheet.rng.uniform(16, 48)
        draw.line([(x, y), (x + length, y)], fill=130, width=2)
    sheet.img.paste(Image.new("RGB", size, light), (0, 0),
                    ImageChops.multiply(strokes, area_mask))

    edge = tuple(int(channel * 0.65) for channel in base)
    ImageDraw.Draw(sheet.img).line(polygon_px + [polygon_px[0]], fill=edge, width=2)


def paint_printed_grid(sheet, room_mask):
    """Напечатанная сетка: тонкие светлые линии, только там, где пол."""
    size = sheet.img.size
    lines = Image.new("L", size, 0)
    draw = ImageDraw.Draw(lines)
    tf = sheet.tf
    if tf.mode == "square" and tf.side > 0:
        for a, b in render_overlay.square_lines(tf, *size):
            draw.line([a, b], fill=255, width=2)
    elif tf.mode == "hex" and tf.radius > 0:
        for shape in render_overlay.hex_shapes(tf, *size):
            points = [(x, y) for x, y in shape]
            draw.line(points + [points[0]], fill=255, width=2)
    sheet.img.paste(Image.new("RGB", size, PRINTED), (0, 0),
                    ImageChops.multiply(lines, room_mask))


def paint_walls(sheet, walls_px):
    draw = ImageDraw.Draw(sheet.img)
    half = WALL_WIDTH / 2.0
    for points in walls_px:
        draw.line(points, fill=WALL_DARK, width=WALL_WIDTH, joint="curve")
        for x, y in (points[0], points[-1]):
            draw.ellipse([x - half, y - half, x + half, y + half], fill=WALL_DARK)
        draw.line([(x, y - 3) for x, y in points], fill=WALL_TOP, width=3, joint="curve")


def paint_doors(sheet, doors_px):
    draw = ImageDraw.Draw(sheet.img)
    for a, b in doors_px:
        draw.line([a, b], fill=DOOR_SILL, width=4)


def paint_prop(sheet, center_px, radius):
    x, y = center_px
    ImageDraw.Draw(sheet.img).ellipse([x - radius, y - radius, x + radius, y + radius],
                                      fill=RUBBLE, outline=WALL_DARK, width=2)


def finish(sheet, noise=5.0):
    """Зерно поверх всего: карта перестаёт быть векторной картинкой."""
    array = np.asarray(sheet.img).astype(np.float32)
    grain = sheet.grain.normal(0.0, noise, array.shape[:2])[:, :, None]
    return Image.fromarray(np.clip(array + grain, 0, 255).astype(np.uint8), "RGB")


def annotation_of(grid, walls_px, doors, areas, width=SIZE, height=SIZE):
    """Собрать эталон: эталонные элементы помечены origin=auto и уверенностью 1.0.

    Так честнее: файл отвечает на вопрос «что тут есть на самом деле», а не «кто
    это нарисовал». Для проверки алгоритма это ровно то, что нужно.
    """
    elements = []
    wall_ids = []
    nxt = 1
    for points in walls_px:
        elements.append({"id": nxt, "type": "wall", "origin": "auto", "confidence": 1.0,
                         "points": as_json(points)})
        wall_ids.append(nxt)
        nxt += 1
    for a, b, ref, kind in doors:
        element = {"id": nxt, "type": "door", "origin": "auto", "confidence": 1.0,
                   "span": as_json([a, b]), "kind": kind,
                   "width_px": round(math.hypot(b[0] - a[0], b[1] - a[1]), 1)}
        if ref is not None:
            element["wall_id"] = wall_ids[ref]
        elements.append(element)
        nxt += 1
    for kind, polygon in areas:
        elements.append({"id": nxt, "type": "area", "origin": "auto", "confidence": 1.0,
                         "area_kind": kind, "polygon": as_json(polygon)})
        nxt += 1
    return {
        "format": "vtt.annotation",
        "version": 1,
        "image": {"file": "original.jpg", "width": width, "height": height},
        "grid": grid,
        "producer": {"kind": "auto", "engine": "make_synthetic", "engine_version": "0.1"},
        "erasure": {"applied": False},
        "elements": elements,
    }


def write_map(folder, sheet, doc, schema):
    """Сохранить картинку, эталон и overlay.png; эталон сначала проверяем валидатором."""
    folder.mkdir(parents=True, exist_ok=True)
    image_path = folder / doc["image"]["file"]
    finish(sheet).save(image_path, "JPEG", quality=88, optimize=True)
    doc["image"]["sha256"] = hashlib.sha256(image_path.read_bytes()).hexdigest()

    errors = validate_annotation.check(doc, schema)
    if errors:
        for error in errors:
            print("  НЕ ТАК: %s" % error, file=sys.stderr)
        raise SystemExit("эталон не прошёл проверку: %s" % folder)

    (folder / "annotation.json").write_text(
        json.dumps(doc, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    stats = render_overlay.render(doc, image_path, folder / "overlay.png")
    print("%-18s стены %d, двери %d, области %d, сетка: %s"
          % (folder.name, stats["walls"] + stats["walls_mid"] + stats["walls_human"],
             stats["doors"], sum(stats["areas"].values()), stats["grid"]))


def build_square(folder):
    """Квадратная сетка 70 px без поворота: комната 10x10 клеток, вода, обломки, две двери.

    Всё считается в клетках: origin — центр клетки (0,0), значит клетка (i, j) занимает
    от i-0.5 до i+0.5, а стены идут по границам клеток, где их и рисует сетка.
    """
    side = 70
    grid = {"mode": "square", "origin": {"x": 105, "y": 105}, "rotation_deg": 0,
            "square": {"side_px": side}, "unit": "ft", "units_per_cell": 5}
    sheet = Sheet(grid, seed=4)

    cells = []
    for i in range(10):
        for j in range(10):
            cells.append(sheet.cells([(i - 0.5, j - 0.5), (i + 0.5, j - 0.5),
                                      (i + 0.5, j + 0.5), (i - 0.5, j + 0.5)]))
    paint_cells(sheet, cells)

    room = sheet.cells([(-0.5, -0.5), (9.5, -0.5), (9.5, 9.5), (-0.5, 9.5)])
    water = sheet.cells([(-0.5, 4.5), (2.5, 4.5), (2.5, 7.5), (-0.5, 7.5)])
    rubble = sheet.cells([(4.5, 4.5), (6.5, 4.5), (6.5, 6.5), (4.5, 6.5)])
    paint_area(sheet, water, WATER, WATER_LIGHT, ripples=18)
    paint_area(sheet, rubble, RUBBLE, (128, 122, 108), ripples=26)
    paint_printed_grid(sheet, mask_of(sheet.img.size, room))

    walls = [sheet.cells(points) for points in [
        [(-0.5, -0.5), (9.5, -0.5)],        # 0 верх
        [(9.5, -0.5), (9.5, 9.5)],          # 1 право
        [(9.5, 9.5), (7.5, 9.5)],           # 2 низ, справа от двери
        [(6.5, 9.5), (-0.5, 9.5)],          # 3 низ, слева от двери
        [(-0.5, 9.5), (-0.5, -0.5)],        # 4 лево
        [(4.5, -0.5), (4.5, 3.5)],          # 5 перегородка сверху вниз
        [(4.5, 3.5), (6.5, 3.5)],           # 6 перегородка до двери
        [(7.5, 3.5), (9.5, 3.5)],           # 7 перегородка после двери
    ]]
    doors = [
        (sheet.cell(6.5, 9.5), sheet.cell(7.5, 9.5), 3, "common"),
        (sheet.cell(6.5, 3.5), sheet.cell(7.5, 3.5), 6, "opening"),
    ]
    paint_walls(sheet, walls)
    paint_doors(sheet, [(a, b) for a, b, _, _ in doors])
    paint_prop(sheet, sheet.cell(8, 8), 20)

    doc = annotation_of(grid, walls, doors,
                        [("water", water), ("difficult", rubble)])
    return sheet, doc


def build_hex(folder):
    """Гексы углом вверх, R=40: пещера из клеток, стены — рёбра между полом и камнем."""
    radius = 40
    rows = [(1, 4), (1, 5), (0, 5), (0, 4), (1, 4), (2, 3)]   # ряд r: q от и до
    floor = {(q, r) for r, (q0, q1) in enumerate(rows) for q in range(q0, q1 + 1)}
    floor -= {(2, 2), (3, 3)}        # два каменных столба внутри пещеры

    bare = {"mode": "hex", "origin": {"x": 0, "y": 0},
            "hex": {"radius_px": radius, "orientation": "pointy", "stagger": "odd"},
            "unit": "ft", "units_per_cell": 5}
    reference = render_overlay.Grid(bare)
    corners = {cell: reference.hex_corners(*cell) for cell in floor}
    xs = [p[0] for shape in corners.values() for p in shape]
    ys = [p[1] for shape in corners.values() for p in shape]
    grid = dict(bare)
    grid["origin"] = {"x": int(round(SIZE / 2 - (min(xs) + max(xs)) / 2)),
                      "y": int(round(SIZE / 2 - (min(ys) + max(ys)) / 2))}
    sheet = Sheet(grid, seed=5)

    cells = {cell: sheet.px(shape) for cell, shape in corners.items()}
    paint_cells(sheet, list(cells.values()))
    room_mask = Image.new("L", sheet.img.size, 0)
    room_draw = ImageDraw.Draw(room_mask)
    for shape in cells.values():
        room_draw.polygon(shape, fill=255)

    paint_area(sheet, cells[(4, 4)], WATER, WATER_LIGHT, ripples=12)
    paint_printed_grid(sheet, room_mask)

    edges = []
    for cell, shape in corners.items():
        center = reference.hex_axial_to_local(*cell)
        for k in range(6):
            a, b = shape[k], shape[(k + 1) % 6]
            outward = (center[0] + ((a[0] + b[0]) / 2 - center[0]) * 1.08,
                       center[1] + ((a[1] + b[1]) / 2 - center[1]) * 1.08)
            if axial_round(*reference.hex_local_to_axial(*outward)) not in floor:
                edges.append((sheet.pixel(a), sheet.pixel(b)))

    vertical = [edge for edge in edges if edge[0][0] == edge[1][0]]
    vertical.sort(key=lambda edge: (min(edge[0][0], edge[1][0]),
                                    abs((edge[0][1] + edge[1][1]) / 2 - SIZE / 2)))
    door_span = vertical[0]
    edges.remove(door_span)

    walls = merge_chains(edges)
    paint_walls(sheet, walls)
    paint_doors(sheet, [door_span])
    paint_prop(sheet, sheet.pixel(reference.hex_axial_to_local(2, 0)), 14)

    doc = annotation_of(grid, walls, [(door_span[0], door_span[1], None, "common")],
                        [("water", cells[(4, 4)])])
    return sheet, doc


def build_rotated(folder):
    """Та же сетка 70 px, но повёрнутая на 12°: карта, снятая со сканера криво."""
    grid = {"mode": "square", "origin": {"x": 420, "y": 420}, "rotation_deg": 12,
            "square": {"side_px": 70}, "unit": "ft", "units_per_cell": 5}
    sheet = Sheet(grid, seed=6)

    room = sheet.px([(-315, -315), (175, -315), (175, -35), (315, -35),
                     (315, 315), (-315, 315)])
    paint_sheet(sheet, room, FLOOR)
    lava = sheet.px([(-175, 35), (35, 35), (35, 315), (-175, 315)])
    paint_area(sheet, lava, LAVA, LAVA_LIGHT, ripples=20)
    paint_printed_grid(sheet, mask_of(sheet.img.size, room))

    walls = [sheet.px(points) for points in [
        [(-315, -315), (-35, -315)],     # 0 верх до двери
        [(35, -315), (175, -315)],       # 1 верх после двери
        [(175, -315), (175, -35)],       # 2 выступ
        [(175, -35), (315, -35)],        # 3 выступ
        [(315, -35), (315, 315)],        # 4 право
        [(315, 315), (175, 315)],        # 5 низ, справа от двери
        [(105, 315), (-315, 315)],       # 6 низ, слева от двери
        [(-315, 315), (-315, -315)],     # 7 лево
    ]]
    doors = [(sheet.pixel((-35, -315)), sheet.pixel((35, -315)), 0, "common"),
             (sheet.pixel((175, 315)), sheet.pixel((105, 315)), 6, "secret")]
    paint_walls(sheet, walls)
    paint_doors(sheet, [(a, b) for a, b, _, _ in doors])
    paint_prop(sheet, sheet.pixel((-245, -245)), 16)

    doc = annotation_of(grid, walls, doors, [("lava", lava)])
    return sheet, doc


def main(argv):
    parser = argparse.ArgumentParser(description="Нарисовать синтетические карты с эталоном")
    parser.add_argument("--out", default=str(ROOT / "tests"),
                        help="куда писать (по умолчанию tests/)")
    parser.add_argument("--only", help="только одна карта, по началу имени: 01, 02, 03")
    args = parser.parse_args(argv)

    schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
    builders = [
        ("01-square-70px", build_square),
        ("02-hex-pointy", build_hex),
        ("03-rotated-12deg", build_rotated),
    ]
    made = 0
    for name, builder in builders:
        if args.only and not name.startswith(args.only):
            continue
        folder = Path(args.out) / name
        sheet, doc = builder(folder)
        write_map(folder, sheet, doc, schema)
        made += 1
    if not made:
        print("под --only %s ничего не подошло" % args.only, file=sys.stderr)
        return 2
    print("готово: карт %d, папка %s" % (made, args.out))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))


