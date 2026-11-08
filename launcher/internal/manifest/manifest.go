// Package manifest читает latest.json — контракт между релизом и лаунчером (MANIFEST.md).
//
// Разбор строгий намеренно: незнакомое поле, незнакомое имя пакета, платформа не из закрытого
// списка или две записи с одним именем и платформой — ошибка, а не "ладно, пропустим".
// Тишина не должна быть согласием, иначе подмена манифеста проходит молча.
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
const Format = 2

// Имена пакетов. Список закрытый: client обязателен, остальные ставятся действием человека.
const (
	NameClient    = "client"
	NameVoice     = "voice"
	NameAnnotator = "annotator"
)

// Платформы и архитектуры — закрытые списки. Новое значение (arm64, darwin) — новая версия
// формата, а не догадка: неизвестное значение здесь ошибка разбора.
const (
	OSWindows = "windows"
	OSLinux   = "linux"
	ArchAMD64 = "amd64"
)

// ErrUnknownFormat — манифест новее, чем понимает лаунчер. Не ошибка поставки: с таким
// манифестом лаунчер продолжает работать с тем, что установлено (MANIFEST.md, правило 3).
var ErrUnknownFormat = errors.New("незнакомая версия формата манифеста")

// Platform — платформа сборки: ось и архитектура.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// String — windows/amd64: так платформа называется в тексте и в списке platforms.
func (p Platform) String() string {
	return p.OS + "/" + p.Arch
}

// Package — одна запись из packages.
type Package struct {
	Name      string `json:"name"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Version   string `json:"version"`
	File      string `json:"file"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MinClient string `json:"min_client,omitempty"`
}

// Platform записи: та же пара, что в полях os и arch.
func (p Package) Platform() Platform {
	return Platform{OS: p.OS, Arch: p.Arch}
}

// Manifest — разобранный latest.json.
type Manifest struct {
	Format    int        `json:"manifest"`
	Release   string     `json:"release"`
	Platforms []Platform `json:"platforms"`
	Packages  []Package  `json:"packages"`
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
	declared, err := m.checkPlatforms()
	if err != nil {
		return err
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
		platform := p.Platform()
		if err := checkPlatform(platform); err != nil {
			return fmt.Errorf("%s (%s): %w", where, p.Name, err)
		}
		if !declared[platform] {
			return fmt.Errorf("%s (%s): %s нет в platforms", where, p.Name, platform)
		}
		key := p.Name + "@" + platform.String()
		if seen[key] {
			return fmt.Errorf("%s: name: пакет %q для %s встречается дважды", where, p.Name, platform)
		}
		seen[key] = true
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
	return m.checkClients()
}

// checkPlatforms разбирает объявленный состав релиза и возвращает множество платформ.
func (m *Manifest) checkPlatforms() (map[Platform]bool, error) {
	if len(m.Platforms) == 0 {
		return nil, errors.New("latest.json: platforms: пусто, а платформы обязательны")
	}
	declared := make(map[Platform]bool, len(m.Platforms))
	for i, platform := range m.Platforms {
		if err := checkPlatform(platform); err != nil {
			return nil, fmt.Errorf("latest.json: platforms[%d]: %w", i, err)
		}
		if declared[platform] {
			return nil, fmt.Errorf("latest.json: platforms[%d]: %s встречается дважды", i, platform)
		}
		declared[platform] = true
	}
	return declared, nil
}

// checkClients: на каждой объявленной платформе клиент ровно один — иначе поставка пустая
// или неоднозначная, и человек с такой платформой не знает, что получит.
func (m *Manifest) checkClients() error {
	for _, platform := range m.Platforms {
		count := 0
		for _, p := range m.Packages {
			if p.Name == NameClient && p.Platform() == platform {
				count++
			}
		}
		if count == 0 {
			return fmt.Errorf("latest.json: platforms: для %s нет client", platform)
		}
	}
	return nil
}

// ClientFor возвращает обязательный пакет для платформы. Ошибка — это факт о релизе,
// а не поломка манифеста: сборки для этой системы в нём просто нет.
func (m *Manifest) ClientFor(target Platform) (Package, error) {
	if p, ok := m.Find(NameClient, target); ok {
		return p, nil
	}
	return Package{}, fmt.Errorf("в релизе %s нет сборки для %s", m.Release, target)
}

// Find ищет пакет по имени и платформе.
func (m *Manifest) Find(name string, target Platform) (Package, bool) {
	for _, p := range m.Packages {
		if p.Name == name && p.Platform() == target {
			return p, true
		}
	}
	return Package{}, false
}

// ForPlatform — записи для одной платформы в порядке манифеста.
func (m *Manifest) ForPlatform(target Platform) []Package {
	out := make([]Package, 0, len(m.Packages))
	for _, p := range m.Packages {
		if p.Platform() == target {
			out = append(out, p)
		}
	}
	return out
}

func knownPackage(name string) bool {
	switch name {
	case NameClient, NameVoice, NameAnnotator:
		return true
	default:
		return false
	}
}

func checkPlatform(p Platform) error {
	if p.OS != OSWindows && p.OS != OSLinux {
		return fmt.Errorf("os: незнакомая платформа %q (бывают %s, %s)", p.OS, OSWindows, OSLinux)
	}
	if p.Arch != ArchAMD64 {
		return fmt.Errorf("arch: незнакомая архитектура %q (бывает %s)", p.Arch, ArchAMD64)
	}
	return nil
}

// checkFileName: file — имя файла в том же релизе, а не адрес. Манифест не должен уметь
// уводить лаунчер на чужой хостинг или за пределы каталога установки.
func checkFileName(file string) error {
	switch {
	case file == "":
		return errors.New("пусто")
	case file != filepath.Base(file) || strings.ContainsAny(file, `/\`):
		return fmt.Errorf("%q — это путь, а нужно имя файла в релизе", file)
	case file == "." || file == "..":
		return fmt.Errorf("%q — не имя файла", file)
	case !strings.HasPrefix(file, "vtt-"):
		return fmt.Errorf("%q: имя начинается с vtt-", file)
	}
	switch {
	case strings.HasSuffix(file, ".zip"), strings.HasSuffix(file, ".exe"):
		return nil
	case strings.HasSuffix(file, ".tar.gz"), strings.HasSuffix(file, ".tgz"):
		return nil
	case strings.HasSuffix(file, ".deb"):
		return nil
	default:
		return fmt.Errorf("%q: ожидается .zip, .exe, .tar.gz или .deb", file)
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
