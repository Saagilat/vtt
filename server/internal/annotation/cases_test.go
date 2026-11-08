package annotation_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Saagilat/vtt/server/internal/annotation"
)

// contractDir — контракт от каталога пакета (server/internal/annotation).
var contractDir = filepath.Join("..", "..", "..", "contract")

// newValidator читает ту же схему, что читает Python-валидатор.
func newValidator(t *testing.T) *annotation.Validator {
	t.Helper()
	validator, err := annotation.LoadSchema(filepath.Join(contractDir, "annotation.v1.schema.json"))
	if err != nil {
		t.Fatalf("схема контракта не читается: %v", err)
	}
	return validator
}

// expectation — то, что лежит в expect.json случая.
type expectation struct {
	Verdict string `json:"verdict"`
	Code    string `json:"code"`
	What    string `json:"what"`
}

// readExpectation читает ожидание случая: вердикт, код и объяснение для человека.
func readExpectation(t *testing.T, path string) expectation {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ожидание не читается: %v", err)
	}
	var expect expectation
	if err := json.Unmarshal(data, &expect); err != nil {
		t.Fatalf("ожидание не разбирается: %v", err)
	}
	return expect
}

// orDash — код так, как его печатает Python-валидатор: у согласия кода нет, и это «—».
func orDash(code string) string {
	if code == "" {
		return "—"
	}
	return code
}

// exampleDocument читает пример контракта так же, как его читает валидатор: числа —
// json.Number, структура — объекты и массивы.
func exampleDocument(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractDir, "annotation.v1.example.json"))
	if err != nil {
		t.Fatalf("пример контракта не читается: %v", err)
	}
	document, err := annotation.Parse(data)
	if err != nil {
		t.Fatalf("пример контракта не разбирается: %v", err)
	}
	file, ok := document.(map[string]any)
	if !ok {
		t.Fatal("пример контракта — не объект")
	}
	return file
}

// TestCases гоняет набор contract/cases: у Python и у Go один набор и один ответ.
//
// Расхождение здесь — это два разных контракта под одним именем, и чинить его надо
// в контракте, а не в одном из валидаторов: сервер и разметчик должны понимать один файл
// одинаково, иначе годная на машине мастера карта отвергается на сервере.
func TestCases(t *testing.T) {
	validator := newValidator(t)
	casesDir := filepath.Join(contractDir, "cases")
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		t.Fatalf("набор случаев не читается: %v", err)
	}

	checked := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		checked++
		t.Run(entry.Name(), func(t *testing.T) {
			dir := filepath.Join(casesDir, entry.Name())
			expect := readExpectation(t, filepath.Join(dir, "expect.json"))

			var image *annotation.ImageInfo
			if _, err := os.Stat(filepath.Join(dir, "image.json")); err == nil {
				image, err = annotation.LoadImageInfo(filepath.Join(dir, "image.json"))
				if err != nil {
					t.Fatalf("image.json не читается: %v", err)
				}
			}

			verdict, err := validator.CheckFile(filepath.Join(dir, "annotation.json"), image)
			if err != nil {
				t.Fatalf("файл случая не читается: %v", err)
			}
			if verdict.Status != expect.Verdict || verdict.Code != expect.Code {
				t.Fatalf("получили %s/%s, ждали %s/%s: %s",
					verdict.Status, orDash(verdict.Code),
					expect.Verdict, orDash(expect.Code), verdict.Message)
			}
		})
	}
	if checked == 0 {
		t.Fatal("в contract/cases нет ни одного случая — проверять нечего")
	}
}
