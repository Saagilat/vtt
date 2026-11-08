#!/usr/bin/env python3
# Стенд подготовки карты: есть ли на карте напечатанная сетка и можно ли её стереть.
#
# Инструмент эксперимента, а не проект: в пакеты не едет, карты мастера в git не кладутся.
# Решение «годится / не годится» принимает человек глазами; числа — чтобы знать, куда смотреть.
#
# Сетка ищется по среднему профилю яркости: на предсказанных линиях яркость ниже, чем между
# ними. Перебираем шаг и сдвиг, считаем провал. Порог «подтверждено» — 5 оттенков: на настоящих
# картах с сеткой провал был 11–29, на карте без сетки — до 2.
#
# Плитка и паркет дают такой же провал, поэтому одной яркости мало: у настоящей печатной сетки
# шаг по осям один и тот же (допуск 1 px), а линии тонкие (до 4 px). Иначе — «не подтверждена».
#
# Использование:
#   python3 scripts/experiments/map_layout/grid.py --in 'тестовый материал' --erase

import argparse
import pathlib
import sys

import numpy as np
from PIL import Image, ImageDraw, ImageFont

IMAGE_SUFFIXES = ('.png', '.jpg', '.jpeg', '.webp')
CONFIRMED = 5.0
WEAK = 2.0
STEP_TOLERANCE = 1
MAX_GRID_WIDTH = 4
FONT_PATHS = ('/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf',
              '/usr/share/fonts/TTF/DejaVuSans.ttf')


def load_font(size):
    for path in FONT_PATHS:
        try:
            return ImageFont.truetype(path, size)
        except OSError:
            continue
    return ImageFont.load_default()


def profile(gray, axis):
    return gray.mean(axis=axis)


def best_offset(v, step):
    if len(v) < step * 3:
        return None
    best = None
    for offset in range(step):
        lines = np.arange(offset, len(v), step)
        mids = np.arange(offset + step // 2, len(v), step)
        if len(lines) < 5 or len(mids) < 4:
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
    # Меньше пяти линий — не сетка, а рамка или пара крупных деталей.
    good = [c for c in candidates if c['drop'] >= best_drop * 0.85]
    chosen = min(good, key=lambda c: c['step'])
    chosen['width'] = estimate_width(v, chosen['offset'], chosen['step'])
    chosen['verdict'] = verdict(chosen['drop'])
    return chosen


# Сетка — правило стола с равными клетками: шаг по осям один и тот же, линии тонкие.
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
    if abs(step_v - step_h) > STEP_TOLERANCE:
        notes.append('шаги по осям не совпадают (' + str(step_v) + ' и ' + str(step_h) + ')')
    width = max(vertical['width'], horizontal['width'])
    if width > MAX_GRID_WIDTH:
        notes.append('линии толстые (' + str(width) + ' px), у печатной сетки до ' + str(MAX_GRID_WIDTH))
    if notes:
        return False, notes
    return True, ['обе оси, шаг ' + str(step_v) + ', толщина ' + str(vertical['width'])]


def draw_overlay(image, title, vertical, horizontal, is_grid, notes):
    result = image.copy()
    draw = ImageDraw.Draw(result)
    thickness = max(2, image.size[0] // 700)
    for det, axis in ((vertical, 'v'), (horizontal, 'h')):
        if det is None:
            continue
        if det['verdict'] == 'подтверждено':
            color = (220, 30, 30)
        else:
            color = (255, 150, 0)
        if axis == 'v':
            for c in range(det['offset'], image.size[0], det['step']):
                draw.line([(c, 0), (c, image.size[1])], fill=color, width=thickness)
        else:
            for r in range(det['offset'], image.size[1], det['step']):
                draw.line([(0, r), (image.size[0], r)], fill=color, width=thickness)

    font = load_font(max(16, image.size[0] // 55))
    lines = [title]
    for label, det in (('верт', vertical), ('гор', horizontal)):
        if det is None:
            lines.append(label + ': кандидатов нет')
            continue
        lines.append(label + ': шаг ' + str(det['step']) + ', провал ' + str(round(det['drop'], 1))
                     + ', толщина ' + str(det['width']) + ' — ' + det['verdict'])
    lines.append(('сетка: ' if is_grid else 'сетка не подтверждена: ') + '; '.join(notes))
    line_height = int(font.size) + 6 if hasattr(font, 'size') else 22
    box_width = 0
    for text in lines:
        bbox = draw.textbbox((0, 0), text, font=font)
        box_width = max(box_width, bbox[2])
    box_height = line_height * len(lines) + 16
    draw.rectangle([(0, 0), (box_width + 20, box_height)], fill=(0, 0, 0))
    y = 8
    for text in lines:
        draw.text((10, y), text, fill=(255, 255, 255), font=font)
        y += line_height
    return result


def erase(rgb, vertical, horizontal):
    out = rgb.astype(np.float32).copy()
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
                out[:, c - half:c + half + 1, :] = (out[:, left][:, None, :] * (1 - t)[None, :, None]
                                                    + out[:, right][:, None, :] * t[None, :, None])
        else:
            for r in np.arange(det['offset'], out.shape[0], det['step']):
                up, down = r - half - 1, r + half + 1
                if up < 0 or down >= out.shape[0]:
                    continue
                t = np.linspace(0.0, 1.0, 2 * half + 3)[1:-1]
                out[r - half:r + half + 1, :, :] = (out[up, :, :][None, :, :] * (1 - t)[:, None, None]
                                                    + out[down, :, :][None, :, :] * t[:, None, None])
    return np.clip(out, 0.0, 255.0).astype(np.uint8)


def process(path, out_dir, step_min, step_max, do_erase):
    image = Image.open(path).convert('RGB')
    gray = np.asarray(image.convert('L'), dtype=np.float32)
    vertical = detect(profile(gray, axis=0), step_min, step_max)
    horizontal = detect(profile(gray, axis=1), step_min, step_max)

    print(path.name + ': ' + str(image.size[0]) + 'x' + str(image.size[1]))
    for label, det in (('вертикаль', vertical), ('горизонталь', horizontal)):
        if det is None:
            print('  ' + label + ': кандидатов нет')
        else:
            print('  ' + label + ': шаг ' + str(det['step']) + ', сдвиг ' + str(det['offset'])
                  + ', провал ' + str(round(det['drop'], 1)) + ', толщина ' + str(det['width'])
                  + ' → ' + det['verdict'])

    is_grid, notes = combined(vertical, horizontal)
    if is_grid:
        print('  вывод: похоже на сетку — ' + '; '.join(notes))
    else:
        print('  вывод: сетка не подтверждена — ' + '; '.join(notes))

    overlay = draw_overlay(image, path.name, vertical, horizontal, is_grid, notes)
    grid_path = out_dir / (path.stem + '.grid.png')
    overlay.save(grid_path)
    print('  наложено: ' + str(grid_path))

    if do_erase and is_grid:
        erased_path = out_dir / (path.stem + '.erased.png')
        image_rgb = np.asarray(image, dtype=np.float32)
        Image.fromarray(erase(image_rgb, vertical, horizontal)).save(erased_path)
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
