#!/usr/bin/env python3
# Стенд разметчика: прогон по кейсам и раскладка результатов.
#
# Пока модель не выбрана, гоняется только прототип поиска сетки. Когда появится vtt-annotate,
# его можно будет подставить через --annotator-cmd, и стенд начнёт сравнивать настоящий результат
# с эталоном, не меняя раскладку и отчёт.
#
# Результаты: results/<кейс>/original.png, result.json, overlay.png, expected.json,
# compare.png, score.json, report.txt.

import argparse
import json
import pathlib
import resource
import shutil
import sys
import time

from PIL import Image

HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(REPO / 'scripts'))

import fixtures  # noqa: E402
import classical  # noqa: E402
import grid  # noqa: E402
import model as wall_model  # noqa: E402
import score as scoring  # noqa: E402
import validate_annotation as contract  # noqa: E402


def annotation_from_detection(result):
    if not result['is_grid']:
        return {'mode': 'none'}
    vertical = result['vertical']
    horizontal = result['horizontal']
    side = round((vertical['step'] + horizontal['step']) / 2.0, 2)
    origin_x = int(round(vertical['offset'] + side / 2.0))
    origin_y = int(round(horizontal['offset'] + side / 2.0))
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


def document(result, expected=None):
    image = result['image']
    grid_value = annotation_from_detection(result)
    if expected is not None and not result['is_grid']:
        grid_value = {'mode': 'none'}
    return {
        'format_version': 1,
        'image': {'width': image['width'], 'height': image['height']},
        'grid': grid_value,
        'walls': [],
        'doors': [],
        'areas': [],
    }


def expected_overlay(image, expected):
    value = expected.get('grid', {})
    if value.get('mode') != 'square' or value.get('rotation_deg', 0):
        return None
    side = value['square']['side_px']
    origin_x, origin_y = value['origin']
    draw_image = image.copy()
    from PIL import ImageDraw
    draw = ImageDraw.Draw(draw_image)
    thickness = max(2, image.size[0] // 700)
    start_x = origin_x - side / 2.0
    start_y = origin_y - side / 2.0
    x = start_x
    while x < image.size[0]:
        draw.line([(x, 0), (x, image.size[1])], fill=(40, 170, 70), width=thickness)
        x += side
    y = start_y
    while y < image.size[1]:
        draw.line([(0, y), (image.size[0], y)], fill=(40, 170, 70), width=thickness)
        y += side
    return draw_image


def overlay_annotation(image, document, title):
    from PIL import ImageDraw
    result = image.copy()
    draw = ImageDraw.Draw(result)
    thickness = max(2, image.size[0] // 700)
    value = document.get('grid', {})
    if value.get('mode') == 'square' and not value.get('rotation_deg', 0):
        side = value['square']['side_px']
        x = value['origin'][0] - side / 2.0
        while x < image.size[0]:
            draw.line([(x, 0), (x, image.size[1])], fill=(220, 30, 30), width=thickness)
            x += side
        y = value['origin'][1] - side / 2.0
        while y < image.size[1]:
            draw.line([(0, y), (image.size[0], y)], fill=(220, 30, 30), width=thickness)
            y += side
    for wall in document.get('walls', []):
        draw.line([tuple(point) for point in wall['points']], fill=(255, 200, 0), width=3)
    for door in document.get('doors', []):
        draw.line([tuple(door['from']), tuple(door['to'])], fill=(40, 200, 80), width=4)
    for area in document.get('areas', []):
        draw.line([tuple(point) for point in area['polygon']], fill=(60, 120, 220), width=3)
    return result


def compare_image(original, result_overlay, wanted_overlay):
    panels = [original, result_overlay]
    if wanted_overlay is not None:
        panels.append(wanted_overlay)
    limit = 700
    thumbs = []
    for panel in panels:
        thumb = panel.copy()
        thumb.thumbnail((limit, limit))
        thumbs.append(thumb)
    width = sum(thumb.width for thumb in thumbs) + 8 * (len(thumbs) - 1)
    height = max(thumb.height for thumb in thumbs)
    canvas = Image.new('RGB', (width, height), (255, 255, 255))
    x = 0
    for thumb in thumbs:
        canvas.paste(thumb, (x, 0))
        x += thumb.width + 8
    return canvas
def verdict_text(verdict):
    if verdict.verdict == 'ok':
        return 'ok'
    return str(verdict.code) + ': ' + str(verdict.message)


def run_case(case_dir, out_root, annotator_cmd='', detector='prototype'):
    originals = [p for p in sorted(case_dir.iterdir())
                 if p.suffix.lower() in grid.IMAGE_SUFFIXES and p.stem == 'original']
    if not originals:
        return None
    source = originals[0]
    expected_path = case_dir / 'expected.json'
    meta_path = case_dir / 'case.json'
    expected = json.loads(expected_path.read_text(encoding='utf-8')) if expected_path.exists() else None
    meta = json.loads(meta_path.read_text(encoding='utf-8')) if meta_path.exists() else {}

    out_dir = out_root / case_dir.name
    out_dir.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, out_dir / ('original' + source.suffix.lower()))

    started = time.monotonic()
    if annotator_cmd:
        return run_external(annotator_cmd, source, out_dir, expected, meta)
    if detector == 'prototype':
        image, result = grid.analyze(source)
        doc = document(result, expected)
        grid_flag = result['is_grid']
        grid_notes = result['notes']
        overlay = grid.draw_overlay(image, source.name, result['vertical'], result['horizontal'],
                                    result['is_grid'], result['notes'])
    elif detector == 'model':
        image = Image.open(source).convert('RGB')
        session = wall_model.load_session(wall_model.DEFAULT_MODEL)
        doc = wall_model.detect(source, session, threshold=wall_model.THRESHOLD)
        grid_flag = doc['grid']['mode'] != 'none'
        grid_notes = ['модель: U-Net 256']
        overlay = overlay_annotation(image, doc, source.name)
    else:
        image = Image.open(source).convert('RGB')
        doc = classical.detect(source, suppress_grid=(detector == 'classical'))
        grid_flag = doc['grid']['mode'] != 'none'
        grid_notes = ['детектор: ' + detector]
        overlay = overlay_annotation(image, doc, source.name)
    elapsed = time.monotonic() - started

    verdict = contract.check(doc)
    expected_verdict = contract.check(expected) if expected is not None else None
    (out_dir / 'result.json').write_text(json.dumps(doc, ensure_ascii=False, indent=2) + chr(10),
                                         encoding='utf-8')
    overlay.save(out_dir / 'overlay.png')

    score_report = None
    if expected is not None:
        shutil.copyfile(expected_path, out_dir / 'expected.json')
        grid_ok, grid_notes = scoring.grid_score(doc['grid'], expected.get('grid', {}))
        known = not meta.get('prototype_supports_grid', True)
        score_report = {
            'grid': {'ok': grid_ok, 'notes': [note for note in grid_notes if note],
                     'known_limitation': known and not grid_ok},
            'walls': scoring.wall_score(doc['walls'], expected.get('walls', [])),
            'doors': scoring.door_score(doc['doors'], expected.get('doors', [])),
            'areas': {'expected': len(expected.get('areas', [])), 'found': len(doc['areas'])},
        }
        (out_dir / 'score.json').write_text(
            json.dumps(score_report, ensure_ascii=False, indent=2) + chr(10), encoding='utf-8')
        wanted = expected_overlay(image, expected)
        if wanted is not None:
            wanted.save(out_dir / 'expected-overlay.png')
        compare_image(image, overlay, wanted).save(out_dir / 'compare.png')
        if meta_path.exists():
            shutil.copyfile(meta_path, out_dir / 'case.json')

    lines = [
        'кейс: ' + case_dir.name,
        'детектор: ' + detector,
        'картинка: %dx%d' % (image.size[0], image.size[1]),
        'время: %.1f с' % elapsed,
        'память процесса: %.0f МБ' % (resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024.0),
        'сетка: ' + ('подтверждена' if grid_flag else 'не подтверждена')
        + ' — ' + '; '.join(item for item in grid_notes if item),
        'контракт результата: ' + verdict_text(verdict),
        'контракт эталона: ' + (verdict_text(expected_verdict) if expected_verdict else '—'),
    ]
    if score_report is not None:
        lines.append('эталон: ' + json.dumps(score_report, ensure_ascii=False))
    (out_dir / 'report.txt').write_text(chr(10).join(lines) + chr(10), encoding='utf-8')
    print(chr(10).join(lines))
    print('')
    return {'name': case_dir.name, 'grid': grid_flag, 'contract': verdict.verdict,
            'score': score_report}


def run_external(command, source, out_dir, expected, meta):
    import shlex
    import subprocess
    output = out_dir / 'result.json'
    started = time.monotonic()
    completed = subprocess.run(shlex.split(command) + ['--image', str(source), '--out', str(output)],
                               capture_output=True, text=True)
    elapsed = time.monotonic() - started
    if completed.returncode != 0:
        print('разметчик ответил ' + str(completed.returncode) + ': ' + completed.stderr.strip())
        return None
    doc = json.loads(output.read_text(encoding='utf-8'))
    verdict = contract.check(doc)
    lines = ['кейс: ' + out_dir.name, 'внешний разметчик: ' + command,
             'время: %.1f с' % elapsed, 'контракт: ' + verdict_text(verdict)]
    score_report = None
    if expected is not None:
        grid_ok, grid_notes = scoring.grid_score(doc.get('grid', {}), expected.get('grid', {}))
        known = not meta.get('prototype_supports_grid', True)
        score_report = {'grid': {'ok': grid_ok, 'notes': [note for note in grid_notes if note],
                                 'known_limitation': known and not grid_ok}}
        lines.append('эталон: ' + json.dumps(score_report, ensure_ascii=False))
        (out_dir / 'score.json').write_text(
            json.dumps(score_report, ensure_ascii=False, indent=2) + chr(10), encoding='utf-8')
    (out_dir / 'report.txt').write_text(chr(10).join(lines) + chr(10), encoding='utf-8')
    print(chr(10).join(lines))
    print('')
    return {'name': out_dir.name, 'grid': False, 'contract': verdict.verdict,
            'score': score_report}


def run_real(path, out_root, detector='prototype'):
    out_dir = out_root / path.stem
    out_dir.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(path, out_dir / ('original' + path.suffix.lower()))
    started = time.monotonic()
    if detector == 'prototype':
        image, result = grid.analyze(path)
        doc = document(result, None)
        grid_flag = result['is_grid']
        grid_notes = result['notes']
        overlay = grid.draw_overlay(image, path.name, result['vertical'], result['horizontal'],
                                    result['is_grid'], result['notes'])
    elif detector == 'model':
        image = Image.open(path).convert('RGB')
        session = wall_model.load_session(wall_model.DEFAULT_MODEL)
        doc = wall_model.detect(path, session, threshold=wall_model.THRESHOLD)
        grid_flag = doc['grid']['mode'] != 'none'
        grid_notes = ['модель: U-Net 256']
        overlay = overlay_annotation(image, doc, path.name)
    else:
        image = Image.open(path).convert('RGB')
        doc = classical.detect(path, suppress_grid=(detector == 'classical'))
        grid_flag = doc['grid']['mode'] != 'none'
        grid_notes = ['детектор: ' + detector]
        overlay = overlay_annotation(image, doc, path.name)
    elapsed = time.monotonic() - started
    verdict = contract.check(doc)
    (out_dir / 'result.json').write_text(json.dumps(doc, ensure_ascii=False, indent=2) + chr(10),
                                         encoding='utf-8')
    overlay.save(out_dir / 'overlay.png')
    lines = ['кейс: ' + path.stem + ' (настоящая карта)',
             'детектор: ' + detector,
             'картинка: %dx%d' % (image.size[0], image.size[1]),
             'время: %.1f с' % elapsed,
             'сетка: ' + ('подтверждена' if grid_flag else 'не подтверждена')
             + ' — ' + '; '.join(item for item in grid_notes if item),
             'стен: ' + str(len(doc['walls'])),
             'контракт: ' + verdict_text(verdict)]
    (out_dir / 'report.txt').write_text(chr(10).join(lines) + chr(10), encoding='utf-8')
    print(chr(10).join(lines))
    print('')
    return {'name': path.stem, 'grid': grid_flag, 'contract': verdict.verdict, 'score': None}


def main():
    parser = argparse.ArgumentParser(description='Стенд разметчика')
    parser.add_argument('--cases', default='scripts/experiments/annotator/cases',
                        help='каталог фикстур (по папке на кейс)')
    parser.add_argument('--in', dest='real', default='', help='каталог настоящих карт')
    parser.add_argument('--out', default='scripts/experiments/annotator/results')
    parser.add_argument('--rebuild', action='store_true', help='пересобрать фикстуры')
    parser.add_argument('--annotator-cmd', default='',
                        help='команда разметчика; по умолчанию гоняется прототип сетки')
    parser.add_argument('--model', default=wall_model.DEFAULT_MODEL,
                        help='ONNX-модель стен для --detector model')
    parser.add_argument('--threshold', type=float, default=wall_model.THRESHOLD,
                        help='порог вероятности маски стен')
    parser.add_argument('--detector', choices=('prototype', 'classical', 'classical-no-suppress', 'model'),
                        default='prototype',
                        help='prototype — только сетка; classical — Canny+Хаф с подавлением сетки; '
                             'classical-no-suppress — то же без подавления; model — U-Net на синтетике')
    args = parser.parse_args()
    wall_model.DEFAULT_MODEL = args.model
    wall_model.THRESHOLD = args.threshold

    out_root = pathlib.Path(args.out)
    out_root.mkdir(parents=True, exist_ok=True)
    summaries = []

    if args.real:
        source = pathlib.Path(args.real)
        files = [p for p in sorted(source.rglob('*')) if p.suffix.lower() in grid.IMAGE_SUFFIXES]
        if not files:
            print('нет картинок: ' + str(source), file=sys.stderr)
            return 1
        for path in files:
            summaries.append(run_real(path, out_root, args.detector))
    else:
        cases_root = pathlib.Path(args.cases)
        if args.rebuild or not cases_root.exists():
            fixtures.build(cases_root)
        case_dirs = sorted(p for p in cases_root.iterdir() if p.is_dir())
        for case_dir in case_dirs:
            summaries.append(run_case(case_dir, out_root, args.annotator_cmd, args.detector))

    summary_path = out_root / 'summary.txt'
    lines = []
    for item in summaries:
        if item is None:
            continue
        line = item['name'] + '  сетка: ' + ('да' if item['grid'] else 'нет') \
               + '  контракт: ' + str(item['contract'])
        if item['score'] is not None:
            grid_report = item['score']['grid']
            if grid_report.get('known_limitation'):
                label = 'ожидаемо (ограничение прототипа)'
            elif grid_report['ok']:
                label = 'ok'
            else:
                label = 'расхождение'
            line += '  эталон: ' + label
        lines.append(line)
    summary_path.write_text(chr(10).join(lines) + chr(10), encoding='utf-8')
    print('итог: ' + str(summary_path))
    return 0


if __name__ == '__main__':
    sys.exit(main())


