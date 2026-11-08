"""Запуск без установки: `python3 -m vtt_prep`."""

import sys

from .cli import main

if __name__ == "__main__":
    sys.exit(main())
