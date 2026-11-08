#!/usr/bin/env python3
# Стенд разметчика: мини-кликер для ручной разметки одной карты.
#
# Поднимает локальный сервер (только 127.0.0.1), отдаёт картинку и страницу с канвасом.
# Размечать можно стены (полилинии), двери (отрезки), области (полигоны) и сетку.
# Сохранение — в annotation.json по контракту v1; файл проверяется тем же валидатором,
# что и сервер. Карта остаётся у мастера: в git ничего не пишется.
#
# Использование:
#   python3 scripts/experiments/annotator/labeler.py --image 'тестовый материал/01.jpg' \
#       --out /tmp/01-expected.json

import argparse
import json
import pathlib
import sys
import webbrowser
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from PIL import Image

HERE = pathlib.Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(REPO / 'scripts'))

import grid  # noqa: E402
import validate_annotation as contract  # noqa: E402


class State:
    def __init__(self, image_path, out_path):
        self.image_path = pathlib.Path(image_path)
        self.out_path = pathlib.Path(out_path)
        self.image = Image.open(self.image_path).convert('RGB')
        self.width, self.height = self.image.size
        self.grid = {'mode': 'none'}
        _, analysis = grid.analyze(self.image_path)
        if analysis['is_grid']:
            side = round((analysis['vertical']['step'] + analysis['horizontal']['step']) / 2.0, 2)
            origin_x = int(round(analysis['vertical']['offset'] + side / 2.0))
            origin_y = int(round(analysis['horizontal']['offset'] + side / 2.0))
            self.grid = {'mode': 'square', 'origin': [origin_x, origin_y],
                         'square': {'side_px': side}}

    def load_existing(self):
        if not self.out_path.exists():
            return None
        try:
            return json.loads(self.out_path.read_text(encoding='utf-8'))
        except (OSError, json.JSONDecodeError):
            return None
class Handler(BaseHTTPRequestHandler):
    state: State

    def log_message(self, format, *args):
        sys.stderr.write((format % args) + chr(10))

    def send_data(self, body, content_type, status=200):
        self.close_connection = True
        reason = self.responses.get(status, ('OK',))[0]
        head = ('HTTP/1.0 %d %s\r\n'
                'Content-Type: %s\r\n'
                'Content-Length: %d\r\n'
                'Connection: close\r\n'
                '\r\n') % (status, reason, content_type, len(body))
        try:
            self.connection.sendall(head.encode('latin-1') + body)
        except Exception as error:
            sys.stderr.write('send error: ' + repr(error) + chr(10))
            sys.stderr.flush()

    def send_json(self, value, status=200):
        body = (json.dumps(value, ensure_ascii=False) + chr(10)).encode('utf-8')
        self.send_data(body, 'application/json; charset=utf-8', status)

    def do_GET(self):
        if self.path == '/':
            body = (HERE / 'labeler.html').read_bytes()
            self.send_data(body, 'text/html; charset=utf-8')
        elif self.path == '/image':
            suffix = self.state.image_path.suffix.lower()
            content_type = 'image/png' if suffix == '.png' else 'image/jpeg'
            self.send_data(self.state.image_path.read_bytes(), content_type)
        elif self.path == '/test':
            self.send_data(b'a' * 20000, 'text/plain; charset=utf-8')
        elif self.path == '/meta':
            self.send_json({'width': self.state.width, 'height': self.state.height,
                            'grid': self.state.grid})
        elif self.path == '/annotation':
            self.send_json(self.state.load_existing())
        else:
            self.send_json({'error': 'not found'}, 404)

    def do_POST(self):
        if self.path != '/save':
            self.send_json({'error': 'not found'}, 404)
            return
        length = int(self.headers.get('Content-Length', '0'))
        try:
            document = json.loads(self.rfile.read(length).decode('utf-8'))
        except (json.JSONDecodeError, UnicodeDecodeError) as error:
            self.send_json({'verdict': 'error', 'code': 'not_json', 'message': str(error)}, 400)
            return
        verdict = contract.check(document)
        if verdict.verdict != 'ok':
            self.send_json({'verdict': 'error', 'code': verdict.code,
                            'message': verdict.message}, 400)
            return
        self.state.out_path.parent.mkdir(parents=True, exist_ok=True)
        self.state.out_path.write_text(json.dumps(document, ensure_ascii=False, indent=2) + chr(10),
                                       encoding='utf-8')
        self.send_json({'verdict': 'ok', 'code': None,
                        'message': 'разметка записана: ' + str(self.state.out_path)})


def main():
    parser = argparse.ArgumentParser(description='Стенд: ручная разметка карты')
    parser.add_argument('--image', required=True)
    parser.add_argument('--out', required=True, help='куда писать annotation.json')
    parser.add_argument('--port', type=int, default=8765)
    parser.add_argument('--no-open', action='store_true')
    args = parser.parse_args()

    state = State(args.image, args.out)
    Handler.state = state
    server = ThreadingHTTPServer(('127.0.0.1', args.port), Handler)
    url = 'http://127.0.0.1:%d/' % args.port
    print('размечаем: ' + state.image_path.name)
    print('картинка: %dx%d' % (state.width, state.height))
    print('откройте: ' + url)
    print('сохранение: ' + str(state.out_path))
    if not args.no_open:
        webbrowser.open(url)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print('остановлено')
    finally:
        server.server_close()
    return 0


if __name__ == '__main__':
    sys.exit(main())

