package annotation

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

// checkImage — размер и хеш в файле против того, что сервер получил картинкой.
//
// Размер раньше геометрии: иначе стена за краем получит код «за край», хотя настоящая
// причина в другой картинке.
func checkImage(document any, image *ImageInfo) Verdict {
	if image == nil {
		return OK("")
	}
	file, _ := document.(map[string]any)
	declared, _ := file["image"].(map[string]any)
	width := coordinate(declared["width"])
	height := coordinate(declared["height"])
	if width != float64(image.Width) || height != float64(image.Height) {
		return Fail(CodeImageSizeMismatch, fmt.Sprintf(
			"в файле %s×%s, а картинка %d×%d: координаты считались от другого размера, "+
				"вся геометрия смещена",
			numberText(width), numberText(height), image.Width, image.Height))
	}
	declaredHash, hasHash := declared["sha256"]
	if hasHash && image.Sha256 != nil && text(declaredHash) != *image.Sha256 {
		return Fail(CodeImageHashMismatch, fmt.Sprintf(
			"в файле sha256 %s, у картинки %s: разметку сделали по другой картинке того же "+
				"размера, и это единственное место, где такое видно",
			text(declaredHash), *image.Sha256))
	}
	return OK("")
}

// checkGeometry — правила, которых схема выразить не может (FORMAT.md §12).
//
// Порядок тот же, что у Python-валидатора, и это не вкусовщина: когда в файле несколько
// нарушений, оба валидатора обязаны назвать одно и то же, иначе один и тот же файл
// получает два разных кода.
func checkGeometry(document any) Verdict {
	file, _ := document.(map[string]any)
	image, _ := file["image"].(map[string]any)
	width := coordinate(image["width"])
	height := coordinate(image["height"])
	walls := elements(file["walls"])
	doors := elements(file["doors"])
	areas := elements(file["areas"])

	for _, collection := range []struct {
		name  string
		items []map[string]any
	}{{"walls", walls}, {"doors", doors}, {"areas", areas}} {
		seen := make(map[string]bool, len(collection.items))
		for _, item := range collection.items {
			id := text(item["id"])
			if seen[id] {
				return Fail(CodeDuplicateID, fmt.Sprintf(
					"%s: id %s повторяется — ссылка должна указывать на один элемент",
					collection.name, id))
			}
			seen[id] = true
		}
	}

	for _, wall := range walls {
		wallPoints := points(wall["points"])
		for index := 1; index < len(wallPoints); index++ {
			if wallPoints[index] == wallPoints[index-1] {
				return Fail(CodeZeroLengthSegment, fmt.Sprintf(
					"стена %s: соседние точки %s совпадают — такой участок ничего не "+
						"ограничивает и портит нормали видимости",
					text(wall["id"]), pointText(wallPoints[index])))
			}
		}
	}
	for _, door := range doors {
		if pointOf(door["from"]) == pointOf(door["to"]) {
			return Fail(CodeZeroLengthSegment, fmt.Sprintf(
				"дверь %s: %s — начало и конец совпадают, проёма нулевой длины не бывает",
				text(door["id"]), pointText(pointOf(door["from"]))))
		}
	}

	for _, area := range areas {
		polygon := points(area["polygon"])
		if polygon[0] != polygon[len(polygon)-1] {
			return Fail(CodePolygonNotClosed, fmt.Sprintf(
				"область %s: последняя пара %s не повторяет первую %s — незамкнутый полигон "+
					"молча меняет форму",
				text(area["id"]), pointText(polygon[len(polygon)-1]), pointText(polygon[0])))
		}
	}

	outside := func(at point) bool {
		return !(0 <= at.x && at.x < width && 0 <= at.y && at.y < height)
	}
	outsideVerdict := func(where string, at point) Verdict {
		return Fail(CodePointOutsideImage, fmt.Sprintf(
			"%s: точка %s вне картинки %s×%s; допустимо x 0…%s, y 0…%s",
			where, pointText(at), numberText(width), numberText(height),
			numberText(width-1), numberText(height-1)))
	}

	for _, wall := range walls {
		for _, at := range points(wall["points"]) {
			if outside(at) {
				return outsideVerdict("стена "+text(wall["id"]), at)
			}
		}
	}
	for _, door := range doors {
		for _, field := range []string{"from", "to"} {
			at := pointOf(door[field])
			if outside(at) {
				return outsideVerdict(fmt.Sprintf("дверь %s (%s)", text(door["id"]), field), at)
			}
		}
	}
	for _, area := range areas {
		for _, at := range points(area["polygon"]) {
			if outside(at) {
				return outsideVerdict("область "+text(area["id"]), at)
			}
		}
	}

	wallsByID := make(map[string][]point, len(walls))
	for _, wall := range walls {
		wallsByID[text(wall["id"])] = points(wall["points"])
	}

	for _, door := range doors {
		wallID, named := door["wall_id"]
		if !named {
			continue
		}
		if _, exists := wallsByID[text(wallID)]; !exists {
			return Fail(CodeDoorWallMissing, fmt.Sprintf(
				"дверь %s: стены %s нет в файле — номер перепутан или взят из другой карты",
				text(door["id"]), text(wallID)))
		}
	}

	for _, door := range doors {
		wallID, named := door["wall_id"]
		if !named {
			continue
		}
		wall := wallsByID[text(wallID)]
		start, end := pointOf(door["from"]), pointOf(door["to"])
		middle := point{x: (start.x + end.x) / 2, y: (start.y + end.y) / 2}
		for _, checked := range []struct {
			name string
			at   point
		}{{"from", start}, {"to", end}, {"середина", middle}} {
			distance := distanceToWall(checked.at, wall)
			if distance > DoorWallTolerancePx {
				return Fail(CodeDoorNotOnWall, fmt.Sprintf(
					"дверь %s: %s %s отстоит от стены %s на %.1f px, а допуск %g px — "+
						"такая дверь открывает проход мимо стены",
					text(door["id"]), checked.name, pointText(checked.at), text(wallID),
					distance, DoorWallTolerancePx))
			}
		}
	}
	return OK("")
}

// point — пара координат: как в файле, только уже числами.
type point struct {
	x, y float64
}

// elements — элементы коллекции. Схема уже проверила, что это массив объектов, поэтому
// проверять форму здесь нечего: не та форма — это расхождение схемы и кода, и о нём должен
// узнать разработчик из тестов, а не мастер из отказа.
func elements(value any) []map[string]any {
	list, _ := value.([]any)
	result := make([]map[string]any, 0, len(list))
	for _, item := range list {
		element, _ := item.(map[string]any)
		result = append(result, element)
	}
	return result
}

// points — ломаная или полигон из файла.
func points(value any) []point {
	list, _ := value.([]any)
	result := make([]point, len(list))
	for index, item := range list {
		result[index] = pointOf(item)
	}
	return result
}

// pointOf — одна пара координат.
func pointOf(value any) point {
	list, _ := value.([]any)
	return point{x: coordinate(list[0]), y: coordinate(list[1])}
}

// coordinate — значение из разобранного файла как число.
//
// Тип уже проверила схема, остаётся перевод из json.Number, а он не удаётся только на числе
// вне float64 (1e400): такая координата заведомо за краем картинки, и +Inf говорит об этом
// честно. Не число вовсе — NaN, и тогда сравнение с границей тоже даст «вне картинки».
func coordinate(value any) float64 {
	number, ok := value.(json.Number)
	if !ok {
		return math.NaN()
	}
	result, err := number.Float64()
	if err != nil {
		return math.Inf(1)
	}
	return result
}

// text — значение из файла как строка. Тип проверен схемой.
func text(value any) string {
	result, _ := value.(string)
	return result
}

// numberText — число так, как его читает человек: «2048», а не «2048.000000».
func numberText(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// pointText — точка так, как её печатает Python-валидатор: «[100, 800]».
func pointText(at point) string {
	return fmt.Sprintf("[%s, %s]", numberText(at.x), numberText(at.y))
}

// distanceToWall — от точки до ближайшего участка ломаной: стена состоит из отрезков,
// и «лежит на стене» значит «лежит на одном из них».
func distanceToWall(at point, wall []point) float64 {
	distance := math.Inf(1)
	for index := 1; index < len(wall); index++ {
		distance = math.Min(distance, distanceToSegment(at, wall[index-1], wall[index]))
	}
	return distance
}

// distanceToSegment — от точки до отрезка, а не до прямой: за концом отрезка расстояние
// считается до конца, иначе дверь «на стене» уезжала бы далеко за угол.
func distanceToSegment(at, start, end point) float64 {
	dx, dy := end.x-start.x, end.y-start.y
	if dx == 0 && dy == 0 {
		return math.Hypot(at.x-start.x, at.y-start.y)
	}
	t := ((at.x-start.x)*dx + (at.y-start.y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	return math.Hypot(at.x-(start.x+t*dx), at.y-(start.y+t*dy))
}
