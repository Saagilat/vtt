#!/usr/bin/env python3
# Стенд разметчика: синтетические пары «карта — маска стен» для обучения.
#
# Генератор не пишет файлы: он отдаёт numpy-пары, чтобы обучение не упиралось в диск.
# Рисуем комнаты и пещеры, иногда сетку и области; целевая маска — только стены.
# Двери и области в обучение первой модели не входят: их ещё предстоит научиться находить.

import io
import random

import numpy as np
from PIL import Image, ImageDraw

import fixtures


PALETTES = [
    ((66, 92, 52), (104, 128, 76), (46, 64, 38)),      # трава
    ((92, 88, 80), (130, 124, 112), (58, 55, 50)),     # камень
    ((150, 126, 88), (188, 164, 120), (96, 78, 52)),   # песок
    ((104, 74, 48), (142, 104, 66), (62, 42, 26)),     # дерево
]


def texture(size, rng):
    first, second, third = rng.choice(PALETTES)
    small = np.array([[rng.random() for _ in range(8)] for _ in range(8)])
    low = Image.fromarray((small * 255).astype(np.uint8)).resize((size, size), Image.BICUBIC)
    field = np.asarray(low, dtype=np.float32)[:, :, None] / 255.0
    base = np.array(first, dtype=np.float32) * (1 - field) + np.array(second, dtype=np.float32) * field
    image = Image.fromarray(np.clip(base, 0, 255).astype(np.uint8)).convert('RGBA')
    draw = ImageDraw.Draw(image)
    for _ in range(rng.randint(3, 10)):
        x = rng.randint(0, size)
        y = rng.randint(0, size)
        r = rng.randint(10, size // 4)
        shade = rng.choice((third, second, first))
        draw.ellipse([x - r, y - r, x + r, y + r], fill=shade + (rng.randint(40, 110),))
    image = image.convert('RGB')
    noise = np.random.default_rng(rng.randint(0, 10 ** 6)).normal(0, 4, (size, size, 1))
    return Image.fromarray(np.clip(np.asarray(image, dtype=np.float32) + noise, 0, 255).astype(np.uint8))


def walls_colored(image, walls, width, color):
    layer = image.convert('RGBA')
    draw = ImageDraw.Draw(layer)
    for wall in walls:
        points = [tuple(point) for point in wall['points']]
        draw.line(points, fill=color + (235,), width=width, joint='curve')
        for point in points:
            draw.ellipse([point[0] - width // 2, point[1] - width // 2,
                          point[0] + width // 2, point[1] + width // 2], fill=color + (235,))
    return layer.convert('RGB')


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
    image = texture(size, rng)
    if rng.random() < 0.4:
        side = rng.choice((40, 50, 60, 70, 80, 90, 100))
        origin = [rng.randint(0, side - 1), rng.randint(0, side - 1)]
        strength = rng.choice((20, 30, 45, 70, 120, 180))
        image = fixtures.paste_grid(
            image, fixtures.square_grid(size, size, side, origin, strength=strength))
    if rng.random() < 0.3:
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

    choice = rng.random()
    if choice < 0.5:
        walls = rooms(size, rng)
    elif choice < 0.85:
        walls = cave(size, rng)
    else:
        walls = cave(size, rng) + rooms(size, rng)
    width = rng.choice((3, 4, 5, 6, 8, 10))
    color = rng.choice(((35, 28, 22), (20, 18, 30), (60, 44, 30), (10, 10, 10)))
    image = walls_colored(image, walls, width, color)
    if rng.random() < 0.3:
        quality = rng.randint(45, 90)
        buffer = io.BytesIO()
        image.save(buffer, format='JPEG', quality=quality)
        buffer.seek(0)
        image = Image.open(buffer).convert('RGB')
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
