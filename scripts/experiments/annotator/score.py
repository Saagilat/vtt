#!/usr/bin/env python3
# Стенд разметчика: сравнить результат с эталоном.
#
# Сетка сравнивается по режиму, стороне/радиусу и фазе линий (сдвиг по модулю шага).
# Стены — по покрытию: точка эталонной стены «найдена», если рядом (6 px) есть стена
# результата; обратное — ложная стена. Двери — по совпадению концов с тем же допуском.
# Области в первой версии только считаются, без IoU: фикстуры пока без них.
#
# Использование:
#   python3 scripts/experiments/annotator/score.py --result R.json --expected E.json

import argparse
import json
import math
import pathlib
import sys

TOLERANCE = 6.0
COVERAGE_FOUND = 0.7
COVERAGE_FALSE = 0.3


def load(path):
    return json.loads(pathlib.Path(path).read_text(encoding='utf-8'))


def distance(point, segment):
    (x, y), (x1, y1), (x2, y2) = point, segment[0], segment[1]
    dx, dy = x2 - x1, y2 - y1
    if dx == 0 and dy == 0:
        return math.hypot(x - x1, y - y1)
    t = max(0.0, min(1.0, ((x - x1) * dx + (y - y1) * dy) / (dx * dx + dy * dy)))
    return math.hypot(x - (x1 + t * dx), y - (y1 + t * dy))


def segments(wall):
    points = wall['points']
    return [(points[i], points[i + 1]) for i in range(len(points) - 1)]


def sample(wall, step=4.0):
    points = []
    for start, end in segments(wall):
        length = math.hypot(end[0] - start[0], end[1] - start[1])
        count = max(1, int(length / step))
        for i in range(count + 1):
            t = i / count
            points.append((start[0] + (end[0] - start[0]) * t, start[1] + (end[1] - start[1]) * t))
    return points


def coverage(wall, others):
    points = sample(wall)
    if not points:
        return 0.0
    near = 0
    for point in points:
        if any(distance(point, segment) <= TOLERANCE
               for other in others for segment in segments(other)):
            near += 1
    return near / len(points)


def wall_score(result, expected):
    found = sum(1 for wall in expected if coverage(wall, result) >= COVERAGE_FOUND)
    false = sum(1 for wall in result if coverage(wall, expected) < COVERAGE_FALSE)
    return {'expected': len(expected), 'found': found, 'false': false}


def door_score(result, expected):
    found = 0
    used = set()
    for door in expected:
        for index, candidate in enumerate(result):
            if index in used:
                continue
            if (math.dist(door['from'], candidate['from']) <= TOLERANCE
                    and math.dist(door['to'], candidate['to']) <= TOLERANCE):
                found += 1
                used.add(index)
                break
    return {'expected': len(expected), 'found': found, 'false': len(result) - len(used)}


def circular_difference(first, second, period):
    value = abs((first - second) % period)
    return min(value, period - value)


def phase(grid):
    side = grid['square']['side_px']
    origin = grid['origin']
    return ((origin[0] - side / 2.0) % side, (origin[1] - side / 2.0) % side)


def grid_score(result, expected):
    notes = []
    want = expected.get('mode', 'none')
    got = result.get('mode', 'none')
    if want == 'none':
        if got == 'none':
            return True, []
        return False, ['сетки нет, а разметчик её нашёл']
    if got != want:
        return False, ['режим ' + str(got) + ' вместо ' + str(want)]
    if want == 'square':
        side_want = expected['square']['side_px']
        side_got = result['square']['side_px']
        if abs(side_want - side_got) > 1.0:
            notes.append('сторона %.1f вместо %.1f' % (side_got, side_want))
        rotation_want = expected.get('rotation_deg', 0.0)
        rotation_got = result.get('rotation_deg', 0.0)
        if abs(rotation_want - rotation_got) > 1.0:
            notes.append('поворот %.1f вместо %.1f' % (rotation_got, rotation_want))
        want_x, want_y = phase(expected)
        got_x, got_y = phase(result)
        if circular_difference(got_x, want_x, side_want) > 2.0:
            notes.append('сдвиг по x %.1f вместо %.1f' % (got_x, want_x))
        if circular_difference(got_y, want_y, side_want) > 2.0:
            notes.append('сдвиг по y %.1f вместо %.1f' % (got_y, want_y))
    elif want == 'hex':
        for key in ('radius_px', 'orientation', 'stagger'):
            if result.get('hex', {}).get(key) != expected['hex'][key]:
                notes.append('hex.' + key + ' не совпал')
    return (not notes), notes


def main():
    parser = argparse.ArgumentParser(description='Стенд: сравнить разметку с эталоном')
    parser.add_argument('--result', required=True)
    parser.add_argument('--expected', required=True)
    parser.add_argument('--case-meta', default='', help='case.json с пометкой об ограничениях')
    args = parser.parse_args()

    result = load(args.result)
    expected = load(args.expected)
    meta = load(args.case_meta) if args.case_meta else {}
    known = not meta.get('prototype_supports_grid', True)

    grid_ok, grid_notes = grid_score(result.get('grid', {}), expected.get('grid', {}))
    report = {
        'grid': {'ok': grid_ok, 'notes': [note for note in grid_notes if note],
                 'known_limitation': known and not grid_ok},
        'walls': wall_score(result.get('walls', []), expected.get('walls', [])),
        'doors': door_score(result.get('doors', []), expected.get('doors', [])),
        'areas': {'expected': len(expected.get('areas', [])),
                  'found': len(result.get('areas', []))},
    }
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if grid_ok or report['grid']['known_limitation'] else 1


if __name__ == '__main__':
    sys.exit(main())
