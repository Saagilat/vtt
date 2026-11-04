#!/usr/bin/env python3
"""Мелкая гигиена markdown: двойные пустые строки и разное число колонок в таблицах."""
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
FILES = [p for p in ROOT.rglob('*.md') if '.git' not in p.parts]


def check(path):
    """Список проблем в одном файле."""
    bad = []
    lines = path.read_text(encoding='utf-8').split('\n')
    prev_blank = False
    for i, line in enumerate(lines, 1):
        if line.strip() == '':
            if prev_blank:
                bad.append('%s:%d двойная пустая строка' % (path.relative_to(ROOT), i))
            prev_blank = True
        else:
            prev_blank = False
    # таблицы: подряд идущие строки |...| должны иметь одинаковое число колонок
    block = []
    for i, line in enumerate(lines + [''], 1):
        if line.startswith('|'):
            block.append((i, line.count('|')))
            continue
        if len(block) > 2:
            counts = {c for _, c in block}
            if len(counts) > 1:
                bad.append('%s:%d таблица с разным числом колонок %s'
                           % (path.relative_to(ROOT), block[0][0], sorted(counts)))
        block = []
    return bad


def main():
    bad = []
    for path in sorted(FILES):
        bad += check(path)
    print('problems:', len(bad))
    for item in bad:
        print('  ', item)
    return 1 if bad else 0


if __name__ == '__main__':
    sys.exit(main())
