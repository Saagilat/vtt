# schema — формат разметки карты

- `annotation.v1.schema.json` — json-схема (draft 2020-12): структура, обязательные поля,
  какие поля бывают при каком режиме сетки. Словами то же самое — [05](../docs/05-map-annotation.md).
- `annotation.v1.example.json` — пример на семь элементов: квадратная сетка, четыре стены,
  дверь, вода и одна правка руками.

Схема не выражает всё: точки внутри картинки, уникальность `id`, ссылки `wall_id` на
существующую стену, стирание только в производной копии — это проверяет
`tools/validate_annotation.py`.

```sh
python3 tools/validate_annotation.py                       # пример и все карты из tests/
python3 tools/validate_annotation.py tests/01-square-70px/annotation.json
python3 tools/validate_annotation.py --self-test           # проверить сам валидатор
```
