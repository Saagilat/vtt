"""Командная строка подготовки карты — `prep/README.md`, «Интерфейс».

Коды возврата те же, что у разметчика: 0 — файл записан, 1 — вход не годится, 2 — внутренняя
ошибка. Шагов подготовки здесь пока нет: каркас отвечает «ещё не реализована» и не пишет файл.
"""

from __future__ import annotations

import argparse
import sys

from . import __version__

PROG = "vtt-prep"


class Parser(argparse.ArgumentParser):
    """Негодные аргументы — это «вход не годится», то есть код 1, а не внутренняя ошибка."""

    def error(self, message: str) -> None:
        self.print_usage(sys.stderr)
        print("%s: %s" % (PROG, message), file=sys.stderr)
        raise SystemExit(1)


def build_parser() -> Parser:
    """Разбор аргументов: ровно те два флага, о которых договорились с клиентом."""
    parser = Parser(
        prog=PROG,
        description="Подготовка карты: формат, поля, предел размера, стирание печатной сетки",
    )
    parser.add_argument("--version", action="version", version="%s %s" % (PROG, __version__))
    parser.add_argument("--in", dest="source", metavar="КАРТА", help="исходная картинка")
    parser.add_argument("--out", dest="target", metavar="КАРТА", help="подготовленная картинка")
    return parser


def main(argv: list[str] | None = None) -> int:
    """Один вход — один код возврата; прогресс и предупреждения идут в stderr."""
    parser = build_parser()
    args = parser.parse_args(argv)
    if not args.source or not args.target:
        parser.error("нужны --in и --out")
    print("%s: подготовка карты ещё не реализована" % PROG, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
