#!/usr/bin/env python3
"""Проверяет ссылки в документации: цель существует, номер из подписи в цели есть.

С `--fix` сначала перецеливает ссылки вида [ANN-Q1](...) на файл, где номер определён.
"""
import argparse
import os
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
HOME = {
    'ANN-': 'annotator/DECISIONS.md', 'ANN-Q': 'annotator/QUESTIONS.md',
    'CLI-': 'client/DECISIONS.md', 'CLI-Q': 'client/QUESTIONS.md',
    'SRV-': 'server/DECISIONS.md', 'SRV-Q': 'server/QUESTIONS.md',
    'CTR-': 'contract/DECISIONS.md', 'CTR-Q': 'contract/DECISIONS.md',
    'LAU-': 'launcher/DECISIONS.md', 'LAU-Q': 'launcher/QUESTIONS.md',
}
PREFIXES = ('ANN-Q', 'CLI-Q', 'SRV-Q', 'CTR-Q', 'LAU-Q',
            'ANN-', 'CLI-', 'SRV-', 'CTR-', 'LAU-')
LINK = re.compile(r'\[([^\]]+)\]\(([^)#\s]+\.md)\)')
NUMBER = re.compile(r'\b(?:D\d\d|Q\d{1,2}|(?:ANN|CLI|SRV|CTR|LAU)-Q?\d+)\b')


def documents():
    """Все markdown-файлы репозитория, кроме истории git."""
    return [p for p in sorted(ROOT.rglob('*.md')) if '.git' not in p.parts]


def home_of(label):
    """Префикс номера из подписи ссылки, если подпись начинается с номера."""
    head = label.split('–')[0]
    for key in PREFIXES:
        if re.fullmatch(key + r'\d+', head):
            return key
    return None


def retarget():
    """Перецеливает ссылки с номерами на файл проекта. Возвращает число правок."""
    fixed = 0
    for path in documents():
        text = path.read_text(encoding='utf-8')

        def repl(m):
            nonlocal fixed
            label, target = m.group(1), m.group(2)
            key = home_of(label)
            if key is None:
                return m.group(0)
            want = (ROOT / HOME[key]).resolve()
            if want == (path.parent / target).resolve():
                return m.group(0)
            rel = os.path.relpath(want, path.parent)
            fixed += 1
            print('  %s: %s -> %s' % (path.relative_to(ROOT), label, rel))
            return '[%s](%s)' % (label, rel)

        new = LINK.sub(repl, text)
        if new != text:
            path.write_text(new, encoding='utf-8')
    return fixed


def check():
    """Список проблем: цели нет на диске или номера из подписи нет в цели."""
    bad = []
    for path in documents():
        text = path.read_text(encoding='utf-8')
        for m in LINK.finditer(text):
            label, target = m.group(1), m.group(2)
            dest = (path.parent / target).resolve()
            if not dest.exists():
                bad.append('%s: нет файла %s' % (path.relative_to(ROOT), target))
                continue
            body = dest.read_text(encoding='utf-8')
            for number in NUMBER.findall(label):
                if number not in body:
                    bad.append('%s: %s отсутствует в %s'
                               % (path.relative_to(ROOT), number, target))
    return bad


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fix', action='store_true', help='перецелить ссылки перед проверкой')
    args = parser.parse_args()
    if args.fix:
        print('retargeted:', retarget())
    bad = check()
    print('broken:', len(bad))
    for item in bad:
        print('  ', item)
    return 1 if bad else 0


if __name__ == '__main__':
    sys.exit(main())
