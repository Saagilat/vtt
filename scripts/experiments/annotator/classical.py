#!/usr/bin/env python3
# Стенд разметчика: классический детектор стен — Canny + Хаф, без модели.
#
# Это baseline для эксперимента: он не претендует на качество, его задача — показать,
# где морфология справляется, а где нужна модель. Детектор возвращает annotation.json:
# сетка из grid.py, стены — отрезки Хафа, двери и области пустые.
#
# Подавление сетки: линии сетки заливаются inpaint'ом перед Canny. Это проверка решения
# «гасить сетку только во входе модели»: картинка и sha256 при этом не меняются.
#
# Использование:
#   python3 scripts/experiments/annotator/classical.py --image карта.png --out result.json

import argparse
import pathlib
import sys

import cv2
import numpy as np

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import grid  # noqa: E402

MIN_LENGTH = 30


def grid_from_detection(result):
    if not result['is_grid']:
        return {'mode': 'none'}
    side = round((result['vertical']['step'] + result['horizontal']['step']) / 2.0, 2)
    origin_x = int(round(result['vertical']['offset'] + side / 2.0))
    origin_y = int(round(result['horizontal']['offset'] + side / 2.0))
    width = result['image']['width']
    height = result['image']['height']
    while origin_x < 0:
        origin_x += int(round(side))
    while origin_y < 0:
        origin_y += int(round(side))
    while origin_x >= width:
        origin_x -= int(round(side))
    while origin_y >= height:
        origin_y -= int(round(side))
    return {'mode': 'square', 'origin': [origin_x, origin_y], 'square': {'side_px': side}}


def grid_mask(shape, result):
    height, width = shape
    mask = np.zeros((height, width), dtype=np.uint8)
    for det, axis in ((result['vertical'], 'v'), (result['horizontal'], 'h')):
        if det is None:
            continue
        thickness = int(det['width']) + 2
        for center in det['centers']:
            value = int(round(center))
            if axis == 'v':
                left, right = max(0, value - thickness // 2), min(width, value + thickness)
                mask[:, left:right] = 255
            else:
                top, bottom = max(0, value - thickness // 2), min(height, value + thickness)
                mask[top:bottom, :] = 255
    return mask


def merge_segments(segments, angle_tolerance=5.0, offset_tolerance=8.0, gap=25.0):
    groups = []
    for first, second, length in segments:
        if length <= 0:
            continue
        ux, uy = (second[0] - first[0]) / length, (second[1] - first[1]) / length
        normal = (-uy, ux)
        offset = normal[0] * first[0] + normal[1] * first[1]
        placed = False
        for group in groups:
            dot = max(-1.0, min(1.0, ux * group['u'][0] + uy * group['u'][1]))
            difference = float(np.degrees(np.arccos(dot)))
            difference = min(difference, 180.0 - difference)
            if difference <= angle_tolerance and abs(offset - group['offset']) <= offset_tolerance:
                group['segments'].append((first, second))
                placed = True
                break
        if not placed:
            groups.append({'u': (ux, uy), 'normal': normal, 'offset': offset,
                           'segments': [(first, second)]})

    walls = []
    for group in groups:
        ux, uy = group['u']
        nx, ny = group['normal']
        intervals = []
        for first, second in group['segments']:
            t1 = ux * first[0] + uy * first[1]
            t2 = ux * second[0] + uy * second[1]
            intervals.append((min(t1, t2), max(t1, t2)))
        intervals.sort()
        merged = []
        for start, end in intervals:
            if merged and start <= merged[-1][1] + gap:
                merged[-1] = (merged[-1][0], max(merged[-1][1], end))
            else:
                merged.append((start, end))
        for start, end in merged:
            first = (ux * start + nx * group['offset'], uy * start + ny * group['offset'])
            second = (ux * end + nx * group['offset'], uy * end + ny * group['offset'])
            walls.append(((int(round(first[0])), int(round(first[1]))),
                          (int(round(second[0])), int(round(second[1])))))
    return walls


def detect(image_path, suppress_grid=True):
    image = cv2.imread(str(image_path))
    height, width = image.shape[:2]
    photo = grid.analyze(pathlib.Path(image_path))
    result = photo[1]

    gray = cv2.cvtColor(image, cv2.COLOR_BGR2GRAY)
    if suppress_grid:
        mask = grid_mask((height, width), result)
        if mask.any():
            gray = cv2.inpaint(gray, mask, 3, cv2.INPAINT_TELEA)
    gray = cv2.GaussianBlur(gray, (3, 3), 0)
    edges = cv2.Canny(gray, 30, 100)
    lines = cv2.HoughLinesP(edges, 1, np.pi / 180.0, threshold=45,
                            minLineLength=MIN_LENGTH, maxLineGap=5)

    segments = []
    if lines is not None:
        for x1, y1, x2, y2 in lines.reshape(-1, 4):
            x1, y1, x2, y2 = int(x1), int(y1), int(x2), int(y2)
            length = float(np.hypot(x2 - x1, y2 - y1))
            if length >= MIN_LENGTH:
                segments.append(((x1, y1), (x2, y2), length))
    segments.sort(key=lambda item: item[2], reverse=True)
    merged = merge_segments(segments[:2000])

    walls = []
    for index, (first, second) in enumerate(merged):
        first = (max(0, min(width - 1, first[0])), max(0, min(height - 1, first[1])))
        second = (max(0, min(width - 1, second[0])), max(0, min(height - 1, second[1])))
        if first == second:
            continue
        walls.append({'id': 'w%d' % (len(walls) + 1),
                      'points': [[first[0], first[1]], [second[0], second[1]]]})
    return {
        'format_version': 1,
        'image': {'width': width, 'height': height},
        'grid': grid_from_detection(result),
        'walls': walls,
        'doors': [],
        'areas': [],
    }


def main():
    parser = argparse.ArgumentParser(description='Стенд: классический детектор стен')
    parser.add_argument('--image', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--no-grid-suppression', action='store_true')
    args = parser.parse_args()

    import json
    document = detect(args.image, suppress_grid=not args.no_grid_suppression)
    pathlib.Path(args.out).write_text(json.dumps(document, ensure_ascii=False, indent=2) + chr(10),
                                      encoding='utf-8')
    print('стен: ' + str(len(document['walls'])))
    return 0


if __name__ == '__main__':
    sys.exit(main())
