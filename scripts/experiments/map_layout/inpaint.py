#!/usr/bin/env python3
# Стенд подготовки карты: стереть сетку моделью inpainting.
#
# Инструмент эксперимента, не проект. Карта обрабатывается плитками: у моделей этого класса
# вход 512x512, а карта больше. Маска строится только из найденной геометрии сетки.
#
# Модели:
#   lama  — LaMa, вход float 0..1, маска 0/1 (1 = стирать), выход 0..255;
#   migan — MI-GAN pipeline (MIT), вход uint8, маска 0 = стирать / 255 = оставить,
#           пайплайн сам обрезает вокруг маски, ресайзит и смешивает.
#
# Результаты кладутся по папкам, по одной на карту:
#   results/01/original.jpg   исходник
#   results/01/mask.png       что именно стираем
#   results/01/lama.png       результат
#   results/01/report.txt     настройки, время, покрытие маски

import argparse
import datetime
import pathlib
import shutil
import sys
import time

import numpy as np
import onnxruntime as ort
from PIL import Image, ImageFilter

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import grid  # noqa: E402

IMAGE_SUFFIXES = ('.png', '.jpg', '.jpeg', '.webp')
DEFAULT_MODELS = {
    'lama': 'scripts/experiments/map_layout/models/lama_fp32.onnx',
    'migan': 'scripts/experiments/map_layout/models/migan_pipeline.onnx',
    'aot': 'scripts/experiments/map_layout/models/aot_inpainting.onnx',
}


def tiles(size, tile, stride):
    positions = []
    pos = 0
    while True:
        if pos + tile >= size:
            positions.append(max(0, size - tile))
            break
        positions.append(pos)
        pos += stride
    return positions


def crop_padded(array, y, x, size):
    height, width = array.shape[:2]
    part = array[y:min(height, y + size), x:min(width, x + size)]
    pad_y = size - part.shape[0]
    pad_x = size - part.shape[1]
    if pad_y or pad_x:
        pads = ((0, pad_y), (0, pad_x)) + ((0, 0),) * (array.ndim - 2)
        part = np.pad(part, pads, mode='edge')
    return part


def build_mask(image_size, vertical, horizontal, extra, minimum):
    # Маску строим численно, а не ImageDraw: линия PIL шириной 2 рисуется как x..x+1,
    # а печатная линия лежит на x-1..x, и половина её оставалась вне маски.
    width, height = image_size
    mask = np.zeros((height, width), dtype=np.uint8)
    for det, axis in ((vertical, 'v'), (horizontal, 'h')):
        if det is None:
            continue
        draw_width = max(minimum, det['width'] + extra)
        limit = width if axis == 'v' else height
        shift = draw_width // 2
        for pos in grid.positions(det['offset'], det['step'], limit):
            start = pos - shift
            end = start + draw_width - 1
            if axis == 'v':
                left, right = max(0, start), min(width - 1, end)
                if left <= right:
                    mask[:, left:right + 1] = 255
            else:
                top, bottom = max(0, start), min(height - 1, end)
                if top <= bottom:
                    mask[top:bottom + 1, :] = 255
    return Image.fromarray(mask, mode='L')


def dilate(mask, radius):
    if radius <= 0:
        return mask
    return mask.filter(ImageFilter.MaxFilter(2 * radius + 1))


def feather(size, edge=48):
    axis = np.arange(size, dtype=np.float32)
    distance = np.minimum(axis, axis[::-1])
    weight = np.minimum(distance[:, None], distance[None, :]) / float(edge)
    return np.clip(weight, 0.0, 1.0)


def run_tile(kind, session, rgb_tile, model_tile):
    if kind == 'lama':
        inputs = {
            'image': (rgb_tile / 255.0).transpose(2, 0, 1)[None].astype(np.float32),
            'mask': model_tile[None, None].astype(np.float32),
        }
        output = session.run(None, inputs)[0][0].transpose(1, 2, 0)
        return np.clip(output.astype(np.float32), 0.0, 255.0)
    if kind == 'aot':
        inputs = {
            'img': (rgb_tile / 255.0).transpose(2, 0, 1)[None].astype(np.float32),
            'mask': (model_tile * 255.0)[None, None].astype(np.float32),
        }
        output = session.run(None, inputs)[0][0].transpose(1, 2, 0)
        return np.clip(output.astype(np.float32) * 255.0, 0.0, 255.0)
    mask = np.where(model_tile > 0.0, 0, 255).astype(np.uint8)
    inputs = {
        'image': rgb_tile.transpose(2, 0, 1)[None].astype(np.uint8),
        'mask': mask[None, None],
    }
    output = session.run(None, inputs)[0][0].transpose(1, 2, 0)
    return output.astype(np.float32)


def inpaint(image, model_mask, paste_mask, session, kind, size, stride):
    width, height = paste_mask.size
    rgb = np.asarray(image, dtype=np.uint8)
    model01 = np.asarray(model_mask, dtype=np.float32) / 255.0
    paste01 = np.asarray(paste_mask, dtype=np.float32) / 255.0
    accum = np.zeros((height, width, 3), dtype=np.float32)
    weights = np.zeros((height, width, 1), dtype=np.float32)
    weight_tile = feather(size)[:, :, None]
    runs = 0
    started = time.monotonic()

    for y in tiles(height, size, stride):
        for x in tiles(width, size, stride):
            mask_tile = crop_padded(model01, y, x, size)
            if float(mask_tile.max()) <= 0.0:
                continue
            rgb_tile = crop_padded(rgb, y, x, size)
            output = run_tile(kind, session, rgb_tile, mask_tile)
            runs += 1

            real_h = min(size, height - y)
            real_w = min(size, width - x)
            part = output[:real_h, :real_w]
            part_mask = paste01[y:y + real_h, x:x + real_w]
            part_weight = weight_tile[:real_h, :real_w] * part_mask[:, :, None]
            accum[y:y + real_h, x:x + real_w] += part * part_weight
            weights[y:y + real_h, x:x + real_w] += part_weight

    result = rgb.astype(np.float32)
    covered = weights[:, :, 0] > 0
    result[covered] = accum[covered] / weights[covered]
    elapsed = time.monotonic() - started
    return np.clip(result, 0.0, 255.0).astype(np.uint8), runs, elapsed


def process(path, session, kind, size, stride, mask_extra, mask_min, mask_context, out_root, tag):
    image = Image.open(path).convert('RGB')
    gray = np.asarray(image.convert('L'), dtype=np.float32)
    vertical = grid.detect(grid.profile(gray, axis=0), 16, 256)
    horizontal = grid.detect(grid.profile(gray, axis=1), 16, 256)
    is_grid, notes = grid.combined(vertical, horizontal)
    print(path.name + ': ' + ('сетка подтверждена' if is_grid else 'сетка не подтверждена')
          + ' — ' + '; '.join(notes))
    if not is_grid:
        print('')
        return False

    prefix = path.stem.replace('-original', '')
    out_dir = out_root / prefix
    out_dir.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(path, out_dir / ('original' + path.suffix.lower()))

    paste_mask = build_mask(image.size, vertical, horizontal, mask_extra, mask_min)
    model_mask = dilate(paste_mask, mask_context)
    paste_mask.save(out_dir / 'mask.png')

    result, runs, elapsed = inpaint(image, model_mask, paste_mask, session, kind, size, stride)
    name = kind + (('-' + tag) if tag else '')
    result_path = out_dir / (name + '.png')
    Image.fromarray(result).save(result_path)

    covered = float(np.asarray(paste_mask, dtype=np.float32).mean()) / 255.0 * 100.0
    seen = float(np.asarray(model_mask, dtype=np.float32).mean()) / 255.0 * 100.0
    report = [
        'файл: ' + path.name,
        'модель: ' + kind,
        'маска: %.2f %% карты, модель видит %.2f %%' % (covered, seen),
        'плиток: %d, размер плитки: %d, перекрытие: %d' % (runs, size, size - stride),
        'время инференса: %.1f с' % elapsed,
        'настройки: mask_extra=%d mask_min=%d mask_context=%d'
        % (mask_extra, mask_min, mask_context),
        'сделано: ' + datetime.datetime.now().strftime('%Y-%m-%d %H:%M'),
    ]
    (out_dir / ('report-' + name + '.txt')).write_text(chr(10).join(report) + chr(10),
                                                       encoding='utf-8')
    print('  маска: %.2f %% карты (модель видит %.2f %%), плиток: %d, модель: %.1f с'
          % (covered, seen, runs, elapsed))
    print('  папка: ' + str(out_dir))
    print('')
    return True


def main():
    parser = argparse.ArgumentParser(description='Стенд: стереть сетку моделью')
    parser.add_argument('--review', default='scripts/experiments/map_layout/review')
    parser.add_argument('--out', default='scripts/experiments/map_layout/results',
                        help='корень результатов: сюда кладутся папки по номерам карт')
    parser.add_argument('--kind', choices=('lama', 'migan', 'aot'), default='lama')
    parser.add_argument('--model', default='', help='путь к ONNX; по умолчанию — по виду модели')
    parser.add_argument('--tile', type=int, default=512)
    parser.add_argument('--overlap', type=int, default=128)
    parser.add_argument('--only', default='', help='только файлы с этим префиксом, например 01')
    parser.add_argument('--mask-extra', type=int, default=0,
                        help='сколько пикселей добавить к толщине линии в маске')
    parser.add_argument('--mask-min', type=int, default=2, help='минимальная толщина маски')
    parser.add_argument('--mask-context', type=int, default=3,
                        help='на сколько расширить маску только для модели')
    parser.add_argument('--tag', default='', help='суффикс к имени результата')
    args = parser.parse_args()

    review = pathlib.Path(args.review)
    files = sorted(p for p in review.iterdir()
                   if p.suffix.lower() in IMAGE_SUFFIXES and p.stem.endswith('-original'))
    if args.only:
        files = [p for p in files if p.stem.startswith(args.only)]
    if not files:
        print('нет исходников в ' + str(review), file=sys.stderr)
        return 1

    model_path = args.model or DEFAULT_MODELS[args.kind]
    if not pathlib.Path(model_path).exists():
        print('нет модели: ' + model_path, file=sys.stderr)
        return 1

    session = ort.InferenceSession(model_path, providers=['CPUExecutionProvider'])
    print('модель: ' + args.kind + ' (' + model_path + ')')
    print('вход: ' + str([(item.name, item.shape, item.type) for item in session.get_inputs()]))
    print('')

    out_root = pathlib.Path(args.out)
    done = 0
    for path in files:
        if process(path, session, args.kind, args.tile, args.tile - args.overlap,
                   args.mask_extra, args.mask_min, args.mask_context, out_root, args.tag):
            done += 1
    print('стёрто моделью: ' + str(done) + ' из ' + str(len(files)))
    return 0


if __name__ == '__main__':
    sys.exit(main())


