// Package ui — окно лаунчера: тот же движок, но человек видит прогресс и кнопки.
//
// Окно ничего не решает: оно передаёт каталог установки и показывает строки, которые пишет
// internal/app. Поэтому консольный режим остаётся равноправным, а приёмка клиента работает
// без окна — флаги командной строки никуда не делись.
package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/Saagilat/vtt/launcher/internal/app"
)

// keepLines — сколько последних строк журнала держим в окне: остальное не помещается
// и не нужно, а память у программы, которая стоит до установки, должна быть скромной.
const keepLines = 200

// Run открывает окно установки и запуска и возвращает код возврата последнего действия.
func Run(installDir string) int {
	a := fyneapp.NewWithID("ru.vtt.launcher")
	w := a.NewWindow("VTT — установка и запуск")
	w.Resize(fyne.NewSize(560, 460))

	status := widget.NewLabel("Готовлюсь…")
	logArea := widget.NewMultiLineEntry()
	logArea.Wrapping = fyne.TextWrapWord
	logArea.Disable()
	out := &logWriter{area: logArea}

	bar := widget.NewProgressBarInfinite()
	bar.Hide()

	code := app.ExitOK
	busy := false
	var run func(check bool)
	play := widget.NewButton("Играть", func() { run(false) })
	check := widget.NewButton("Проверить", func() { run(true) })
	quit := widget.NewButton("Закрыть", func() { a.Quit() })

	setBusy := func(value bool) {
		busy = value
		if value {
			bar.Show()
			bar.Start()
			play.Disable()
			check.Disable()
			return
		}
		bar.Stop()
		bar.Hide()
		play.Enable()
		check.Enable()
	}

	run = func(isCheck bool) {
		if busy {
			return
		}
		setBusy(true)
		status.SetText("Работаю…")
		out.Reset()
		cfg := app.Config{InstallDir: installDir, Check: isCheck, Out: out}
		go func() {
			got := app.Run(context.Background(), cfg)
			fyne.Do(func() {
				code = got
				setBusy(false)
				if got == app.ExitOK {
					status.SetText("Готово")
					return
				}
				status.SetText(fmt.Sprintf("Не получилось: код %d — подробности в журнале", got))
			})
		}()
	}

	top := container.NewVBox(status, bar)
	bottom := container.NewHBox(play, check, quit)
	w.SetContent(container.NewBorder(top, bottom, nil, nil, logArea))
	// Первый запуск ведёт себя как ярлык: обновить недостающее и запустить клиент.
	a.Lifecycle().SetOnStarted(func() { go run(false) })
	w.Show()
	a.Run()
	return code
}

// logWriter показывает строки движка в окне и не даёт журналу расти без предела.
type logWriter struct {
	mu   sync.Mutex
	area *widget.Entry
	text string
}

func (l *logWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.text += string(p)
	if lines := strings.Count(l.text, "\n"); lines > keepLines {
		parts := strings.SplitN(l.text, "\n", lines-keepLines+1)
		l.text = parts[len(parts)-1]
	}
	text := l.text
	l.mu.Unlock()
	fyne.Do(func() { l.area.SetText(text) })
	return len(p), nil
}

// Reset очищает журнал перед новым действием.
func (l *logWriter) Reset() {
	l.mu.Lock()
	l.text = ""
	l.mu.Unlock()
	fyne.Do(func() { l.area.SetText("") })
}
