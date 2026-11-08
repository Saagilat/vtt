package annotation

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

// limits — коллекции, у которых нарушенный предел числа элементов получает свой код:
// «слишком много стен» не должно путаться с опечаткой в поле. Сами числа живут в схеме,
// второй их копии здесь нет.
var limits = map[string]bool{"walls": true, "doors": true, "areas": true}

// DoorWallTolerancePx — допуск «дверь на стене», в пикселях. Число продублировано из
// FORMAT.md §12 намеренно: Python-валидатор держит его у себя, и расхождение в нём ловится
// случаем door-not-on-wall на границе допуска.
const DoorWallTolerancePx = 2.0

// Validator — схема контракта, собранная один раз.
//
// Схема у Python и у Go одна и та же — contract/annotation.v1.schema.json, — поэтому форму
// оба понимают одинаково по построению, а не по обещанию.
type Validator struct {
	schema *jsonschema.Schema
}

// NewValidator собирает проверку из байтов схемы.
func NewValidator(schemaJSON []byte) (*Validator, error) {
	document, err := unmarshal(schemaJSON)
	if err != nil {
		return nil, fmt.Errorf("схема не разбирается: %w", err)
	}
	const schemaURL = "file:///annotation.v1.schema.json"
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaURL, document); err != nil {
		return nil, fmt.Errorf("схема не принята: %w", err)
	}
	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("схема не собирается: %w", err)
	}
	return &Validator{schema: schema}, nil
}

// LoadSchema читает схему из файла — тот самый файл, что читает Python-валидатор.
func LoadSchema(path string) (*Validator, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return NewValidator(data)
}

// Check — полный вердикт в порядке FORMAT.md §12: форма, картинка, геометрия.
func (v *Validator) Check(document any, image *ImageInfo) Verdict {
	if verdict := v.checkSchema(document); !verdict.Good() {
		return verdict
	}
	if verdict := checkImage(document, image); !verdict.Good() {
		return verdict
	}
	return checkGeometry(document)
}

// CheckFile — разбор и проверка файла.
//
// Ошибку возвращает только чтение: непрочитанный файл — беда запуска, а неразобранный —
// вердикт. Путать их нельзя, иначе сервер ответит мастеру «внутренняя ошибка» там,
// где виноват файл.
func (v *Validator) CheckFile(path string, image *ImageInfo) (Verdict, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Verdict{}, err
	}
	document, err := Parse(data)
	if err != nil {
		var parse *ParseError
		if !errors.As(err, &parse) {
			return Verdict{}, err
		}
		return Fail(parse.Code, filepath.Base(path)+": "+parse.Reason), nil
	}
	return v.Check(document, image), nil
}

// checkSchema сверяет форму и переводит нарушенное слово схемы в код отказа.
func (v *Validator) checkSchema(document any) Verdict {
	err := v.schema.Validate(document)
	if err == nil {
		return OK("")
	}
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return Fail(CodeSchema, err.Error())
	}
	leaf := firstError(validation)
	code := CodeSchema
	switch {
	case limits[first(leaf.InstanceLocation)] && isMaxItems(leaf):
		// У предела свой код: иначе «слишком много стен» не отличить от опечатки в файле.
		code = CodeTooManyElements
	case first(leaf.InstanceLocation) == "image" && isMaximum(leaf):
		code = CodeImageTooLarge
	}
	where := instancePath(leaf.InstanceLocation)
	return Fail(code, fmt.Sprintf("%s: %s (%s)", where, hint(leaf.ErrorKind), libraryText(leaf)))
}

// firstError — то нарушение, которое назвал бы Python: он собирает все и берёт первое
// по пути в файле. Порядок обхода у библиотеки свой и от запуска к запуску разный, поэтому
// выбираем сами — иначе два валидатора назвали бы разные ошибки на одном и том же файле.
func firstError(root *jsonschema.ValidationError) *jsonschema.ValidationError {
	var first *jsonschema.ValidationError
	walkLeaves(root, func(leaf *jsonschema.ValidationError) {
		if first == nil || comparePaths(leaf.InstanceLocation, first.InstanceLocation) < 0 {
			first = leaf
		}
	})
	if first == nil {
		return root
	}
	return first
}

// walkLeaves обходит дерево ошибок до листьев: только у листа нарушено конкретное слово,
// а узлы выше — это «а внутри вот что».
func walkLeaves(err *jsonschema.ValidationError, visit func(*jsonschema.ValidationError)) {
	if len(err.Causes) == 0 {
		visit(err)
		return
	}
	for _, cause := range err.Causes {
		walkLeaves(cause, visit)
	}
}

// comparePaths сравнивает пути как Python: по сегментам, числа — как числа («10» после «9»,
// а не перед «2»), короткий путь раньше длинного.
func comparePaths(left, right []string) int {
	for index := 0; index < len(left) && index < len(right); index++ {
		if left[index] == right[index] {
			continue
		}
		leftNumber, leftErr := strconv.Atoi(left[index])
		rightNumber, rightErr := strconv.Atoi(right[index])
		if leftErr == nil && rightErr == nil {
			return cmp.Compare(leftNumber, rightNumber)
		}
		return strings.Compare(left[index], right[index])
	}
	return cmp.Compare(len(left), len(right))
}

// instancePath повторяет вид пути из Python: «/doors/0/type», а у самого файла — «файл».
func instancePath(location []string) string {
	if len(location) == 0 {
		return "файл"
	}
	return "/" + strings.Join(location, "/")
}

// first — первый сегмент пути: коллекция или поле, к которому относится нарушение.
func first(location []string) string {
	if len(location) == 0 {
		return ""
	}
	return location[0]
}

// isMaxItems — нарушен предел числа элементов.
func isMaxItems(err *jsonschema.ValidationError) bool {
	_, ok := err.ErrorKind.(*kind.MaxItems)
	return ok
}

// isMaximum — нарушен потолок значения.
func isMaximum(err *jsonschema.ValidationError) bool {
	_, ok := err.ErrorKind.(*kind.Maximum)
	return ok
}

// libraryText — текст самой библиотеки в скобках. Он на английском, как и у Python-валидатора:
// это цитата из библиотеки, а не наш текст, и переводить её — значит разойтись с ней словами.
func libraryText(err *jsonschema.ValidationError) string {
	return err.ErrorKind.LocalizedString(message.NewPrinter(language.Russian))
}

// hint — что значит нарушенное слово схемы, по-русски. Текст читает человек, код сравнивают
// машины: текст можно менять свободно, код — только вместе с контрактом.
func hint(errorKind jsonschema.ErrorKind) string {
	switch errorKind.(type) {
	case *kind.AdditionalProperties:
		return "лишнее поле — такого в формате нет"
	case *kind.Required:
		return "не хватает обязательного поля"
	case *kind.Type:
		return "не тот тип значения"
	case *kind.Enum:
		return "значение не из списка"
	case *kind.Const:
		return "не то значение"
	case *kind.Pattern:
		return "строка не того вида"
	case *kind.Minimum:
		return "значение меньше допустимого"
	case *kind.Maximum:
		return "значение больше допустимого"
	case *kind.ExclusiveMinimum:
		return "значение должно быть строго больше допустимого"
	case *kind.ExclusiveMaximum:
		return "значение должно быть строго меньше допустимого"
	case *kind.MinItems:
		return "слишком мало элементов"
	case *kind.MaxItems:
		return "слишком много элементов"
	case *kind.DependentRequired:
		return "поле требует пары и без неё не значит ничего"
	case *kind.Not:
		return "поле запрещено в этом режиме"
	case *kind.AnyOf:
		return "не подходит ни один из вариантов"
	case *kind.AllOf:
		return "нарушено одно из условий"
	default:
		return "форма не соответствует схеме"
	}
}
