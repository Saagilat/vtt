package annotation_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Saagilat/vtt/server/internal/annotation"
)

// TestParse — разбор без поблажек.
//
// NaN и Infinity — единственное место, где Go и Python расходятся на входе: Go считает их
// синтаксической ошибкой, Python — числом. Код отказа у обоих должен быть один, поэтому
// разбор различает «в файле NaN» и «файл не разбирается», и различает по-настоящему:
// слово внутри строки — это строка, а не число.
func TestParse(t *testing.T) {
	cases := []struct {
		name string
		data string
		code string
	}{
		{"обычный файл", `{"a": 1, "b": "текст"}`, ""},
		{"NaN", `{"a": NaN}`, annotation.CodeNonFiniteNumber},
		{"Infinity", `{"a": Infinity}`, annotation.CodeNonFiniteNumber},
		{"минус бесконечность", `{"a": -Infinity}`, annotation.CodeNonFiniteNumber},
		{"NaN в строке", `{"a": "NaN"}`, ""},
		{"Infinity в строке", `{"a": "Infinity"}`, ""},
		{"NaN в ключе", `{"NaN": 1}`, ""},
		{"NaN с хвостом", `{"a": NaNx}`, annotation.CodeNotJSON},
		{"минус перед NaN", `{"a": -NaN}`, annotation.CodeNotJSON},
		{"оборванный файл", `{"a": 1`, annotation.CodeNotJSON},
		{"лишняя запятая", `{"a": 1,}`, annotation.CodeNotJSON},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			_, err := annotation.Parse([]byte(one.data))
			if one.code == "" {
				if err != nil {
					t.Fatalf("ждали разбор, получили %v", err)
				}
				return
			}
			var parse *annotation.ParseError
			if !errors.As(err, &parse) {
				t.Fatalf("ждали отказ %s, получили %v", one.code, err)
			}
			if parse.Code != one.code {
				t.Fatalf("получили %s, ждали %s (%s)", parse.Code, one.code, parse.Reason)
			}
		})
	}
}

// TestParseKeepsNumber — число из файла не проходит через float64: у стены с координатой
// 2^60 последний бит по дороге не теряется, и предел сверяется с настоящим числом.
func TestParseKeepsNumber(t *testing.T) {
	document, err := annotation.Parse([]byte(`{"a": 1152921504606846977}`))
	if err != nil {
		t.Fatalf("разбор не удался: %v", err)
	}
	file, _ := document.(map[string]any)
	number, ok := file["a"].(json.Number)
	if !ok {
		t.Fatalf("ждали json.Number, получили %T", file["a"])
	}
	if number.String() != "1152921504606846977" {
		t.Fatalf("число потеряло точность: %s", number)
	}
}

// TestVerdictJSON — вердикт пишется так же, как у Python-валидатора: одна строка JSON,
// у согласия код null, а не пустая строка.
func TestVerdictJSON(t *testing.T) {
	cases := []struct {
		name    string
		verdict annotation.Verdict
		want    string
	}{
		{
			"отказ",
			annotation.Fail(annotation.CodeDoorNotOnWall, "дверь мимо стены"),
			`{"verdict":"error","code":"door_not_on_wall","message":"дверь мимо стены"}`,
		},
		{
			"согласие",
			annotation.OK(""),
			`{"verdict":"ok","code":null,"message":"разметка соответствует контракту v1"}`,
		},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			data, err := json.Marshal(one.verdict)
			if err != nil {
				t.Fatalf("вердикт не пишется: %v", err)
			}
			if string(data) != one.want {
				t.Fatalf("получили %s, ждали %s", data, one.want)
			}
		})
	}
}

// TestNotAnObject — файл, который не объект, отвергается по форме, и путь в отказе — «файл»,
// а не пустая строка: человек читает текст, а не сегменты пути.
func TestNotAnObject(t *testing.T) {
	validator := newValidator(t)
	for _, data := range []string{`[]`, `null`, `"аннотация"`} {
		document, err := annotation.Parse([]byte(data))
		if err != nil {
			t.Fatalf("%s не разбирается: %v", data, err)
		}
		verdict := validator.Check(document, nil)
		if verdict.Code != annotation.CodeSchema {
			t.Fatalf("%s: получили %s/%s, ждали %s",
				data, verdict.Status, orDash(verdict.Code), annotation.CodeSchema)
		}
		if !strings.HasPrefix(verdict.Message, "файл: ") {
			t.Fatalf("%s: отказ начинается не с «файл»: %s", data, verdict.Message)
		}
	}
}

// TestFirstErrorFollowsNumberOrder — из нескольких нарушений называется то, что раньше
// по пути в файле, и номер элемента сравнивается как число: стена 9 раньше стены 10.
// Сравнение «по строке» поставило бы десятую перед девятой, и на живом файле с десятком
// стен коды разъехались бы с Python.
func TestFirstErrorFollowsNumberOrder(t *testing.T) {
	validator := newValidator(t)
	file := exampleDocument(t)
	withWalls(11)(file)
	walls, _ := file["walls"].([]any)
	for _, index := range []int{9, 10} {
		walls[index] = map[string]any{
			"id":     fmt.Sprintf("w%d", index+1),
			"points": []any{[]any{json.Number("100.5"), number(100)}, p(300, 200)},
		}
	}

	verdict := validator.Check(file, nil)
	if verdict.Code != annotation.CodeSchema {
		t.Fatalf("получили %s/%s, ждали %s", verdict.Status, orDash(verdict.Code), annotation.CodeSchema)
	}
	if !strings.Contains(verdict.Message, "/walls/9/points/0") {
		t.Fatalf("названо не то нарушение: %s", verdict.Message)
	}
}
