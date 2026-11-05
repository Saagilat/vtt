#!/usr/bin/env python3
"""Проверяет ссылки в документации: цель существует и якорь в цели есть.

Ссылки бывают двух видов: `[текст](файл.md)` и `[текст](файл.md#заголовок)`. Якорь — это
заголовок в цели, приведённый к виду GitHub: строчные буквы, без знаков препинания, пробелы
заменены на дефис, повтор заголовка получает суффикс `-1`, `-2`.
"""
import pathlib
import re
import sys
import unicodedata

ROOT = pathlib.Path(__file__).resolve().parents[1]
LINK = re.compile(r'\[([^\]]+)\]\(([^)\s]+\.md)(?:#([^)\s]+))?\)')
HEADING = re.compile(r'^(#{1,6})\s+(.*)$')
CODE = re.compile(r'`([^`]*)`')


def documents():
    """Все markdown-файлы репозитория, кроме истории git."""
    return [p for p in sorted(ROOT.rglob('*.md')) if '.git' not in p.parts]


def slug(text):
    """Заголовок в том виде, в каком его делает якорь GitHub."""
    text = CODE.sub(r'\1', text)
    text = re.sub(r'\[([^\]]+)\]\([^)]*\)', r'\1', text)
    text = text.strip().lower()
    out = []
    for ch in text:
        if ch.isalnum() or ch in '-_':
            out.append(ch)
        elif ch.isspace():
            out.append('-')
        elif unicodedata.category(ch).startswith('M'):
            out.append(ch)
    return ''.join(out)


def anchors(path):
    """Множество якорей, которые есть в файле."""
    seen = {}
    result = set()
    for line in path.read_text(encoding='utf-8').split('\n'):
        m = HEADING.match(line)
        if not m:
            continue
        base = slug(m.group(2).replace('#', ''))
        count = seen.get(base, 0)
        seen[base] = count + 1
        result.add(base if count == 0 else '%s-%d' % (base, count))
    return result


def check():
    """Список проблем: нет файла, нет якоря."""
    bad = []
    cache = {}
    for path in documents():
        text = path.read_text(encoding='utf-8')
        for m in LINK.finditer(text):
            target, anchor = m.group(2), m.group(3)
            dest = (path.parent / target).resolve()
            where = path.relative_to(ROOT)
            if not dest.exists():
                bad.append('%s: нет файла %s' % (where, target))
                continue
            if anchor is None:
                continue
            if dest not in cache:
                cache[dest] = anchors(dest)
            if anchor not in cache[dest]:
                bad.append('%s: нет якоря #%s в %s' % (where, anchor, target))
    return bad


def main():
    bad = check()
    print('broken:', len(bad))
    for item in bad:
        print('  ', item)
    return 1 if bad else 0


if __name__ == '__main__':
    sys.exit(main())
