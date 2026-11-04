// Package semver сравнивает версии вида 1.4.0 и 0.3.1-rc1.
//
// Своя реализация, а не зависимость: лаунчер запускается первым, до всякой установки,
// поэтому он собирается только со стандартной библиотекой (LAU-02). Нужно от semver
// здесь ровно одно — сказать, какая из двух версий новее и годится ли строка как версия.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version — разобранная версия. Нулевое значение невалидно: версию получают только через Parse.
type Version struct {
	major, minor, patch int
	pre                 []string
}

// Parse разбирает MAJOR.MINOR.PATCH[-PRE][+BUILD]. Строгий: лишние знаки, пустые числа
// и один или два компонента — ошибка, а не «догадаемся». Метаданные сборки (+BUILD)
// разбираются и отбрасываются: они не участвуют в сравнении — так же, как в semver 2.0.0.
func Parse(s string) (Version, error) {
	var v Version
	core := s

	// Метаданные сборки — после '+', в сравнении не участвуют.
	if i := strings.IndexByte(core, '+'); i >= 0 {
		build := core[i+1:]
		if build == "" {
			return v, fmt.Errorf("версия %q: пустые метаданные сборки после «+»", s)
		}
		for _, part := range strings.Split(build, ".") {
			if part == "" {
				return v, fmt.Errorf("версия %q: пустая часть в метаданных сборки", s)
			}
		}
		core = core[:i]
	}

	// Предварительная версия — после '-'.
	if i := strings.IndexByte(core, '-'); i >= 0 {
		pre := core[i+1:]
		core = core[:i]
		if pre == "" {
			return v, fmt.Errorf("версия %q: пустая предварительная часть после «-»", s)
		}
		v.pre = strings.Split(pre, ".")
		for _, part := range v.pre {
			if part == "" {
				return v, fmt.Errorf("версия %q: пустая часть в предварительной версии", s)
			}
			for _, r := range part {
				ok := r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-'
				if !ok {
					return v, fmt.Errorf("версия %q: недопустимый символ %q", s, r)
				}
			}
		}
	}

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("версия %q: нужны три числа через точку, например 1.4.0", s)
	}
	nums := make([]int, 3)
	for i, part := range parts {
		n, err := parseNumber(part)
		if err != nil {
			return v, fmt.Errorf("версия %q: %w", s, err)
		}
		nums[i] = n
	}
	v.major, v.minor, v.patch = nums[0], nums[1], nums[2]
	return v, nil
}

// parseNumber принимает только цифры без знака и ведущих нулей: «01» — не число версии,
// а опечатка, и лучше сказать об этом сразу (то же правило, что в semver 2.0.0).
func parseNumber(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("пустое число")
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, fmt.Errorf("ведущий ноль в числе %q", s)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("не число: %q", s)
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("не число: %q", s)
	}
	return n, nil
}

// Valid — разбирается ли строка как версия.
func Valid(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// String возвращает версию в том же виде, в каком её пишут в манифесте.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
	if len(v.pre) > 0 {
		s += "-" + strings.Join(v.pre, ".")
	}
	return s
}

// Compare возвращает -1, 0 или 1: v старее, равна или новее other.
func (v Version) Compare(other Version) int {
	if c := compareInt(v.major, other.major); c != 0 {
		return c
	}
	if c := compareInt(v.minor, other.minor); c != 0 {
		return c
	}
	if c := compareInt(v.patch, other.patch); c != 0 {
		return c
	}
	// Предварительная версия старее той же версии без неё: 1.4.0-rc1 < 1.4.0.
	if len(v.pre) == 0 && len(other.pre) == 0 {
		return 0
	}
	if len(v.pre) == 0 {
		return 1
	}
	if len(other.pre) == 0 {
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(other.pre); i++ {
		if c := compareIdent(v.pre[i], other.pre[i]); c != 0 {
			return c
		}
	}
	return compareInt(len(v.pre), len(other.pre))
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// compareIdent: числовой идентификатор всегда старее буквенного (1.0.0-2 < 1.0.0-alpha).
func compareIdent(a, b string) int {
	an, aNum := numeric(a)
	bn, bNum := numeric(b)
	switch {
	case aNum && bNum:
		return compareInt(an, bn)
	case aNum:
		return -1
	case bNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}

func numeric(s string) (int, bool) {
	n, err := parseNumber(s)
	return n, err == nil
}
