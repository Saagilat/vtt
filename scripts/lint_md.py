#!/usr/bin/env python3
"""Гигиена markdown: пустые строки, колонки таблиц, длина строки, следы истории и номеров."""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
FILES = [p for p in ROOT.rglob('*.md') if not {'.git', '.venv'} & set(p.parts)]

# Заголовки и пометки, которыми в тексте держали историю. Документ описывает только то,
# что верно сейчас, поэтому таких слов в нём быть не должно.
HISTORY = re.compile(
    r'\*\*(Почему|Уточнено|Дополнено|Отменено|Отменяет|Отменён)\b'
    r'|^(Уже закрыто|Закрыто|Что было|Куда переехало)\s*$'
    r'|❓')
# Номера решений и вопросов: `D05`, `Q42`, `CLI-02`, `SRV-Q7`. Их больше нет — ссылка идёт
# на заголовок.
NUMBERS = re.compile(r'\b(?:D\d{2}|Q\d{1,2}|(?:ANN|CLI|SRV|CTR|LAU)-Q?\d+)\b')
MAX_LINE = 100


def check(path):
    """Список проблем в одном файле."""
    bad = []
    name = path.relative_to(ROOT)
    lines = path.read_text(encoding='utf-8').split('\n')
    prev_blank = False
    fence = False
    for i, line in enumerate(lines, 1):
        if line.startswith('```'):
            fence = not fence
        if line.strip() == '':
            if prev_blank:
                bad.append('%s:%d двойная пустая строка' % (name, i))
            prev_blank = True
        else:
            prev_blank = False
        if HISTORY.search(line):
            bad.append('%s:%d след истории: %s' % (name, i, line.strip()[:60]))
        for number in NUMBERS.findall(line):
            bad.append('%s:%d номер решения или вопроса: %s' % (name, i, number))
        # длину проверяем только у прозы: таблицы и код живут по своим правилам
        if fence or line.startswith(('|', '>')):
            continue
        if len(line) > MAX_LINE:
            bad.append('%s:%d строка длиннее %d символов: %d'
                       % (name, i, MAX_LINE, len(line)))
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
                           % (name, block[0][0], sorted(counts)))
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
