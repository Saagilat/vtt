package annotation_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Saagilat/vtt/server/internal/annotation"
)

// TestExample — пример из контракта годен, и содержимое случая ok-example совпадает с ним
// один в один: это обещание contract/README.md, и проверять его должно что-то кроме глаз,
// иначе правка в одном файле оставит второй врать.
//
// Сравниваем содержимое, а не байты: в примере точки стены стоят в строку, в случае — по
// одной на линию. Это оформление, а не контракт, и расхождение в нём ничего не значит.
func TestExample(t *testing.T) {
	validator := newValidator(t)
	examplePath := filepath.Join(contractDir, "annotation.v1.example.json")

	verdict, err := validator.CheckFile(examplePath, nil)
	if err != nil {
		t.Fatalf("пример не читается: %v", err)
	}
	if !verdict.Good() {
		t.Fatalf("пример отвергнут: %s %s", verdict.Code, verdict.Message)
	}

	example := exampleDocument(t)
	caseFile := documentFromFile(t, filepath.Join(contractDir, "cases", "ok-example", "annotation.json"))
	if !reflect.DeepEqual(example, caseFile) {
		t.Fatal("случай ok-example и пример контракта разошлись по содержимому")
	}
}

// documentFromFile читает файл так же, как его читает валидатор.
func documentFromFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("файл не читается: %v", err)
	}
	document, err := annotation.Parse(data)
	if err != nil {
		t.Fatalf("файл не разбирается: %v", err)
	}
	file, ok := document.(map[string]any)
	if !ok {
		t.Fatalf("%s — не объект", path)
	}
	return file
}
