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


def annotation(width, height, grid):
    return {
        'format_version': 1,
        'image': {'width': width, 'height': height},
        'grid': grid,
        'walls': [],
        'doors': [],
        'areas': [],
    }


CASES = (
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
        if case['kind'] == 'square':
            layer = square_grid(WIDTH, HEIGHT, case['side'], case['origin'],
                                angle=case.get('angle', 0.0),
                                strength=case.get('strength', 60))
            image = paste_grid(image, layer)
        elif case['kind'] == 'hex':
            image = paste_grid(image, hex_grid(WIDTH, HEIGHT, case['radius'], case['origin']))

        case_dir = out_root / case['name']
        case_dir.mkdir(parents=True, exist_ok=True)
        image.save(case_dir / 'original.png')
        expected = annotation(WIDTH, HEIGHT, case['grid'])
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

