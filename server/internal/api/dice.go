package api

import (
	"fmt"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
)

// dicePattern — один сорт кубика и необязательный модификатор: 1d20, 3d6+2.
// Сложные формулы (несколько сортов, перебросы) — следующий срез.
var dicePattern = regexp.MustCompile(`^([0-9]{0,3})d([0-9]{1,4})([+-][0-9]{1,4})?$`)

type rollResult struct {
	Formula  string
	Values   []int
	Modifier int
	Total    int
}

// parseRoll разбирает формулу и бросает кубы. Результат считает сервер:
// клиент присылает формулу, а не готовое число.
func parseRoll(formula string) (rollResult, error) {
	compact := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(formula), " ", ""))
	match := dicePattern.FindStringSubmatch(compact)
	if match == nil {
		return rollResult{}, fmt.Errorf("формула кубов: нужно как 1d20+3")
	}
	count := 1
	if match[1] != "" {
		parsed, err := strconv.Atoi(match[1])
		if err != nil || parsed < 1 {
			return rollResult{}, fmt.Errorf("кубов должно быть от 1 до 100")
		}
		count = parsed
	}
	if count > 100 {
		return rollResult{}, fmt.Errorf("кубов должно быть от 1 до 100")
	}
	sides, err := strconv.Atoi(match[2])
	if err != nil || sides < 2 || sides > 1000 {
		return rollResult{}, fmt.Errorf("граней должно быть от 2 до 1000")
	}
	modifier := 0
	if match[3] != "" {
		modifier, err = strconv.Atoi(match[3])
		if err != nil || modifier < -1000 || modifier > 1000 {
			return rollResult{}, fmt.Errorf("модификатор от -1000 до 1000")
		}
	}
	values := make([]int, count)
	total := modifier
	for i := range values {
		values[i] = rand.IntN(sides) + 1
		total += values[i]
	}
	normalized := fmt.Sprintf("%dd%d", count, sides)
	if modifier > 0 {
		normalized += fmt.Sprintf("+%d", modifier)
	} else if modifier < 0 {
		normalized += strconv.Itoa(modifier)
	}
	return rollResult{Formula: normalized, Values: values, Modifier: modifier, Total: total}, nil
}
