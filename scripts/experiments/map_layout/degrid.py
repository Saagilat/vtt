#!/usr/bin/env python3
# Стенд подготовки карты: убрать печатную сетку вычитанием её профиля.
#
# Инструмент эксперимента, не проект. Идея: сетка — ровная полупрозрачная тёмная накладка
# поверх карты. Если усреднить картинку поперёк линий, получится профиль затемнения; его можно
# вычесть (add) или разделить на коэффициент пропускания (mul), не закрашивая и не дорисовывая.
# Профиль берётся либо общий по карте (global), либо свой для каждой линии (local).
#
# Использование:
#   python3 scripts/experiments/map_layout/degrid.py --only 01 --mode add-local

import argparse
import pathlib
import shutil
import sys

import numpy as np
from PIL import Image

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import grid  # noqa: E402

IMAGE_SUFFIXES = ('.png', '.jpg', '.jpeg', '.webp')
RADIUS = 4
MODES = ('add-global', 'add-local', 'mul-global', 'mul-local')


def line_samples(channel, centers, axis):
    height, width = channel.shape
    samples = []
    for center in centers:
        center = int(round(center))
        medians = []
        for d in range(-RADIUS, RADIUS + 1):
            if axis == 'v':
                x = center + d
                medians.append(float(np.median(channel[:, x])) if 0 <= x < width else None)
            else:
                y = center + d
                medians.append(float(np.median(channel[y, :])) if 0 <= y < height else None)
        if any(value is None for value in medians):
            continue
        values = np.asarray(medians, dtype=np.float32)
        background = float(np.median(np.concatenate([values[:2], values[-2:]])))
        samples.append((center, values, background))
    return samples


def apply_profile(out, axis, mode, samples):
    height, width = out.shape
    if not samples:
        return
    if mode.endswith('global'):
        values = np.median(np.asarray([item[1] for item in samples]), axis=0)
        background = float(np.median(np.concatenate([values[:2], values[-2:]])))
        plan = [(center, values, background) for center, _, _ in samples]
    else:
        plan = samples

    for center, values, background in plan:
        for d in range(-RADIUS, RADIUS + 1):
            correction = background - float(values[d + RADIUS])
            if mode.startswith('add'):
                if correction <= 0.5:
                    continue
                change = correction
            else:
                if correction <= 0.5:
                    continue
                change = background / max(1.0, float(values[d + RADIUS]))
            if axis == 'v':
                x = center + d
                if 0 <= x < width:
                    if mode.startswith('add'):
                        out[:, x] += change
                    else:
                        out[:, x] *= change
            else:
                y = center + d
                if 0 <= y < height:
                    if mode.startswith('add'):
                        out[y, :] += change
                    else:
                        out[y, :] *= change


def process(path, vertical, horizontal, mode, passes, out_root):
    image = Image.open(path).convert('RGB')
    data = np.asarray(image, dtype=np.float32)
    result = data.copy()
    for pass_index in range(passes):
        mean = result.mean(axis=2)
        for det, axis in ((vertical, 'v'), (horizontal, 'h')):
            if det is None:
                continue
            profile_1d = mean.mean(axis=0 if axis == 'v' else 1)
            centers = grid.refine_centers(profile_1d, det)
            samples = line_samples(mean, centers, axis)
            for channel in range(3):
                apply_profile(result[:, :, channel], axis, mode, samples)
        result = np.clip(result, 0.0, 255.0)
        print('  проход ' + str(pass_index + 1) + ' готов')
    prefix = path.stem.replace('-original', '')
    out_dir = out_root / prefix
    out_dir.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(path, out_dir / ('original' + path.suffix.lower()))
    suffix = '-degrid-' + mode + ('-p' + str(passes) if passes > 1 else '')
    out_path = out_dir / (suffix.lstrip('-') + '.png')
    Image.fromarray(result.astype(np.uint8)).save(out_path)
    print('  результат: ' + str(out_path))
    return out_path


def main():
    parser = argparse.ArgumentParser(description='Стенд: убрать сетку профилем')
    parser.add_argument('--review', default='scripts/experiments/map_layout/review')
    parser.add_argument('--only', default='', help='только файлы с этим префиксом')
    parser.add_argument('--mode', choices=MODES, default='add-local')
    parser.add_argument('--passes', type=int, default=1, help='сколько раз повторить вычитание')
    parser.add_argument('--out', default='scripts/experiments/map_layout/results',
                        help='корень результатов: сюда кладутся папки по номерам карт')
    args = parser.parse_args()

    review = pathlib.Path(args.review)
    files = sorted(p for p in review.iterdir()
                   if p.suffix.lower() in IMAGE_SUFFIXES and p.stem.endswith('-original'))
    if args.only:
        files = [p for p in files if p.stem.startswith(args.only)]
    if not files:
        print('нет исходников в ' + str(review), file=sys.stderr)
        return 1

    out_root = pathlib.Path(args.out)
    for path in files:
        image = Image.open(path).convert('RGB')
        gray = np.asarray(image.convert('L'), dtype=np.float32)
        vertical = grid.detect(grid.profile(gray, axis=0), 16, 256)
        horizontal = grid.detect(grid.profile(gray, axis=1), 16, 256)
        is_grid, notes = grid.combined(vertical, horizontal)
        print(path.name + ': ' + ('сетка есть' if is_grid else 'сетки нет')
              + ' — ' + '; '.join(notes) + ', режим ' + args.mode)
        if not is_grid:
            print('')
            continue
        process(path, vertical, horizontal, args.mode, args.passes, out_root)
        print('')
    return 0


if __name__ == '__main__':
    sys.exit(main())
