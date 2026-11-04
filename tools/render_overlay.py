#!/usr/bin/env python3
"""Наложить разметку карты на картинку: original + annotation.json -> overlay.png.

Зачем. В эксперименте (docs/04) качество смотрят глазами: если находится мусор,
никакие цифры времени этого не оправдают. Скрипт рисует поверх исходной карты
то, что описано в annotation.json, по правилам docs/05: сетку, стены, двери,
области. Картинка — не источник истины, источник — json.

Запуск:
    python3 tools/render_overlay.py tests/01-square-70px/annotation.json
    python3 tools/render_overlay.py ann.json --image карта.jpg --out overlay.png
    python3 tools/render_overlay.py ann.json --no-legend --scale 2

Кладёт рядом с json файл overlay.png (или туда, куда сказали --out).

Координаты — пиксели картинки; origin сетки — центр клетки (0,0); поворот
по часовой стрелке (docs/05 §5). Нужен Pillow — это локальный инструмент,
на сервер он не едет.
"""

import argparse
import json
import math
import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

# Полосы уверенности из docs/05 §9: выше высокой — обычный вид, ниже низкой — не рисуем.
CONF_HIGH = 0.8
CONF_LOW = 0.5
SUPERSAMPLE = 3

WALL_HIGH = (52, 211, 96)
WALL_MID = (255, 196, 30)
HUMAN = (90, 200, 255)
DOOR = (255, 132, 0)
GRID = (255, 255, 255)
ORIGIN = (255, 70, 70)
HIDDEN = (120, 120, 120)

AREA_COLORS = {
    "water": ((60, 130, 220), "вода"),
    "lava": ((230, 70, 40), "лава"),
    "difficult": ((150, 140, 110), "тяжёлый рельеф"),
    "unknown": ((190, 190, 190), "непонятно, проверьте"),
}
AREA_ALPHA = 76          # 0.30 от 255 — docs/05 §8
GRID_ALPHA = 140
WALL_ALPHA = 235
# Стена — граница, а не полоса краски: в json толщины нет (docs/05 §6), это только вид.
WALL_WIDTH = 3.2         # в пикселях карты; жёлтая полоса рисуется на 0.8 толще

FONT_PATHS = (
    "/usr/share/fonts/TTF/DejaVuSans.ttf",
    "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
    "/usr/share/fonts/liberation/LiberationSans-Regular.ttf",
    "/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
)


def font(size):
    """Шрифт с кириллицей, если он есть в системе; иначе None (легенды не будет)."""
    for path in FONT_PATHS:
        if Path(path).is_file():
            try:
                return ImageFont.truetype(path, size)
            except OSError:
                continue
    return None


class Grid:
    """Пиксели <-> координаты сетки: origin — центр (0,0), поворот по часовой."""

    def __init__(self, grid):
        self.mode = grid["mode"]
        origin = grid.get("origin") or {"x": 0, "y": 0}
        self.ox = float(origin["x"])
        self.oy = float(origin["y"])
        angle = math.radians(float(grid.get("rotation_deg", 0.0)))
        self.cos = math.cos(angle)
        self.sin = math.sin(angle)
        square = grid.get("square") or {}
        hexa = grid.get("hex") or {}
        self.side = float(square.get("side_px", 0.0))
        self.radius = float(hexa.get("radius_px", 0.0))
        self.orientation = hexa.get("orientation", "pointy")

    def to_px(self, lx, ly):
        """Локальные пиксели (без поворота, от origin) -> пиксели картинки."""
        return (self.ox + lx * self.cos - ly * self.sin,
                self.oy + lx * self.sin + ly * self.cos)

    def to_local(self, x, y):
        """Пиксели картинки -> локальные пиксели (для оценки, сколько линий рисовать)."""
        dx, dy = x - self.ox, y - self.oy
        return (dx * self.cos + dy * self.sin, -dx * self.sin + dy * self.cos)

    def hex_axial_to_local(self, q, r):
        """docs/05 §5.1: pointy — x = R√3(q + r/2), y = 1.5Rr; flat — наоборот."""
        if self.orientation == "pointy":
            return (self.radius * math.sqrt(3.0) * (q + r / 2.0),
                    self.radius * 1.5 * r)
        return (self.radius * 1.5 * q,
                self.radius * math.sqrt(3.0) * (r + q / 2.0))

    def hex_local_to_axial(self, lx, ly):
        if self.radius <= 0:
            return (0.0, 0.0)
        if self.orientation == "pointy":
            r = ly / (self.radius * 1.5)
            return (lx / (self.radius * math.sqrt(3.0)) - r / 2.0, r)
        q = lx / (self.radius * 1.5)
        return (q, ly / (self.radius * math.sqrt(3.0)) - q / 2.0)

    def hex_corners(self, q, r):
        """Шесть вершин клетки в локальных пикселях; R — радиус описанной окружности."""
        cx, cy = self.hex_axial_to_local(q, r)
        start = -90.0 if self.orientation == "pointy" else 0.0
        corners = []
        for k in range(6):
            a = math.radians(start + 60.0 * k)
            corners.append((cx + self.radius * math.cos(a),
                            cy + self.radius * math.sin(a)))
        return corners


def square_lines(tf, width, height):
    """Линии квадратной сетки на всю картинку: диапазон считаем по её углам.

    `origin` — центр клетки (0,0) (docs/05 §5.1), поэтому линии идут по границам
    клеток, то есть со сдвигом на полшага от origin. Иначе сетка уезжает на полклетки
    от стен, которые стоят по границам.
    """
    if tf.side <= 0:
        return []
    corners = [(0, 0), (width - 1, 0), (0, height - 1), (width - 1, height - 1)]
    local = [tf.to_local(x, y) for x, y in corners]
    i_min = int(math.floor(min(p[0] for p in local) / tf.side)) - 1
    i_max = int(math.ceil(max(p[0] for p in local) / tf.side)) + 1
    j_min = int(math.floor(min(p[1] for p in local) / tf.side)) - 1
    j_max = int(math.ceil(max(p[1] for p in local) / tf.side)) + 1
    half = tf.side / 2.0
    lines = []
    for i in range(i_min, i_max + 1):
        x = i * tf.side + half
        lines.append((tf.to_px(x, j_min * tf.side), tf.to_px(x, j_max * tf.side)))
    for j in range(j_min, j_max + 1):
        y = j * tf.side + half
        lines.append((tf.to_px(i_min * tf.side, y), tf.to_px(i_max * tf.side, y)))
    return lines


def hex_shapes(tf, width, height):
    """Шестиугольники, попадающие на картинку: диапазон аксиальных координат по углам."""
    if tf.radius <= 0:
        return []
    corners = [(0, 0), (width - 1, 0), (0, height - 1), (width - 1, height - 1)]
    axial = [tf.hex_local_to_axial(*tf.to_local(x, y)) for x, y in corners]
    q_min = int(math.floor(min(a[0] for a in axial))) - 1
    q_max = int(math.ceil(max(a[0] for a in axial))) + 1
    r_min = int(math.floor(min(a[1] for a in axial))) - 1
    r_max = int(math.ceil(max(a[1] for a in axial))) + 1
    shapes = []
    for q in range(q_min, q_max + 1):
        for r in range(r_min, r_max + 1):
            shapes.append([tf.to_px(lx, ly) for lx, ly in tf.hex_corners(q, r)])
    return shapes


class Pen:
    """Рисование в увеличенном холсте: координаты в пикселях карты, толщины тоже."""

    def __init__(self, draw, scale):
        self.draw = draw
        self.scale = float(scale)

    def _xy(self, points):
        s = self.scale
        return [(float(x) * s, float(y) * s) for x, y in points]

    def line(self, points, color, width_px, alpha=255, caps=True):
        pts = self._xy(points)
        w = max(1, int(round(width_px * self.scale)))
        self.draw.line(pts, fill=color + (alpha,), width=w, joint="curve")
        if caps and len(pts) > 1:
            r = w / 2.0
            for x, y in (pts[0], pts[-1]):
                self.draw.ellipse([x - r, y - r, x + r, y + r], fill=color + (alpha,))

    def closed(self, points, color, width_px, alpha=255):
        pts = self._xy(points)
        self.draw.line(pts + [pts[0]], fill=color + (alpha,),
                       width=max(1, int(round(width_px * self.scale))), joint="curve")

    def fill(self, points, color, alpha):
        self.draw.polygon(self._xy(points), fill=color + (alpha,))

    def ring(self, center, radius_px, color, width_px, alpha=255):
        x, y = self._xy([center])[0]
        r = radius_px * self.scale
        self.draw.ellipse([x - r, y - r, x + r, y + r], outline=color + (alpha,),
                          width=max(1, int(round(width_px * self.scale))))


def pt(point):
    return (float(point["x"]), float(point["y"]))


def door_ticks(a, b, length):
    """Два перпендикулярных штриха по краям проёма — чтобы дверь читалась."""
    dx, dy = b[0] - a[0], b[1] - a[1]
    n = math.hypot(dx, dy) or 1.0
    ox, oy = -dy / n * length, dx / n * length
    return [((a[0] - ox, a[1] - oy), (a[0] + ox, a[1] + oy)),
            ((b[0] - ox, b[1] - oy), (b[0] + ox, b[1] + oy))]


def draw_annotation(pen, annotation, width, height):
    """Области -> сетка -> стены -> двери -> центр клетки. Возвращает счётчики."""
    stats = {"walls": 0, "walls_mid": 0, "walls_hidden": 0, "walls_human": 0,
             "doors": 0, "areas": {}, "grid": None}
    grid = annotation["grid"]
    tf = Grid(grid)

    for element in annotation["elements"]:
        if element["type"] != "area":
            continue
        kind = element.get("area_kind", "unknown")
        color, _ = AREA_COLORS.get(kind, AREA_COLORS["unknown"])
        pen.fill([pt(p) for p in element["polygon"]], color, AREA_ALPHA)
        pen.closed([pt(p) for p in element["polygon"]], color, 1.5)
        stats["areas"][kind] = stats["areas"].get(kind, 0) + 1

    if grid["mode"] == "square" and tf.side > 0:
        segments = square_lines(tf, width, height)
        for a, b in segments:
            pen.line([a, b], (0, 0, 0), 2.2, alpha=70, caps=False)
        for a, b in segments:
            pen.line([a, b], GRID, 1.0, alpha=GRID_ALPHA, caps=False)
        stats["grid"] = "квадрат %.0f px" % tf.side
    elif grid["mode"] == "hex" and tf.radius > 0:
        shapes = hex_shapes(tf, width, height)
        for shape in shapes:
            pen.closed(shape, (0, 0, 0), 2.2, alpha=70)
        for shape in shapes:
            pen.closed(shape, GRID, 1.0, alpha=GRID_ALPHA)
        name = "угол вверх" if tf.orientation == "pointy" else "грань вверх"
        stats["grid"] = "гекс, %s, R %.0f px" % (name, tf.radius)

    for element in annotation["elements"]:
        if element["type"] != "wall":
            continue
        points = [pt(p) for p in element["points"]]
        if element.get("origin") == "human":
            pen.line(points, HUMAN, WALL_WIDTH)
            stats["walls_human"] += 1
            continue
        confidence = float(element.get("confidence", 1.0))
        if confidence >= CONF_HIGH:
            pen.line(points, WALL_HIGH, WALL_WIDTH)
            stats["walls"] += 1
        elif confidence >= CONF_LOW:
            pen.line(points, WALL_MID, WALL_WIDTH + 0.8)
            stats["walls_mid"] += 1
        else:
            stats["walls_hidden"] += 1

    for element in annotation["elements"]:
        if element["type"] != "door":
            continue
        a, b = pt(element["span"][0]), pt(element["span"][1])
        pen.line([a, b], DOOR, 3.0)
        for p, q in door_ticks(a, b, 7.0):
            pen.line([p, q], DOOR, 2.0, caps=False)
        stats["doors"] += 1

    origin = grid.get("origin")
    if grid["mode"] != "none" and origin:
        o = pt(origin)
        pen.line([(o[0] - 8, o[1]), (o[0] + 8, o[1])], ORIGIN, 1.6, caps=False)
        pen.line([(o[0], o[1] - 8), (o[0], o[1] + 8)], ORIGIN, 1.6, caps=False)
        pen.ring(o, 5.0, ORIGIN, 1.6)
    return stats


def legend_items(stats):
    """Что показать в подписи: только то, что на картинке действительно есть."""
    items = []
    if stats["walls"]:
        items.append((WALL_HIGH, "стена"))
    if stats["walls_mid"]:
        items.append((WALL_MID, "стена, средняя уверенность"))
    if stats["walls_human"]:
        items.append((HUMAN, "правка руками"))
    if stats["walls_hidden"]:
        items.append((HIDDEN, "не рисуется: низкая уверенность, %d шт"
                      % stats["walls_hidden"]))
    if stats["doors"]:
        items.append((DOOR, "дверь"))
    for kind in ("water", "lava", "difficult", "unknown"):
        if stats["areas"].get(kind):
            items.append((AREA_COLORS[kind][0], AREA_COLORS[kind][1]))
    if stats["grid"]:
        items.append((GRID, "сетка"))
        items.append((ORIGIN, "центр клетки (0,0)"))
    return items


def legend_title(stats):
    walls = stats["walls"] + stats["walls_mid"] + stats["walls_human"]
    areas = sum(stats["areas"].values())
    return ("разметка поверх карты — стены %d, двери %d, области %d, сетка: %s"
            % (walls, stats["doors"], areas, stats["grid"] or "нет"))


def legend_strip(width, stats):
    """Полоса внизу картинки: заголовок и цветные плашки. Без шрифта — не рисуем её."""
    face = font(15)
    if face is None:
        return None
    pad, row = 12, 22
    items = legend_items(stats)
    rows, current, x = [], [], pad
    for color, label in items:
        need = 18 + 6 + face.getlength(label) + 18
        if current and x + need > width - pad:
            rows.append((current, x))
            current, x = [], pad
        current.append((color, label, x))
        x += need
    rows.append((current, x))

    strip = Image.new("RGBA", (width, pad * 2 + row * (len(rows) + 1) - 6), (22, 22, 26, 255))
    draw = ImageDraw.Draw(strip)
    draw.text((pad, pad - 4), legend_title(stats), font=face, fill=(226, 226, 232, 255))
    for index, (entries, _) in enumerate(rows):
        y = pad + row * (index + 1) - 2
        for color, label, x in entries:
            draw.rounded_rectangle([x, y + 3, x + 18, y + 17], radius=3,
                                   fill=color + (255,))
            draw.text((x + 24, y), label, font=face, fill=(226, 226, 232, 255))
    return strip


def render(annotation, image_path, out_path, legend=True, scale=SUPERSAMPLE):
    """Наложить разметку на карту и сохранить PNG. Возвращает счётчики."""
    if scale < 1:
        raise ValueError("--scale меньше 1 не бывает")
    with Image.open(image_path) as source:
        image = source.convert("RGBA")
    width, height = image.size
    want = (annotation["image"]["width"], annotation["image"]["height"])
    if (width, height) != want:
        raise ValueError("картинка %dx%d, а разметка снята с %dx%d — координаты не те"
                         % (width, height, want[0], want[1]))

    layer = Image.new("RGBA", (width * scale, height * scale), (0, 0, 0, 0))
    stats = draw_annotation(Pen(ImageDraw.Draw(layer), scale), annotation, width, height)
    layer = layer.resize((width, height), Image.LANCZOS)
    out = Image.alpha_composite(image, layer)

    strip = legend_strip(width, stats) if legend else None
    if strip is not None:
        canvas = Image.new("RGBA", (width, height + strip.height), (22, 22, 26, 255))
        canvas.paste(out, (0, 0))
        canvas.paste(strip, (0, height))
        out = canvas

    Path(out_path).parent.mkdir(parents=True, exist_ok=True)
    out.convert("RGB").save(out_path, "PNG")
    return stats


def main(argv):
    parser = argparse.ArgumentParser(description="Наложить разметку карты на картинку")
    parser.add_argument("annotation", help="annotation.json (формат vtt.annotation)")
    parser.add_argument("--image", help="карта; по умолчанию image.file рядом с json")
    parser.add_argument("--out", help="куда писать; по умолчанию overlay.png рядом с json")
    parser.add_argument("--no-legend", action="store_true", help="без полосы-легенды")
    parser.add_argument("--scale", type=int, default=SUPERSAMPLE,
                        help="во сколько раз крупнее холст для сглаживания (по умолчанию %d)"
                             % SUPERSAMPLE)
    args = parser.parse_args(argv)

    path = Path(args.annotation)
    try:
        annotation = json.loads(path.read_text(encoding="utf-8"))
        image_path = Path(args.image) if args.image else path.parent / annotation["image"]["file"]
        out_path = Path(args.out) if args.out else path.parent / "overlay.png"
        stats = render(annotation, image_path, out_path,
                       legend=not args.no_legend, scale=args.scale)
    except (OSError, ValueError, KeyError) as exc:
        print("%s: %s" % (path, exc), file=sys.stderr)
        return 2
    print(legend_title(stats))
    print("%s -> %s" % (image_path.name, out_path))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
