#!/usr/bin/env python3
# Стенд подготовки карты: есть ли на карте напечатанная сетка и можно ли её стереть.
#
# Это инструмент эксперимента, а не проект: в пакеты он не едет, фикстуры в git не кладутся.
# Решение «годится / не годится» принимает человек глазами; числа здесь — чтобы знать, куда
# смотреть.
#
# Как ищется сетка: по среднему профилю яркости. Если линии есть, то на предсказанных линиях
# яркость ниже, чем между ними. Перебираем шаг и сдвиг, считаем провал между серединой
# промежутка и линией; лучший кандидат и есть гипотеза. Порог «подтверждено» — 5 оттенков:
# на настоящих картах с сеткой провал был 11–29, на карте без сетки — до 2.
#
# Плитка и паркет дают такой же провал, поэтому одной яркости мало: у настоящей печатной сетки
# шаг по осям один и тот же, а линии тонкие. Если шаги расходятся или швы широкие, стенд говорит
# «не подтверждена» и предлагает посмотреть глазами.
#
# Использование:
#   python3 scripts/experiments/map_layout/grid.py --in 'тестовый материал'
#   python3 scripts/experiments/map_layout/grid.py --in kartinka.jpg --erase

import argparse
import pathlib
import sys

import numpy as np
from PIL import Image

IMAGE_SUFFIXES = ('.png', '.jpg', '.jpeg', '.webp')
CONFIRMED = 5.0
WEAK = 2.0


def load_gray(path):
    image = Image.open(path).convert('L')
    return np.asarray(image, dtype=np.float32)


def profile(gray, axis):
    return gray.mean(axis=axis)


def best_offset(v, step):
    if len(v) < step * 3:
        return None
    best = None
    for offset in range(step):
        lines = np.arange(offset, len(v), step)
        mids = np.arange(offset + step // 2, len(v), step)
        if len(lines) < 3 or len(mids) < 2:
            continue
        line = float(v[lines].mean())
        between = float(v[mids].mean())
        drop = between - line
        if best is None or drop > best[1]:
            best = (offset, drop, line, between)
    if best is None:
        return None
    offset, drop, line, between = best
    return {'step': step, 'offset': offset, 'drop': drop, 'line': line, 'between': between}


def estimate_width(v, offset, step):
    widths = []
    for center in range(offset, len(v), step):
        line = float(v[center])
        between = float(v[min(len(v) - 1, center + step // 2)])
        threshold = (line + between) / 2
        left = center
        while left - 1 >= 0 and v[left - 1] < threshold:
            left -= 1
        right = center
        while right + 1 < len(v) and v[right + 1] < threshold:
            right += 1
        widths.append(right - left + 1)
    if not widths:
        return 1
    width = int(round(float(np.median(widths))))
    return max(1, min(width, 9))


def verdict(drop):
    if drop >= CONFIRMED:
        return 'подтверждено'
    if drop >= WEAK:
        return 'слабо'
    return 'не подтверждено'


def detect(v, step_min, step_max):
    candidates = []
    for step in range(step_min, step_max + 1):
        found = best_offset(v, step)
        if found is not None:
            candidates.append(found)
    if not candidates:
        return None
    best_drop = max(c['drop'] for c in candidates)
    # Меньший шаг лучше большего, если провал почти такой же: 70, а не 140.
    good = [c for c in candidates if c['drop'] >= best_drop * 0.85]
    chosen = min(good, key=lambda c: c['step'])
    chosen['width'] = estimate_width(v, chosen['offset'], chosen['step'])
    chosen['verdict'] = verdict(chosen['drop'])
    return chosen


# Сетка — правило стола с равными клетками: у настоящей печатной сетки шаг по осям один и тот
# же, а линии тонкие. Плитка и паркет дают провал не хуже, но шаги по осям расходятся или швы
# широкие. Это не приговор, а повод посмотреть глазами.
def combined(vertical, horizontal):
    notes = []
    axes = [d for d in (vertical, horizontal) if d is not None]
    if not axes:
        return False, ['кандидатов нет']
    confirmed = [d for d in axes if d['verdict'] == 'подтверждено']
    if not confirmed:
        return False, ['ни одна ось не подтверждена']
    if len(confirmed) < 2:
        return False, ['подтверждена только одна ось']
    step_v, step_h = vertical['step'], horizontal['step']
    tolerance = max(2, int(round(max(step_v, step_h) * 0.02)))
    if abs(step_v - step_h) > tolerance:
        notes.append('шаги по осям не совпадают (' + str(step_v) + ' и ' + str(step_h) + ')')
    width = max(vertical['width'], horizontal['width'])
    if width > 4:
        notes.append('линии толстые (' + str(width) + ' px), у печатной сетки обычно 1–3')
    if notes:
        return False, notes
    return True, ['обе оси, шаг ' + str(step_v) + ', толщина ' + str(vertical['width'])]


def overlay(gray, vertical, horizontal):
    rgb = np.stack([gray, gray, gray], axis=-1).copy()
    red = np.array([220.0, 30.0, 30.0], dtype=np.float32)
    if vertical is not None:
        for c in np.arange(vertical['offset'], rgb.shape[1], vertical['step']):
            rgb[:, c] = rgb[:, c] * 0.35 + red * 0.65
    if horizontal is not None:
        for r in np.arange(horizontal['offset'], rgb.shape[0], horizontal['step']):
            rgb[r, :] = rgb[r, :] * 0.35 + red * 0.65
    return np.clip(rgb, 0.0, 255.0).astype(np.uint8)


def erase(gray, vertical, horizontal):
    out = gray.copy()
    for det, axis in ((vertical, 'v'), (horizontal, 'h')):
        if det is None or det['verdict'] != 'подтверждено':
            continue
        half = max(1, det['width'] // 2 + 1)
        if axis == 'v':
            for c in np.arange(det['offset'], out.shape[1], det['step']):
                left, right = c - half - 1, c + half + 1
                if left < 0 or right >= out.shape[1]:
                    continue
                t = np.linspace(0.0, 1.0, 2 * half + 3)[1:-1]
                out[:, c - half:c + half + 1] = (out[:, left][:, None] * (1 - t)
                                                + out[:, right][:, None] * t)
        else:
            for r in np.arange(det['offset'], out.shape[0], det['step']):
                up, down = r - half - 1, r + half + 1
                if up < 0 or down >= out.shape[0]:
                    continue
                t = np.linspace(0.0, 1.0, 2 * half + 3)[1:-1]
                out[r - half:r + half + 1, :] = (out[up, :][None, :] * (1 - t)[:, None]
                                                 + out[down, :][None, :] * t[:, None])
    return out


def describe(title, det):
    if det is None:
        print('  ' + title + ': кандидатов нет')
        return
    print('  ' + title + ': шаг ' + str(det['step']) + ', сдвиг ' + str(det['offset'])
          + ', провал ' + str(round(det['drop'], 1)) + ', толщина ' + str(det['width'])
          + ' → ' + det['verdict'])


def process(path, out_dir, step_min, step_max, do_erase):
    gray = load_gray(path)
    vertical = detect(profile(gray, axis=0), step_min, step_max)
    horizontal = detect(profile(gray, axis=1), step_min, step_max)

    print(path.name + ': ' + str(gray.shape[1]) + 'x' + str(gray.shape[0]))
    describe('вертикаль', vertical)
    describe('горизонталь', horizontal)

    is_grid, notes = combined(vertical, horizontal)
    if is_grid:
        print('  вывод: похоже на сетку — ' + '; '.join(notes))
    else:
        print('  вывод: сетка не подтверждена — ' + '; '.join(notes))

    grid_path = out_dir / (path.stem + '.grid.png')
    Image.fromarray(overlay(gray, vertical, horizontal)).save(grid_path)
    print('  наложено: ' + str(grid_path))

    if do_erase and is_grid:
        erased_path = out_dir / (path.stem + '.erased.png')
        Image.fromarray(np.clip(erase(gray, vertical, horizontal), 0.0, 255.0).astype(np.uint8)).save(erased_path)
        print('  стёрто: ' + str(erased_path))
    elif do_erase:
        print('  стёрто: пропущено, сетка не подтверждена')
    print('')


def main():
    parser = argparse.ArgumentParser(description='Стенд: есть ли напечатанная сетка')
    parser.add_argument('--in', dest='source', required=True, help='картинка или каталог')
    parser.add_argument('--out', dest='target', default='scripts/experiments/map_layout/out')
    parser.add_argument('--step-min', type=int, default=16)
    parser.add_argument('--step-max', type=int, default=256)
    parser.add_argument('--erase', action='store_true', help='стереть найденную сетку по соседям')
    args = parser.parse_args()

    source = pathlib.Path(args.source)
    if source.is_dir():
        files = [p for p in sorted(source.iterdir()) if p.suffix.lower() in IMAGE_SUFFIXES]
    else:
        files = [source]
    if not files:
        print('нет картинок: ' + str(source), file=sys.stderr)
        return 1

    out_dir = pathlib.Path(args.target)
    out_dir.mkdir(parents=True, exist_ok=True)
    for path in files:
        process(path, out_dir, args.step_min, args.step_max, args.erase)
    return 0


if __name__ == '__main__':
    sys.exit(main())
