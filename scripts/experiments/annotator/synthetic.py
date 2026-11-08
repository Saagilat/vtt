#!/usr/bin/env python3
# Стенд разметчика: синтетические пары «карта — маска стен» для обучения.
#
# Генератор не пишет файлы: он отдаёт numpy-пары, чтобы обучение не упиралось в диск.
# Рисуем комнаты и пещеры, иногда сетку и области; целевая маска — только стены.
# Двери и области в обучение первой модели не входят: их ещё предстоит научиться находить.

import random

import numpy as np
from PIL import Image, ImageDraw

import fixtures


def wall_mask(size, walls, width):
    mask = Image.new('L', (size, size), 0)
    draw = ImageDraw.Draw(mask)
    for wall in walls:
        points = [tuple(point) for point in wall['points']]
        draw.line(points, fill=255, width=width, joint='curve')
        for point in points:
            draw.ellipse([point[0] - width // 2, point[1] - width // 2,
                          point[0] + width // 2, point[1] + width // 2], fill=255)
    return mask


def rooms(size, rng):
    margin = rng.randint(12, 36)
    walls = [[[margin, margin], [size - margin, margin], [size - margin, size - margin],
              [margin, size - margin], [margin, margin]]]
    for _ in range(rng.randint(1, 3)):
        if rng.random() < 0.5:
            x = rng.randint(margin + 20, size - margin - 20)
            y1 = rng.randint(margin, size // 2)
            y2 = rng.randint(size // 2, size - margin)
            walls.append([[x, y1], [x, y2]])
        else:
            y = rng.randint(margin + 20, size - margin - 20)
            x1 = rng.randint(margin, size // 2)
            x2 = rng.randint(size // 2, size - margin)
            walls.append([[x1, y], [x2, y]])
    return [{'points': wall} for wall in walls]


def cave(size, rng):
    walls = []
    for _ in range(rng.randint(2, 4)):
        points = []
        x = rng.randint(0, size // 4)
        y = rng.randint(0, size)
        while x < size:
            points.append([x, max(0, min(size - 1, y))])
            x += rng.randint(30, 80)
            y += rng.randint(-60, 60)
        walls.append({'points': points})
    return walls


def sample(size=256, seed=0):
    rng = random.Random(seed)
    image = Image.fromarray(fixtures.ground(size, size, seed=seed + 1))
    if rng.random() < 0.35:
        side = rng.choice((50, 60, 70, 80, 90))
        origin = [rng.randint(0, side - 1), rng.randint(0, side - 1)]
        strength = rng.choice((25, 40, 60, 100, 160))
        image = fixtures.paste_grid(
            image, fixtures.square_grid(size, size, side, origin, strength=strength))
    if rng.random() < 0.25:
        low = max(8, size // 12)
        high = max(low + 8, size - 3 * low)
        x = rng.randint(low, high)
        y = rng.randint(low, high)
        w = rng.randint(max(8, size // 10), max(9, size // 3))
        h = rng.randint(max(8, size // 10), max(9, size // 3))
        x = min(x, size - w - 1)
        y = min(y, size - h - 1)
        image = fixtures.draw_areas(image, [{
            'id': 'a', 'type': rng.choice(('water', 'lava', 'difficult')),
            'polygon': [[x, y], [x + w, y], [x + w, y + h], [x, y + h], [x, y]]}])

    walls = rooms(size, rng) if rng.random() < 0.6 else cave(size, rng)
    width = rng.choice((4, 5, 6, 8))
    image = fixtures.draw_walls(image, walls, width=width)
    mask = wall_mask(size, walls, width)
    return np.asarray(image, dtype=np.uint8), np.asarray(mask, dtype=np.uint8)


def build(count, size=256):
    images = np.zeros((count, size, size, 3), dtype=np.uint8)
    masks = np.zeros((count, 1, size, size), dtype=np.uint8)
    for index in range(count):
        image, mask = sample(size, seed=index * 7919 + 13)
        images[index] = image
        masks[index, 0] = mask // 255
    return images, masks
