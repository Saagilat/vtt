// Package manifest читает latest.json — контракт между релизом и лаунчером (MANIFEST.md).
//
// Разбор строгий намеренно: незнакомое поле, незнакомое имя пакета или две записи с одним
// именем — ошибка, а не «ладно, пропустим». Тишина не должна быть согласием, иначе подмена
// манифеста проходит молча (MANIFEST.md, правило 2).
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Saagilat/vtt/launcher/internal/semver"
)

// Format — версия формата манифеста, которую понимает этот код.
const Format = 1

// Имена пакетов. Список закрытый: `client` обязателен, остальные ставятся действием человека.
const (
	NameClient    = "client"
	NameVoice     = "voice"
	NameAnnotator = "annotator"
)

// ErrUnknownFormat — манифест новее, чем понимает лаунчер. Не ошибка поставки: с таким
// манифестом лаунчер продолжает работать с тем, что установлено (MANIFEST.md, правило 3).
var ErrUnknownFormat = errors.New("незнакомая версия формата манифеста")

// Package — одна запись из packages.
type Package struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	File      string `json:"file"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MinClient string `json:"min_client,omitempty"`
}

// Manifest — разобранный latest.json.
type Manifest struct {
	Format   int       `json:"manifest"`
	Release  string    `json:"release"`
	Packages []Package `json:"packages"`
}

// Parse разбирает и проверяет latest.json целиком: либо манифест, которому можно верить,
// либо ошибка с указанием поля.
func Parse(data []byte) (*Manifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("latest.json: %w", err)
	}
	if dec.More() {
		return nil, errors.New("latest.json: после объекта есть лишние данные")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate проверяет уже разобранный манифест по правилам MANIFEST.md.
func (m *Manifest) Validate() error {
	if m.Format != Format {
		return fmt.Errorf("%w: %d, понимаем только %d", ErrUnknownFormat, m.Format, Format)
	}
	if !semver.Valid(m.Release) {
		return fmt.Errorf("latest.json: release: %q — не версия вида 1.4.0", m.Release)
	}
	if len(m.Packages) == 0 {
		return errors.New("latest.json: packages: пусто, а client обязателен")
	}

	seen := make(map[string]bool, len(m.Packages))
	for i := range m.Packages {
		p := &m.Packages[i]
		where := fmt.Sprintf("latest.json: packages[%d]", i)
		if !knownPackage(p.Name) {
			return fmt.Errorf("%s: name: незнакомый пакет %q (бывают %s, %s, %s)",
				where, p.Name, NameClient, NameVoice, NameAnnotator)
		}
		if seen[p.Name] {
			return fmt.Errorf("%s: name: пакет %q встречается дважды", where, p.Name)
		}
		seen[p.Name] = true
		if !semver.Valid(p.Version) {
			return fmt.Errorf("%s (%s): version: %q — не версия вида 1.4.0", where, p.Name, p.Version)
		}
		if err := checkFileName(p.File); err != nil {
			return fmt.Errorf("%s (%s): file: %w", where, p.Name, err)
		}
		if err := checkSHA256(p.SHA256); err != nil {
			return fmt.Errorf("%s (%s): sha256: %w", where, p.Name, err)
		}
		if p.Size <= 0 {
			return fmt.Errorf("%s (%s): size: %d — размер должен быть больше нуля", where, p.Name, p.Size)
		}
		if p.MinClient != "" && !semver.Valid(p.MinClient) {
			return fmt.Errorf("%s (%s): min_client: %q — не версия вида 1.4.0", where, p.Name, p.MinClient)
		}
	}
	if !seen[NameClient] {
		return errors.New("latest.json: packages: нет client, а поставка без него пустая")
	}
	return nil
}

// Client возвращает обязательный пакет. Вызывать после Parse: он гарантирует, что client есть.
func (m *Manifest) Client() Package {
	p, ok := m.Find(NameClient)
	if !ok {
		panic("manifest: client отсутствует, манифест не прошёл Parse/Validate")
	}
	return p
}

// Find ищет пакет по имени.
func (m *Manifest) Find(name string) (Package, bool) {
	for _, p := range m.Packages {
		if p.Name == name {
			return p, true
		}
	}
	return Package{}, false
}

// Names возвращает имена пакетов в порядке манифеста.
func (m *Manifest) Names() []string {
	names := make([]string, 0, len(m.Packages))
	for _, p := range m.Packages {
		names = append(names, p.Name)
	}
	return names
}

func knownPackage(name string) bool {
	switch name {
	case NameClient, NameVoice, NameAnnotator:
		return true
	default:
		return false
	}
}

// checkFileName: file — имя файла в том же релизе, а не адрес. Манифест не должен уметь
// уводить лаунчер на чужой хостинг или за пределы каталога установки (MANIFEST.md).
func checkFileName(file string) error {
	switch {
	case file == "":
		return errors.New("пусто")
	case file != filepath.Base(file) || strings.ContainsAny(file, `/\`):
		return fmt.Errorf("%q — это путь, а нужно имя файла в релизе", file)
	case file == "." || file == "..":
		return fmt.Errorf("%q — не имя файла", file)
	case !strings.HasPrefix(file, "vtt-"):
		return fmt.Errorf("%q: имя начинается с vtt- (D41)", file)
	}
	switch {
	case strings.HasSuffix(file, ".zip"), strings.HasSuffix(file, ".exe"):
		return nil
	default:
		return fmt.Errorf("%q: ожидается .zip или .exe", file)
	}
}

func checkSHA256(s string) error {
	if len(s) != 64 {
		return fmt.Errorf("%q: нужно 64 шестнадцатеричных знака, а не %d", s, len(s))
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return fmt.Errorf("%q: hex в нижнем регистре, знак %q не подходит", s, r)
		}
	}
	return nil
}
