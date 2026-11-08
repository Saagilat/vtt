#!/usr/bin/env python3
# Стенд разметчика: модель стен (U-Net, ONNX) поверх синтетики.
#
# Модель предсказывает маску стен плитками 256; маска превращается в отрезки Хафа и склеивается
# тем же кодом, что у классического baseline. Сетка гасится во входе модели, как и задумано:
# картинка и sha256 не меняются. Двери и области модель пока не ищет.

import argparse
import pathlib
import sys

import cv2
import numpy as np
import onnxruntime as ort
from PIL import Image

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import classical  # noqa: E402
import grid  # noqa: E402

TILE = 192
OVERLAP = 48
THRESHOLD = 0.5
DEFAULT_MODEL = 'scripts/experiments/annotator/models/v2/annotator_walls.onnx'


def load_session(model_path):
    return ort.InferenceSession(str(model_path), providers=['CPUExecutionProvider'])


def predict_mask(session, image, grid_result=None, suppress_grid=True, threshold=THRESHOLD):
    rgb = cv2.imread(str(image))
    height, width = rgb.shape[:2]
    if suppress_grid and grid_result is not None and grid_result['is_grid']:
        mask = classical.grid_mask((height, width), grid_result)
        rgb = cv2.inpaint(rgb, mask, 3, cv2.INPAINT_TELEA)

    probability = np.zeros((height, width), dtype=np.float32)
    weights = np.zeros((height, width), dtype=np.float32)
    positions = list(range(0, max(1, width - TILE + 1), TILE - OVERLAP))
    if positions[-1] != max(0, width - TILE):
        positions.append(max(0, width - TILE))
    columns = positions
    rows = list(range(0, max(1, height - TILE + 1), TILE - OVERLAP))
    if rows[-1] != max(0, height - TILE):
        rows.append(max(0, height - TILE))

    for y in rows:
        for x in columns:
            crop = rgb[y:y + TILE, x:x + TILE]
            if crop.shape[0] != TILE or crop.shape[1] != TILE:
                crop = cv2.copyMakeBorder(crop, 0, TILE - crop.shape[0], 0, TILE - crop.shape[1],
                                          cv2.BORDER_REPLICATE)
            tensor = (crop[:, :, ::-1].astype(np.float32) / 255.0).transpose(2, 0, 1)[None]
            logits = session.run(None, {'image': tensor})[0][0, 0]
            tile = 1.0 / (1.0 + np.exp(-logits))
            real_h = min(TILE, height - y)
            real_w = min(TILE, width - x)
            probability[y:y + real_h, x:x + real_w] += tile[:real_h, :real_w]
            weights[y:y + real_h, x:x + real_w] += 1.0
    probability /= np.maximum(weights, 1e-6)
    return (probability > threshold).astype(np.uint8) * 255


def walls_from_mask(mask):
    edges = cv2.Canny(mask, 50, 150)
    lines = cv2.HoughLinesP(edges, 1, np.pi / 180.0, threshold=20,
                            minLineLength=15, maxLineGap=8)
    segments = []
    if lines is not None:
        height, width = mask.shape
        for x1, y1, x2, y2 in lines.reshape(-1, 4):
            length = float(np.hypot(x2 - x1, y2 - y1))
            if length >= 15:
                segments.append(((int(x1), int(y1)), (int(x2), int(y2)), length))
    segments.sort(key=lambda item: item[2], reverse=True)
    merged = classical.merge_segments(segments[:1500])
    walls = []
    height, width = mask.shape
    for first, second in merged:
        first = (max(0, min(width - 1, first[0])), max(0, min(height - 1, first[1])))
        second = (max(0, min(width - 1, second[0])), max(0, min(height - 1, second[1])))
        if first == second:
            continue
        walls.append({'id': 'w%d' % (len(walls) + 1),
                      'points': [[first[0], first[1]], [second[0], second[1]]]})
    return walls


def detect(image_path, session, suppress_grid=True, threshold=THRESHOLD):
    image_path = pathlib.Path(image_path)
    result = grid.analyze(image_path)
    mask = predict_mask(session, image_path, result[1], suppress_grid=suppress_grid,
                        threshold=threshold)
    return {
        'format_version': 1,
        'image': {'width': result[1]['image']['width'], 'height': result[1]['image']['height']},
        'grid': classical.grid_from_detection(result[1]),
        'walls': walls_from_mask(mask),
        'doors': [],
        'areas': [],
    }


def main():
    parser = argparse.ArgumentParser(description='Стенд: модель стен')
    parser.add_argument('--image', required=True)
    parser.add_argument('--out', required=True)
    parser.add_argument('--model', default=DEFAULT_MODEL)
    parser.add_argument('--no-grid-suppression', action='store_true')
    parser.add_argument('--threshold', type=float, default=THRESHOLD)
    args = parser.parse_args()

    import json
    session = load_session(args.model)
    document = detect(args.image, session, suppress_grid=not args.no_grid_suppression,
                      threshold=args.threshold)
    pathlib.Path(args.out).write_text(json.dumps(document, ensure_ascii=False, indent=2) + chr(10),
                                      encoding='utf-8')
    print('стен: ' + str(len(document['walls'])))
    return 0


if __name__ == '__main__':
    sys.exit(main())
