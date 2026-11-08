// Package annotation — проверка разметки карты по контракту v1 (contract/FORMAT.md).
//
// Один вход — один вердикт: ok либо error с кодом из закрытого списка (contract/README.md).
// Порядок проверок — форма, картинка, геометрия: пока не сошлась форма, говорить «стена
// за краем» бессмысленно. Форму держит схема contract/annotation.v1.schema.json, смысловые
// правила — код этого пакета, и оба обязаны совпадать с scripts/validate_annotation.py
// на наборе contract/cases: расхождение там — это два разных контракта под одним именем.
package annotation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Вердикты.
const (
	VerdictOK    = "ok"
	VerdictError = "error"
)

// Коды отказов — закрытый список из contract/README.md.
const (
	CodeNotJSON           = "not_json"
	CodeNonFiniteNumber   = "non_finite_number"
	CodeSchema            = "schema"
	CodeTooManyElements   = "too_many_elements"
	CodeImageTooLarge     = "image_too_large"
	CodeImageSizeMismatch = "image_size_mismatch"
	CodeImageHashMismatch = "image_sha256_mismatch"
	CodeDuplicateID       = "duplicate_id"
	CodeZeroLengthSegment = "zero_length_segment"
	CodePolygonNotClosed  = "polygon_not_closed"
	CodePointOutsideImage = "point_outside_image"
	CodeDoorWallMissing   = "door_wall_missing"
	CodeDoorNotOnWall     = "door_not_on_wall"
)

// Verdict — ответ на один файл.
//
// У годного файла кода нет, и в JSON он выходит null, а не пустой строкой: «кода нет»
// и «код пустой» — разные вещи, и путать их незачем.
type Verdict struct {
	Status  string
	Code    string
	Message string
}

// MarshalJSON пишет вердикт так же, как его пишет Python-валидатор.
func (v Verdict) MarshalJSON() ([]byte, error) {
	type wire struct {
		Verdict string  `json:"verdict"`
		Code    *string `json:"code"`
		Message string  `json:"message"`
	}
	var code *string
	if v.Code != "" {
		code = &v.Code
	}
	return json.Marshal(wire{Verdict: v.Status, Code: code, Message: v.Message})
}

// Good — файл годен.
func (v Verdict) Good() bool { return v.Status == VerdictOK }

// OK — файл годен. Пустой текст заменяется обычным: отказ объясняют, а согласие — нет.
func OK(message string) Verdict {
	if message == "" {
		message = "разметка соответствует контракту v1"
	}
	return Verdict{Status: VerdictOK, Message: message}
}

// Fail — файл отвергнут: код из закрытого списка и текст для человека.
func Fail(code, message string) Verdict {
	return Verdict{Status: VerdictError, Code: code, Message: message}
}

// ImageInfo — что сервер получил картинкой: размер после разбора и хеш принятых байт.
//
// Хеш необязателен: файл `image.json` в наборе случаев заменяет саму картинку, а хеш
// в нём есть не всегда.
type ImageInfo struct {
	Width  int
	Height int
	Sha256 *string
}

// LoadImageInfo читает то, что сервер получил картинкой: `{"width": …, "height": …, "sha256": …}`.
func LoadImageInfo(path string) (*ImageInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Width  int     `json:"width"`
		Height int     `json:"height"`
		Sha256 *string `json:"sha256"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &ImageInfo{Width: wire.Width, Height: wire.Height, Sha256: wire.Sha256}, nil
}

// ParseError — файл не разобрался. Code — `not_json` или `non_finite_number`.
type ParseError struct {
	Code   string
	Reason string
}

func (e *ParseError) Error() string { return e.Reason }

// Parse разбирает файл без поблажек.
//
// Числа остаются json.Number: 2^60, переведённое в float64, теряет последний бит, а сверять
// его потом с пределом надо точно.
//
// NaN и Infinity в JSON не бывают, и Go считает их синтаксической ошибкой. Python принимает
// их как числа и отвергает отдельным кодом `non_finite_number`, поэтому здесь есть второй
// шанс: если документ с этими словами вместо чисел всё-таки разбирается, код —
// `non_finite_number`, а не `not_json`. Иначе два валидатора назвали бы один файл по-разному.
func Parse(data []byte) (any, error) {
	document, err := unmarshal(data)
	if err == nil {
		return document, nil
	}
	if replaced, changed := replaceNonFinite(data); changed {
		if _, retry := unmarshal(replaced); retry == nil {
			return nil, &ParseError{
				Code:   CodeNonFiniteNumber,
				Reason: "в файле NaN или Infinity — такое значение ломает любой расчёт",
			}
		}
	}
	return nil, &ParseError{Code: CodeNotJSON, Reason: fmt.Sprintf("это не JSON (%v)", err)}
}

// unmarshal разбирает JSON без потери точности: числа становятся json.Number.
func unmarshal(data []byte) (any, error) {
	return jsonschema.UnmarshalJSON(bytes.NewReader(data))
}

// replaceNonFinite заменяет голые NaN и Infinity на 0 — только чтобы узнать, разобрался ли
// документ, не будь в нём этих слов. Внутри строк их не трогаем: "NaN" — законная строка,
// и таких в файле может быть сколько угодно.
func replaceNonFinite(data []byte) ([]byte, bool) {
	replaced := make([]byte, 0, len(data))
	changed := false
	inString := false
	for index := 0; index < len(data); index++ {
		char := data[index]
		switch {
		case inString:
			replaced = append(replaced, char)
			if char == '\\' && index+1 < len(data) {
				index++
				replaced = append(replaced, data[index])
				continue
			}
			if char == '"' {
				inString = false
			}
		case char == '"':
			inString = true
			replaced = append(replaced, char)
		case tokenAt(data, index, "NaN"):
			if index > 0 && data[index-1] == '-' {
				// «-NaN» числом не бывает и у Python тоже: пусть разбор падает как not_json.
				replaced = append(replaced, char)
				continue
			}
			replaced = append(replaced, '0')
			index += len("NaN") - 1
			changed = true
		case tokenAt(data, index, "Infinity"):
			// Минус перед Infinity остаётся: «-0» — такое же число.
			replaced = append(replaced, '0')
			index += len("Infinity") - 1
			changed = true
		default:
			replaced = append(replaced, char)
		}
	}
	return replaced, changed
}

// tokenAt — слово стоит целиком: ни буквы до, ни буквы после. Иначе это кусок мусора,
// а не литерал, и называть такой файл «NaN внутри» нельзя.
func tokenAt(data []byte, index int, token string) bool {
	if !bytes.HasPrefix(data[index:], []byte(token)) {
		return false
	}
	end := index + len(token)
	if end < len(data) && isWordByte(data[end]) {
		return false
	}
	return index == 0 || !isWordByte(data[index-1])
}

// isWordByte — символы, из которых состоят слова: буква, цифра, подчёркивание.
func isWordByte(char byte) bool {
	switch {
	case 'a' <= char && char <= 'z', 'A' <= char && char <= 'Z':
		return true
	case '0' <= char && char <= '9', char == '_':
		return true
	default:
		return false
	}
}
