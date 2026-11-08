package manifest

import (
	"errors"
	"strings"
	"testing"
)

const sha = "9f2c4d0e1b7a9c55c40d81f2a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"

var (
	winAmd = Platform{OS: OSWindows, Arch: ArchAMD64}
	linAmd = Platform{OS: OSLinux, Arch: ArchAMD64}
)

// good — манифест, который обязан проходить: тот же набор полей, что в примере MANIFEST.md.
const good = `{
  "manifest": 2,
  "release": "1.4.0",
  "platforms": [
    {"os": "windows", "arch": "amd64"},
    {"os": "linux", "arch": "amd64"}
  ],
  "packages": [
    {"name": "client", "os": "windows", "arch": "amd64", "version": "1.4.0",
     "file": "vtt-client-1.4.0-win64.zip", "sha256": "` + sha + `", "size": 41234123},
    {"name": "client", "os": "linux", "arch": "amd64", "version": "1.4.0",
     "file": "vtt-client-1.4.0-linux-amd64.tar.gz", "sha256": "` + sha + `", "size": 39123456},
    {"name": "voice", "os": "windows", "arch": "amd64", "version": "0.3.1",
     "file": "vtt-voice-0.3.1-ru.zip", "sha256": "` + sha + `", "size": 81234567,
     "min_client": "1.4.0"}
  ]
}`

func TestParseGood(t *testing.T) {
	m, err := Parse([]byte(good))
	if err != nil {
		t.Fatalf("Parse(good): %v", err)
	}
	if m.Release != "1.4.0" || m.Format != Format {
		t.Fatalf("разобрали не то: format=%d release=%q", m.Format, m.Release)
	}
	if len(m.Platforms) != 2 {
		t.Fatalf("platforms = %v", m.Platforms)
	}
	if got, err := m.ClientFor(winAmd); err != nil || got.File != "vtt-client-1.4.0-win64.zip" {
		t.Fatalf("ClientFor(windows) = %+v, %v", got, err)
	}
	if got, err := m.ClientFor(linAmd); err != nil || got.File != "vtt-client-1.4.0-linux-amd64.tar.gz" {
		t.Fatalf("ClientFor(linux) = %+v, %v", got, err)
	}
	if got, ok := m.Find(NameVoice, winAmd); !ok || got.MinClient != "1.4.0" {
		t.Fatalf("Find(voice) = %+v, %v", got, ok)
	}
	if got := m.ForPlatform(linAmd); len(got) != 1 || got[0].Name != NameClient {
		t.Fatalf("ForPlatform(linux) = %+v", got)
	}
}

func TestClientForMissingPlatform(t *testing.T) {
	// Релиз собран не для всех систем — это факт о релизе, а не поломка манифеста.
	m, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ClientFor(Platform{OS: OSLinux, Arch: "arm64"}); err == nil ||
		!strings.Contains(err.Error(), "нет сборки") {
		t.Fatalf("ожидали ошибку нет сборки, получили %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			name: "незнакомое поле",
			json: strings.Replace(good, `"release": "1.4.0",`, `"release": "1.4.0", "extra": 1,`, 1),
			want: "unknown field",
		},
		{
			name: "незнакомый пакет",
			json: strings.Replace(good, `"name": "voice"`, `"name": "server"`, 1),
			want: "незнакомый пакет",
		},
		{
			name: "дубль пакета на платформе",
			json: strings.Replace(good, `{"name": "client", "os": "linux", "arch": "amd64"`, `{"name": "client", "os": "windows", "arch": "amd64"`, 1),
			want: "встречается дважды",
		},
		{
			name: "платформа не из списка",
			json: strings.Replace(good, `{"os": "linux", "arch": "amd64"}`, `{"os": "darwin", "arch": "amd64"}`, 1),
			want: "незнакомая платформа",
		},
		{
			name: "архитектура не из списка",
			json: strings.Replace(good, `{"os": "linux", "arch": "amd64"}`, `{"os": "linux", "arch": "arm64"}`, 1),
			want: "незнакомая архитектура",
		},
		{
			name: "у записи нет os",
			json: strings.Replace(good, `{"name": "client", "os": "windows", "arch": "amd64"`, `{"name": "client", "arch": "amd64"`, 1),
			want: "незнакомая платформа",
		},
		{
			name: "пакета нет в platforms",
			json: strings.Replace(good, `{"os": "windows", "arch": "amd64"},
    {"os": "linux", "arch": "amd64"}`, `{"os": "windows", "arch": "amd64"}`, 1),
			want: "нет в platforms",
		},
		{
			name: "platforms пусто",
			json: strings.Replace(good, `"platforms": [
    {"os": "windows", "arch": "amd64"},
    {"os": "linux", "arch": "amd64"}
  ],`, `"platforms": [],`, 1),
			want: "platforms: пусто",
		},
		{
			name: "platforms дважды",
			json: strings.Replace(good, `{"os": "linux", "arch": "amd64"}
  ],`, `{"os": "windows", "arch": "amd64"}
  ],`, 1),
			want: "встречается дважды",
		},
		{
			name: "хеш не hex",
			json: strings.Replace(good, sha, strings.Repeat("Z", 64), 1),
			want: "hex в нижнем регистре",
		},
		{
			name: "хеш короткий",
			json: strings.Replace(good, sha, "9f2c4d0e", 1),
			want: "64 шестнадцатеричных",
		},
		{
			name: "размер ноль",
			json: strings.Replace(good, `"size": 41234123`, `"size": 0`, 1),
			want: "больше нуля",
		},
		{
			name: "file — путь",
			json: strings.Replace(good, `vtt-client-1.4.0-win64.zip`, `sub/client.zip`, 1),
			want: "это путь",
		},
		{
			name: "file — наверх",
			json: strings.Replace(good, `vtt-client-1.4.0-win64.zip`, `../client.zip`, 1),
			want: "это путь",
		},
		{
			name: "file без vtt-",
			json: strings.Replace(good, `vtt-client-1.4.0-win64.zip`, `client.zip`, 1),
			want: "начинается с vtt-",
		},
		{
			name: "file не архив",
			json: strings.Replace(good, `vtt-client-1.4.0-win64.zip`, `vtt-client-1.4.0-win64.bin`, 1),
			want: "ожидается",
		},
		{
			name: "release не версия",
			json: strings.Replace(good, `"release": "1.4.0"`, `"release": "1.4"`, 1),
			want: "release",
		},
		{
			name: "min_client не версия",
			json: strings.Replace(good, `"min_client": "1.4.0"`, `"min_client": "1.4"`, 1),
			want: "min_client",
		},
		{
			name: "лишние данные после объекта",
			json: good + `{"manifest": 2}`,
			want: "лишние данные",
		},
		{
			name: "пусто вместо json",
			json: ``,
			want: "EOF",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.json))
			if err == nil {
				t.Fatalf("Parse прошёл, а не должен")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ошибка %q, ожидали упоминание %q", err, c.want)
			}
		})
	}
}

func TestParseRejectsNoClient(t *testing.T) {
	// Поставка без client пустая: правильное сообщение именно про его отсутствие.
	only := `{"manifest": 2, "release": "1.4.0",
		"platforms": [{"os": "windows", "arch": "amd64"}],
		"packages": [{"name": "voice", "os": "windows", "arch": "amd64", "version": "0.3.1",
		"file": "vtt-voice-0.3.1-ru.zip", "sha256": "` + sha + `", "size": 1}]}`
	if _, err := Parse([]byte(only)); err == nil || !strings.Contains(err.Error(), "нет client") {
		t.Fatalf("ожидали ошибку про отсутствие client, получили %v", err)
	}
}

func TestUnknownFormatIsRecognisable(t *testing.T) {
	// Незнакомая версия формата — не ошибка поставки: лаунчер должен уметь отличить её
	// от "манифест битый" и продолжить работать с установленным (MANIFEST.md, правило 3).
	bumped := strings.Replace(good, `"manifest": 2`, `"manifest": 3`, 1)
	_, err := Parse([]byte(bumped))
	if !errors.Is(err, ErrUnknownFormat) {
		t.Fatalf("ожидали ErrUnknownFormat, получили %v", err)
	}
}

func TestParseRejectsEmptyPackages(t *testing.T) {
	empty := `{"manifest": 2, "release": "1.4.0",
		"platforms": [{"os": "windows", "arch": "amd64"}], "packages": []}`
	_, err := Parse([]byte(empty))
	if err == nil || !strings.Contains(err.Error(), "пусто") {
		t.Fatalf("ожидали ошибку про пустой список, получили %v", err)
	}
}
