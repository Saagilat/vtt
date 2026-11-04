package semver

import "testing"

func TestParseValid(t *testing.T) {
	cases := map[string]string{
		"1.4.0":         "1.4.0",
		"0.3.1-rc1":     "0.3.1-rc1",
		"1.0.0-alpha.1": "1.0.0-alpha.1",
		"1.4.0+build.5": "1.4.0", // метаданные сборки разбираются и отбрасываются
		"10.20.30":      "10.20.30",
	}
	for in, want := range cases {
		v, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): неожиданная ошибка: %v", in, err)
		}
		if got := v.String(); got != want {
			t.Errorf("Parse(%q).String() = %q, ожидали %q", in, got, want)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	cases := map[string]string{
		"пусто":            "",
		"два числа":        "1.4",
		"четыре числа":     "1.4.0.1",
		"ведущий ноль":     "1.04.0",
		"буква v":          "v1.4.0",
		"не число":         "1.4.x",
		"минус":            "-1.4.0",
		"пустая предверс.": "1.4.0-",
		"пустые метадан.":  "1.4.0+",
		"пробел":           " 1.4.0",
	}
	for name, s := range cases {
		if _, err := Parse(s); err == nil {
			t.Errorf("%s: Parse(%q) прошёл, а не должен", name, s)
		}
		if Valid(s) {
			t.Errorf("%s: Valid(%q) = true, а должен быть false", name, s)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.4.0", "1.3.9", 1},
		{"1.4.0", "1.4.0", 0},
		{"1.4.0", "1.4.0+build.5", 0},
		{"10.0.0", "9.9.9", 1},
		{"1.4.0-rc1", "1.4.0", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-2", "1.0.0-alpha", -1},
	}
	for _, c := range cases {
		a, err := Parse(c.a)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.a, err)
		}
		b, err := Parse(c.b)
		if err != nil {
			t.Fatalf("Parse(%q): %v", c.b, err)
		}
		if got := a.Compare(b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, ожидали %d", c.a, c.b, got, c.want)
		}
		if got, want := b.Compare(a), -c.want; got != want {
			t.Errorf("Compare(%s, %s) = %d, ожидали %d (обратный порядок)", c.b, c.a, got, want)
		}
	}
}
