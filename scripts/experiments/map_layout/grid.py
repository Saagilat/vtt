#!/usr/bin/env python3
# Стенд подготовки карты: есть ли на карте напечатанная сетка и можно ли её стереть.
#
# Инструмент эксперимента, не проект: в пакеты не едет, карты мастера в git не кладутся.
# Результат — папка review: исходник, наложение и (если сетка подтверждена) стёртая карта,
# по номеру на карту. Рядом summary.txt: номер, файл, вердикт и причина.
#
# Сетка ищется по среднему профилю яркости. Период сначала грубо находится по FFT (это ловит
# бледную сетку, которую перебор целых шагов теряет), потом шаг и сдвиг уточняются дробно.
# Плитка и паркет дают такой же провал, поэтому одной яркости мало: у настоящей печатной сетки
# шаг по осям один и тот же (допуск 1 px), линии тонкие (до 4 px) и линий не меньше пяти.
#
# Красные линии — только подтверждённая сетка; оранжевые — кандидаты, которые отклонены.
#
# Использование:
#   python3 scripts/experiments/map_layout/grid.py --in 'тестовый материал' --erase

import argparse
import pathlib
import shutil
import sys

import numpy as np
from PIL import Image, ImageDraw, ImageFont

IMAGE_SUFFIXES = ('.png', '.jpg', '.jpeg', '.webp')
CONFIRMED = 5.0
WEAK = 2.0
STEP_TOLERANCE = 1.0
MAX_GRID_WIDTH = 4
MIN_LINES = 5
HARMONIC_SHARE = 0.65
OFFSET_STEP = 0.5
STEP_STEP = 0.1
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


def best_phase(v, step):
    if len(v) < step * MIN_LINES:
        return None
    best = None
    offset = 0.0
    while offset < step:
        lines = np.arange(offset, len(v), step).astype(int)
        mids = np.arange(offset + step / 2.0, len(v), step).astype(int)
        lines = lines[(lines >= 0) & (lines < len(v))]
        mids = mids[(mids >= 0) & (mids < len(v))]
        if len(lines) < MIN_LINES or len(mids) < MIN_LINES - 1:
            offset += OFFSET_STEP
            continue
        drop = float(v[mids].mean() - v[lines].mean())
        if best is None or drop > best[0]:
            best = (drop, offset)
        offset += OFFSET_STEP
    if best is None:
        return None
    return {'step': round(step, 2), 'offset': round(best[1], 1), 'drop': best[0]}


def fft_periods(v, step_min, step_max, limit=6):
    trend = np.convolve(v, np.ones(201) / 201, mode='same')
    detrended = v - trend
    window = np.hanning(len(v))
    spec = np.abs(np.fft.rfft(detrended * window))
    freqs = np.fft.rfftfreq(len(v))
    peaks = []
    for i in range(1, len(spec) - 1):
        if freqs[i] <= 0:
            continue
        period = 1.0 / freqs[i]
        if period < step_min or period > step_max:
            continue
        if spec[i] > spec[i - 1] and spec[i] > spec[i + 1]:
            peaks.append((float(spec[i]), period))
    peaks.sort(reverse=True)
    return [period for _, period in peaks[:limit]]


def detect(v, step_min, step_max):
    periodic = []
    for period in fft_periods(v, step_min, step_max):
        best = None
        step = max(float(step_min), period * 0.95)
        limit = min(float(step_max), period * 1.05)
        while step <= limit:
            found = best_phase(v, step)
            if found is not None and (best is None or found['drop'] > best['drop']):
                best = found
            step += STEP_STEP
        if best is not None:
            periodic.append(best)
    pool = periodic
    if not pool:
        for step in range(step_min, step_max + 1):
            found = best_phase(v, float(step))
            if found is not None:
                pool.append(found)
    if not pool:
        return None
    best_drop = max(c['drop'] for c in pool)
    # Настоящий шаг — наименьший период FFT, чей провал почти такой же: 42.5, а не 127.
    threshold = max(CONFIRMED, best_drop * HARMONIC_SHARE)
    chosen = None
    for candidate in sorted(pool, key=lambda c: c['step']):
        if candidate['drop'] >= threshold:
            chosen = candidate
            break
    if chosen is None:
        chosen = max(pool, key=lambda c: c['drop'])
    chosen['width'] = estimate_width(v, chosen['offset'], chosen['step'])
    chosen['verdict'] = verdict(chosen['drop'])
    return chosen


def positions(offset, step, limit):
    result = []
    pos = offset
    while pos < limit:
        index = int(round(pos))
        if 0 <= index < limit:
            result.append(index)
        pos += step
    return result


def refine_centers(v, det, radius=3):
    # Уточняет положение каждой линии по локальному минимуму профиля: шаг из FFT дробный,
    # и за десятки линий накопленный сдвиг доходит до пары пикселей.
    centers = []
    for pos in positions(det['offset'], det['step'], len(v)):
        left = max(0, pos - radius)
        right = min(len(v), pos + radius + 1)
        segment = v[left:right]
        if len(segment) < 3:
            continue
        index = int(np.argmin(segment))
        shift = 0.0
        if 0 < index < len(segment) - 1:
            before, middle, after = (float(segment[index - 1]), float(segment[index]),
                                     float(segment[index + 1]))
            denominator = before - 2.0 * middle + after
            if abs(denominator) > 1e-6:
                shift = 0.5 * (before - after) / denominator
                shift = max(-0.5, min(0.5, shift))
        centers.append(left + index + shift)
    return centers


def estimate_width(v, offset, step):
    widths = []
    for center in positions(offset, step, len(v)):
        line = float(v[center])
        mid = int(round(center + step / 2.0))
        if mid >= len(v):
            continue
        between = float(v[mid])
        threshold = (line + between) / 2.0
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
    if abs(vertical['step'] - horizontal['step']) > STEP_TOLERANCE:
        notes.append('шаги по осям не совпадают (' + str(vertical['step']) + ' и '
                     + str(horizontal['step']) + ')')
    width = max(vertical['width'], horizontal['width'])
    if width > MAX_GRID_WIDTH:
        notes.append('линии толстые (' + str(width) + ' px), у печатной сетки до ' + str(MAX_GRID_WIDTH))
    if notes:
        return False, notes
    return True, ['обе оси, шаг ' + str(vertical['step']) + ', толщина ' + str(vertical['width'])]


def draw_overlay(image, title, vertical, horizontal, is_grid, notes):
    result = image.copy()
    draw = ImageDraw.Draw(result)
    thickness = max(2, image.size[0] // 700)
    for det, axis in ((vertical, 'v'), (horizontal, 'h')):
        if det is None:
            continue
        color = (220, 30, 30) if is_grid else (255, 150, 0)
        if axis == 'v':
            for c in positions(det['offset'], det['step'], image.size[0]):
                draw.line([(c, 0), (c, image.size[1])], fill=color, width=thickness)
        else:
            for r in positions(det['offset'], det['step'], image.size[1]):
                draw.line([(0, r), (image.size[0], r)], fill=color, width=thickness)

    font = load_font(max(16, image.size[0] // 55))
    lines = [title]
    for label, det in (('верт', vertical), ('гор', horizontal)):
        if det is None:
            lines.append(label + ': кандидатов нет')
        else:
            lines.append(label + ': шаг ' + str(det['step']) + ', провал ' + str(round(det['drop'], 1))
                         + ', толщина ' + str(det['width']) + ' — ' + det['verdict'])
    lines.append(('сетка подтверждена: ' if is_grid else 'сетка не подтверждена: ') + '; '.join(notes))
    line_height = int(font.size) + 6 if hasattr(font, 'size') else 22
    box_width = 0
    for text in lines:
        bbox = draw.textbbox((0, 0), text, font=font)
        box_width = max(box_width, bbox[2])
    draw.rectangle([(0, 0), (box_width + 20, line_height * len(lines) + 16)], fill=(0, 0, 0))
    y = 8
    for text in lines:
        draw.text((10, y), text, fill=(255, 255, 255), font=font)
        y += line_height
    return result


def erase(rgb, vertical, horizontal):
    out = rgb.astype(np.float32).copy()
    for det, axis in ((vertical, 'v'), (horizontal, 'h')):
        if det is None:
            continue
        half = max(1, det['width'] // 2 + 1)
        if axis == 'v':
            for c in positions(det['offset'], det['step'], out.shape[1]):
                left, right = c - half - 1, c + half + 1
                if left < 0 or right >= out.shape[1]:
                    continue
                t = np.linspace(0.0, 1.0, 2 * half + 3)[1:-1]
                out[:, c - half:c + half + 1, :] = (out[:, left][:, None, :] * (1 - t)[None, :, None]
                                                    + out[:, right][:, None, :] * t[None, :, None])
        else:
            for r in positions(det['offset'], det['step'], out.shape[0]):
                up, down = r - half - 1, r + half + 1
                if up < 0 or down >= out.shape[0]:
                    continue
                t = np.linspace(0.0, 1.0, 2 * half + 3)[1:-1]
                out[r - half:r + half + 1, :, :] = (out[up, :, :][None, :, :] * (1 - t)[:, None, None]
                                                    + out[down, :, :][None, :, :] * t[:, None, None])
    return np.clip(out, 0.0, 255.0).astype(np.uint8)


def process(path, review_dir, number, step_min, step_max, do_erase):
    image = Image.open(path).convert('RGB')
    gray = np.asarray(image.convert('L'), dtype=np.float32)
    vertical = detect(profile(gray, axis=0), step_min, step_max)
    horizontal = detect(profile(gray, axis=1), step_min, step_max)
    is_grid, notes = combined(vertical, horizontal)

    prefix = str(number).zfill(2)
    shutil.copyfile(path, review_dir / (prefix + '-original' + path.suffix.lower()))
    overlay = draw_overlay(image, prefix + ': ' + path.name, vertical, horizontal, is_grid, notes)
    overlay.save(review_dir / (prefix + '-grid.png'))
    erased_name = '-'
    if do_erase and is_grid:
        erased_path = review_dir / (prefix + '-erased.png')
        Image.fromarray(erase(np.asarray(image, dtype=np.float32), vertical, horizontal)).save(erased_path)
        erased_name = erased_path.name

    verdict_text = ('сетка подтверждена: ' if is_grid else 'сетка не подтверждена: ') + '; '.join(notes)
    for label, det in (('верт', vertical), ('гор', horizontal)):
        if det is None:
            print('  ' + label + ': кандидатов нет')
        else:
            print('  ' + label + ': шаг ' + str(det['step']) + ', сдвиг ' + str(det['offset'])
                  + ', провал ' + str(round(det['drop'], 1)) + ', толщина ' + str(det['width'])
                  + ' → ' + det['verdict'])
    print('  ' + verdict_text)

    return (prefix + '  ' + path.name + '  ' + str(image.size[0]) + 'x' + str(image.size[1])
            + '  ' + verdict_text + '  стёрто: ' + erased_name)


def main():
    parser = argparse.ArgumentParser(description='Стенд: есть ли напечатанная сетка')
    parser.add_argument('--in', dest='source', required=True, help='картинка или каталог')
    parser.add_argument('--review', dest='review', default='scripts/experiments/map_layout/review')
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

    review_dir = pathlib.Path(args.review)
    review_dir.mkdir(parents=True, exist_ok=True)
    for old in review_dir.iterdir():
        if old.is_file():
            old.unlink()

    summary = []
    for number, path in enumerate(files, start=1):
        print(str(number) + '. ' + path.name)
        summary.append(process(path, review_dir, number, args.step_min, args.step_max, args.erase))
        print('')

    summary_path = review_dir / 'summary.txt'
    summary_path.write_text(chr(10).join(summary) + chr(10), encoding='utf-8')
    print('итог: ' + str(summary_path))
    return 0


if __name__ == '__main__':
    sys.exit(main())
