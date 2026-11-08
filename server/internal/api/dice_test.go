package api

import "testing"

func TestParseRoll(t *testing.T) {
	for _, formula := range []string{"1d20", "d20", "2d6+3", "1d8 - 2", "100d1000"} {
		result, err := parseRoll(formula)
		if err != nil {
			t.Fatalf("%q: %v", formula, err)
		}
		if len(result.Values) == 0 {
			t.Fatalf("%q: нет значений", formula)
		}
		sum := result.Modifier
		for _, value := range result.Values {
			if value < 1 {
				t.Fatalf("%q: значение %d", formula, value)
			}
			sum += value
		}
		if sum != result.Total {
			t.Fatalf("%q: сумма %d, итог %d", formula, sum, result.Total)
		}
	}
}

func TestParseRollRejects(t *testing.T) {
	for _, formula := range []string{"", "20", "0d6", "1d1", "101d6", "1d6+2000", "1d6+1d4", "d"} {
		if _, err := parseRoll(formula); err == nil {
			t.Fatalf("%q принят", formula)
		}
	}
}
