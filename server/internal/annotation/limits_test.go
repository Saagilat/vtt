package annotation_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/Saagilat/vtt/server/internal/annotation"
)

// number и p — значения для файла, собранного в памяти. Числа — json.Number, как после
// разбора: собранный случай должен проходить тот же путь, что и настоящий файл.
func number(value int) json.Number { return json.Number(strconv.Itoa(value)) }

func p(x, y int) []any { return []any{number(x), number(y)} }

// withWalls — ровно count стен: базовые w1 и w2 остаются, на w2 ссылается дверь примера.
func withWalls(count int) func(map[string]any) {
	return func(document map[string]any) {
		walls, _ := document["walls"].([]any)
		for index := 3; index <= count; index++ {
			walls = append(walls, map[string]any{
				"id":     fmt.Sprintf("w%d", index),
				"points": []any{p(100, 100), p(300, 100+index%500)},
			})
		}
		document["walls"] = walls
	}
}

// withDoors — ровно count дверей на стене w2: все лежат на ней, поэтому предел — единственное,
// что может отказать.
func withDoors(count int) func(map[string]any) {
	return func(document map[string]any) {
		doors := make([]any, 0, count)
		for index := 1; index <= count; index++ {
			doors = append(doors, map[string]any{
				"id":      "d" + strconv.Itoa(index),
				"wall_id": "w2",
				"from":    p(850, 800),
				"to":      p(920, 800),
				"type":    "normal",
			})
		}
		document["doors"] = doors
	}
}

// withAreas — ровно count областей: полигон замкнут и внутри картинки.
func withAreas(count int) func(map[string]any) {
	return func(document map[string]any) {
		polygon := []any{p(1200, 900), p(1500, 900), p(1500, 1200), p(1200, 1200), p(1200, 900)}
		areas := make([]any, 0, count)
		for index := 1; index <= count; index++ {
			areas = append(areas, map[string]any{
				"id":      "a" + strconv.Itoa(index),
				"type":    "water",
				"polygon": polygon,
			})
		}
		document["areas"] = areas
	}
}

// withImageSide — картинка ровно side×side.
func withImageSide(side int) func(map[string]any) {
	return func(document map[string]any) {
		image, _ := document["image"].(map[string]any)
		image["width"] = number(side)
		image["height"] = number(side)
	}
}

// TestLimits — «ровно столько можно, на один больше — нет».
//
// Случаи строятся в памяти: держать в репозитории файл на 20 001 стену ради одного отказа
// незачем. Проверять предел всё равно надо — именно на нём два валидатора разойдутся, если
// один забудет число: числа повторяются в FORMAT.md, в схеме и в коде.
func TestLimits(t *testing.T) {
	validator := newValidator(t)
	example, err := os.ReadFile(filepath.Join(contractDir, "annotation.v1.example.json"))
	if err != nil {
		t.Fatalf("пример контракта не читается: %v", err)
	}

	sameSize := func() *annotation.ImageInfo {
		side := 8192
		return &annotation.ImageInfo{Width: side, Height: side}
	}

	cases := []struct {
		name    string
		mutate  func(map[string]any)
		image   *annotation.ImageInfo
		verdict string
		code    string
	}{
		{"стен 20 000", withWalls(20000), nil, annotation.VerdictOK, ""},
		{"стен 20 001", withWalls(20001), nil, annotation.VerdictError, annotation.CodeTooManyElements},
		{"дверей 5 000", withDoors(5000), nil, annotation.VerdictOK, ""},
		{"дверей 5 001", withDoors(5001), nil, annotation.VerdictError, annotation.CodeTooManyElements},
		{"областей 2 000", withAreas(2000), nil, annotation.VerdictOK, ""},
		{"областей 2 001", withAreas(2001), nil, annotation.VerdictError, annotation.CodeTooManyElements},
		{"картинка 8192", withImageSide(8192), sameSize(), annotation.VerdictOK, ""},
		{"картинка 8193", withImageSide(8193), nil, annotation.VerdictError, annotation.CodeImageTooLarge},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			document, err := annotation.Parse(example)
			if err != nil {
				t.Fatalf("пример не разбирается: %v", err)
			}
			file, ok := document.(map[string]any)
			if !ok {
				t.Fatal("пример — не объект")
			}
			one.mutate(file)

			verdict := validator.Check(file, one.image)
			if verdict.Status != one.verdict || verdict.Code != one.code {
				t.Fatalf("получили %s/%s, ждали %s/%s: %s",
					verdict.Status, orDash(verdict.Code), one.verdict, orDash(one.code),
					verdict.Message)
			}
		})
	}
}
