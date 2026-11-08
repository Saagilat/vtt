#!/usr/bin/env python3
# Стенд разметчика: синтетические карты с точным эталоном.
#
# Рисуем карту сами, поэтому заранее знаем и сетку, и её отсутствие. Каждая фикстура — папка:
# original.png (карта) и expected.json (валидная разметка по контракту v1).
# В case.json лежит подсказка для стенда: умеет ли текущий прототип такую сетку находить.
#
# Использование:
#   python3 scripts/experiments/annotator/fixtures.py

import argparse
import json
import math
import pathlib

import numpy as np
from PIL import Image, ImageDraw

WIDTH = 2048
HEIGHT = 1536


def ground(width, height, seed=1):
    rng = np.random.default_rng(seed)
    small = rng.normal(0.0, 1.0, (24, 32))
    low = Image.fromarray(((small - small.min()) / (small.max() - small.min()) * 255).astype(np.uint8))
    low = low.resize((width, height), Image.BICUBIC)
    field = np.asarray(low, dtype=np.float32) / 255.0
    green = np.stack([
        70.0 + 30.0 * field,
        95.0 + 35.0 * field,
        55.0 + 20.0 * field,
    ], axis=2)
    noise = rng.normal(0.0, 3.0, (height, width, 1))
    return np.clip(green + noise, 0, 255).astype(np.uint8)


def square_grid(width, height, side, origin, angle=0.0, strength=60):
    pad = int(max(width, height))
    layer = Image.new('RGBA', (width + 2 * pad, height + 2 * pad), (0, 0, 0, 0))
    draw = ImageDraw.Draw(layer)
    phase_x = origin[0] - side / 2.0
    phase_y = origin[1] - side / 2.0
    start = -int((width + pad) // side) - 1
    stop = int((width + 2 * pad) // side) + 2
    for k in range(start, stop):
        x = phase_x + k * side + pad
        draw.line([(x, 0), (x, height + 2 * pad)], fill=(30, 30, 30, strength), width=2)
    start = -int((height + pad) // side) - 1
    stop = int((height + 2 * pad) // side) + 2
    for k in range(start, stop):
        y = phase_y + k * side + pad
        draw.line([(0, y), (width + 2 * pad, y)], fill=(30, 30, 30, strength), width=2)
    if angle:
        layer = layer.rotate(-angle, center=(pad + width / 2.0, pad + height / 2.0),
                             resample=Image.BICUBIC)
    return layer.crop((pad, pad, pad + width, pad + height))


def hex_grid(width, height, radius, origin):
    layer = Image.new('RGBA', (width, height), (0, 0, 0, 0))
    draw = ImageDraw.Draw(layer)
    step_x = radius * 1.5
    step_y = radius * math.sqrt(3.0)
    for row in range(-1, int(height / step_y) + 2):
        for col in range(-1, int(width / step_x) + 2):
            cx = origin[0] + col * step_x
            cy = origin[1] + row * step_y + (0 if col % 2 == 0 else step_y / 2.0)
            points = []
            for i in range(6):
                angle = math.radians(60 * i)
                points.append((cx + radius * math.cos(angle), cy + radius * math.sin(angle)))
            draw.polygon(points, outline=(30, 30, 30, 55), width=2)
    return layer


def paste_grid(base, layer):
    image = base.convert('RGBA')
    image.alpha_composite(layer)
    return image.convert('RGB')


def draw_areas(image, areas):
    layer = image.convert('RGBA')
    draw = ImageDraw.Draw(layer)
    colors = {'water': (60, 120, 200, 70), 'lava': (220, 80, 40, 80),
              'difficult': (110, 80, 40, 60), 'unknown': (120, 120, 120, 60)}
    for area in areas:
        points = [tuple(point) for point in area['polygon']]
        draw.polygon(points, fill=colors.get(area['type'], colors['unknown']),
                     outline=(40, 40, 40, 160), width=3)
    return layer.convert('RGB')


def draw_walls(image, walls, width=6):
    layer = image.convert('RGBA')
    draw = ImageDraw.Draw(layer)
    for wall in walls:
        points = [tuple(point) for point in wall['points']]
        draw.line(points, fill=(35, 28, 22, 230), width=width, joint='curve')
        for point in points:
            draw.ellipse([point[0] - width // 2, point[1] - width // 2,
                          point[0] + width // 2, point[1] + width // 2],
                         fill=(35, 28, 22, 230))
    return layer.convert('RGB')


def annotation(width, height, grid, walls=None, doors=None, areas=None):
    return {
        'format_version': 1,
        'image': {'width': width, 'height': height},
        'grid': grid,
        'walls': walls or [],
        'doors': doors or [],
        'areas': areas or [],
    }


ROOM_WALLS = [
    {'id': 'w1', 'points': [[100, 100], [1900, 100], [1900, 1400], [100, 1400], [100, 100]]},
    {'id': 'w2', 'points': [[735, 100], [735, 800]]},
    {'id': 'w3', 'points': [[735, 800], [1435, 800]]},
    {'id': 'w4', 'points': [[1435, 800], [1435, 1400]]},
]
ROOM_DOORS = [
    {'id': 'd1', 'wall_id': 'w1', 'from': [600, 100], 'to': [670, 100], 'type': 'normal'},
    {'id': 'd2', 'wall_id': 'w2', 'from': [735, 400], 'to': [735, 470], 'type': 'secret'},
    {'id': 'd3', 'wall_id': 'w3', 'from': [1000, 800], 'to': [1070, 800], 'type': 'opening'},
]
ROOM_AREAS = [
    {'id': 'a1', 'type': 'water',
     'polygon': [[1200, 900], [1500, 900], [1500, 1200], [1200, 1200], [1200, 900]]},
    {'id': 'a2', 'type': 'difficult',
     'polygon': [[200, 950], [600, 950], [600, 1300], [200, 1300], [200, 950]]},
]
CAVE_WALLS = [
    {'id': 'w1', 'points': [[120, 200], [300, 140], [480, 220], [640, 160], [820, 260],
                            [980, 200], [1120, 300], [1300, 240], [1480, 330], [1650, 280],
                            [1820, 380]]},
    {'id': 'w2', 'points': [[120, 200], [160, 420], [120, 650], [200, 900], [150, 1150],
                            [250, 1350]]},
    {'id': 'w3', 'points': [[250, 1350], [500, 1300], [760, 1370], [1000, 1290], [1240, 1360],
                            [1480, 1300], [1700, 1360]]},
    {'id': 'w4', 'points': [[1820, 380], [1780, 620], [1840, 860], [1790, 1100], [1700, 1360]]},
]
CAVE_AREAS = [
    {'id': 'a1', 'type': 'lava',
     'polygon': [[900, 700], [1200, 650], [1400, 820], [1250, 1050], [950, 980], [900, 700]]},
]

CASES = (
    {
        'name': 'rooms-square',
        'prototype_supports_grid': True,
        'grid': {'mode': 'square', 'origin': [35, 35], 'square': {'side_px': 70}},
        'kind': 'square',
        'side': 70,
        'origin': [35, 35],
        'strength': 55,
        'walls': ROOM_WALLS,
        'doors': ROOM_DOORS,
        'areas': ROOM_AREAS,
    },
    {
        'name': 'rooms-no-grid',
        'prototype_supports_grid': True,
        'grid': {'mode': 'none'},
        'kind': 'none',
        'walls': ROOM_WALLS,
        'doors': ROOM_DOORS,
        'areas': ROOM_AREAS,
    },
    {
        'name': 'cave',
        'prototype_supports_grid': True,
        'grid': {'mode': 'none'},
        'kind': 'none',
        'walls': CAVE_WALLS,
        'areas': CAVE_AREAS,
    },
    {
        'name': 'square-70',
        'prototype_supports_grid': True,
        'grid': {'mode': 'square', 'origin': [35, 35], 'square': {'side_px': 70}},
        'kind': 'square',
        'side': 70,
        'origin': [35, 35],
        'strength': 60,
    },
    {
        'name': 'square-70-dark',
        'prototype_supports_grid': True,
        'grid': {'mode': 'square', 'origin': [35, 35], 'square': {'side_px': 70}},
        'kind': 'square',
        'side': 70,
        'origin': [35, 35],
        'strength': 180,
    },
    {
        'name': 'square-100-faint',
        'prototype_supports_grid': True,
        'grid': {'mode': 'square', 'origin': [50, 50], 'square': {'side_px': 100}},
        'kind': 'square',
        'side': 100,
        'origin': [50, 50],
        'strength': 45,
    },
    {
        'name': 'no-grid',
        'prototype_supports_grid': True,
        'grid': {'mode': 'none'},
        'kind': 'none',
    },
    {
        'name': 'square-rotated-12',
        'prototype_supports_grid': False,
        'grid': {'mode': 'square', 'origin': [1024, 768], 'rotation_deg': 12,
                 'square': {'side_px': 70}},
        'kind': 'square',
        'side': 70,
        'origin': [1024, 768],
        'angle': 12,
        'strength': 60,
    },
    {
        'name': 'hex-flat',
        'prototype_supports_grid': False,
        'grid': {'mode': 'hex', 'origin': [120, 120],
                 'hex': {'radius_px': 40, 'orientation': 'flat', 'stagger': 'odd'}},
        'kind': 'hex',
        'radius': 40,
        'origin': [120, 120],
    },
)


def build(out_root):
    out_root.mkdir(parents=True, exist_ok=True)
    for case in CASES:
        image = Image.fromarray(ground(WIDTH, HEIGHT))
        if case.get('areas'):
            image = draw_areas(image, case['areas'])
        if case['kind'] == 'square':
            layer = square_grid(WIDTH, HEIGHT, case['side'], case['origin'],
                                angle=case.get('angle', 0.0),
                                strength=case.get('strength', 60))
            image = paste_grid(image, layer)
        elif case['kind'] == 'hex':
            image = paste_grid(image, hex_grid(WIDTH, HEIGHT, case['radius'], case['origin']))
        if case.get('walls'):
            image = draw_walls(image, case['walls'])

        case_dir = out_root / case['name']
        case_dir.mkdir(parents=True, exist_ok=True)
        image.save(case_dir / 'original.png')
        expected = annotation(WIDTH, HEIGHT, case['grid'], case.get('walls'),
                              case.get('doors'), case.get('areas'))
        (case_dir / 'expected.json').write_text(
            json.dumps(expected, ensure_ascii=False, indent=2) + chr(10), encoding='utf-8')
        meta = {
            'name': case['name'],
            'prototype_supports_grid': case['prototype_supports_grid'],
            'notes': case.get('notes', ''),
        }
        (case_dir / 'case.json').write_text(
            json.dumps(meta, ensure_ascii=False, indent=2) + chr(10), encoding='utf-8')
        print('сделано: ' + str(case_dir))
    return len(CASES)


def main():
    parser = argparse.ArgumentParser(description='Стенд: синтетические карты с эталоном')
    parser.add_argument('--out', default='scripts/experiments/annotator/cases')
    args = parser.parse_args()
    count = build(pathlib.Path(args.out))
    print('фикстур: ' + str(count))
    return 0


if __name__ == '__main__':
    raise SystemExit(main())

