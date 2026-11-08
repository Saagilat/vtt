"""Командная строка разметчика — `annotator/README.md`, «Интерфейс».

Коды возврата: 0 — файл записан, 1 — вход не годится, 2 — внутренняя ошибка. Модели, сетки
и векторизации здесь пока нет: каркас разбирает аргументы, отвечает «ещё не реализовано»
и ничего не пишет — частичный результат не записывается.
"""

from __future__ import annotations

import argparse
import sys

from . import __version__

PROG = "vtt-annotate"


class Parser(argparse.ArgumentParser):
    """Негодные аргументы — это «вход не годится», то есть код 1, а не внутренняя ошибка."""

    def error(self, message: str) -> None:
        self.print_usage(sys.stderr)
        print("%s: %s" % (PROG, message), file=sys.stderr)
        raise SystemExit(1)


def build_parser() -> Parser:
    """Ровно три режима из README: разметить, нарисовать найденное, проверить чужой файл."""
    parser = Parser(
        prog=PROG,
        description="Разметка карты: сетка, стены, двери и области из подготовленной картинки",
    )
    parser.add_argument("--version", action="version", version="%s %s" % (PROG, __version__))
    parser.add_argument("--image", metavar="КАРТА", help="подготовленная картинка")
    parser.add_argument("--out", metavar="ФАЙЛ", help="annotation.json или overlay.png")
    parser.add_argument("--render", action="store_true", help="нарисовать найденное в --out")
    parser.add_argument("--check", metavar="ФАЙЛ", help="проверить готовую разметку")
    parser.add_argument("--min-confidence", type=float, metavar="ЧИСЛО", help="порог от 0 до 1")
    return parser


def main(argv: list[str] | None = None) -> int:
    """Один вход — один код возврата; прогресс и предупреждения идут в stderr."""
    parser = build_parser()
    args = parser.parse_args(argv)
    if args.min_confidence is not None and not 0.0 <= args.min_confidence <= 1.0:
        parser.error("--min-confidence — число от 0 до 1")
    if args.check and (args.image or args.out):
        parser.error("--check не сочетается с --image и --out")
    if args.check:
        print("%s: проверка разметки ещё не реализована" % PROG, file=sys.stderr)
        return 2
    if not args.image or not args.out:
        parser.error("нужны --image и --out или --check")
    print("%s: разметка карты ещё не реализована" % PROG, file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
