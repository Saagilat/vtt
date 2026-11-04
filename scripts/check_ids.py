#!/usr/bin/env python3
"""Проверяет, что каждый номер, упомянутый в тексте, где-то определён, и что номера не дублируются."""
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[1]
FILES = [p for p in ROOT.rglob('*.md') if '.git' not in p.parts]
DEFINITIONS = [('docs/02-decisions.md', r'D\d\d'), ('docs/03-open-questions.md', r'Q\d{1,2}')]
FOLDERS = [('client', 'CLI'), ('server', 'SRV'), ('contract', 'CTR'),
           ('annotator', 'ANN'), ('launcher', 'LAU')]


def definitions():
    """Номер -> где определён; плюс список дублей."""
    defined, dups = {}, []

    def note(key, where):
        if key in defined:
            dups.append('%s: %s и %s' % (key, defined[key], where))
        else:
            defined[key] = where

    for rel, prefix in DEFINITIONS:
        text = (ROOT / rel).read_text(encoding='utf-8')
        for m in re.finditer(r'(?m)^### (' + prefix + r')\.', text):
            note(m.group(1), rel)
    for folder, prefix in FOLDERS:
        for p in sorted((ROOT / folder).glob('*.md')):
            text = p.read_text(encoding='utf-8')
            for m in re.finditer(r'(?m)^### (' + prefix + r'-Q?\d+)\.', text):
                note(m.group(1), str(p.relative_to(ROOT)))

    # вопросы в таблицах 03 и закрытые строки тоже считаются определёнными
    t03 = (ROOT / 'docs/03-open-questions.md').read_text(encoding='utf-8')
    for m in re.finditer(r'(?m)^(?:\| |- \*\*)(Q\d{1,2})[. ]', t03):
        note(m.group(1), 'docs/03-open-questions.md')
    # «куда переехало» легализует старые номера
    for p in FILES:
        for m in re.finditer(r'@@|(\bD\d\d\b|\bQ\d{1,2}\b) → ', p.read_text(encoding='utf-8')):
            if m.group(1):
                note(m.group(1), 'переехало: ' + str(p.relative_to(ROOT)))
    return defined, dups


def unknown(defined):
    """Упоминания номеров, которых нигде нет."""
    bad = []
    for p in FILES:
        text = p.read_text(encoding='utf-8')
        for m in re.finditer(r'(?<![A-Za-z0-9_-])(D\d\d|Q\d{1,2}|(?:ANN|CLI|SRV|CTR|LAU)-Q?\d+)(?!\d)', text):
            if m.group(1) not in defined:
                bad.append('%s: %s' % (p.relative_to(ROOT), m.group(1)))
    return bad


def main():
    defined, dups = definitions()
    bad = unknown(defined)
    print('defined:', len(defined))
    print('duplicates:', len(dups))
    for d in dups:
        print('  ', d)
    print('unknown refs:', len(bad))
    for b in sorted(set(bad)):
        print('  ', b)
    return 1 if dups or bad else 0


if __name__ == '__main__':
    sys.exit(main())
