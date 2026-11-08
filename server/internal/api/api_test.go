package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogoutRevokesToken(t *testing.T) {
	handler, _, _ := fixture(t)
	token := login(t, handler, "alice")
	if rec := do(t, handler, "GET", "/v1/me", "", token); rec.Code != http.StatusOK {
		t.Fatalf("до выхода: код %d, тело %s", rec.Code, rec.Body)
	}
	if rec := do(t, handler, "DELETE", "/v1/sessions/current", "", token); rec.Code != http.StatusNoContent {
		t.Fatalf("выход: код %d, ожидали 204", rec.Code)
	}
	if rec := do(t, handler, "GET", "/v1/me", "", token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("после выхода токен ещё работает: код %d", rec.Code)
	}
}

func TestSessionExpires(t *testing.T) {
	handler, _, clk := fixture(t)
	token := login(t, handler, "alice")
	clk.Advance(SessionTTL + time.Second)
	if rec := do(t, handler, "GET", "/v1/me", "", token); rec.Code != http.StatusUnauthorized {
		t.Fatalf("истёкший токен принят: код %d", rec.Code)
	}
}

func TestBadLoginLooksTheSame(t *testing.T) {
	// Неверный пароль и несуществующий логин неразличимы — иначе вход
	// превращается в проверку "кто есть на сервере".
	handler, _, _ := fixture(t)
	wrong := do(t, handler, "POST", "/v1/sessions", `{"login":"alice","password":"нет"}`, "")
	unknown := do(t, handler, "POST", "/v1/sessions", `{"login":"nobody","password":"нет"}`, "")
	if wrong.Code != unknown.Code || wrong.Body.String() != unknown.Body.String() {
		t.Fatalf("ответы различаются: %d %s и %d %s",
			wrong.Code, wrong.Body, unknown.Code, unknown.Body)
	}
}

func TestBusyWhilePasswordChecked(t *testing.T) {
	dir := &blockingDirectory{started: make(chan struct{}), release: make(chan struct{})}
	handler, err := New(Config{
		Directory: dir,
		Sessions:  NewMemory(),
		Now:       func() time.Time { return testStart },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	first := make(chan int, 1)
	go func() {
		rec := do(t, handler, "POST", "/v1/sessions", `{"login":"alice","password":"x"}`, "")
		first <- rec.Code
	}()
	<-dir.started

	second := do(t, handler, "POST", "/v1/sessions", `{"login":"bob","password":"x"}`, "")
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("вторая проверка: код %d, ожидали 503", second.Code)
	}
	if !strings.Contains(second.Body.String(), `"busy"`) {
		t.Fatalf("вторая проверка: тело %s", second.Body)
	}

	close(dir.release)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("первая проверка: код %d, ожидали 200", code)
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("без зависимостей сервер собрался")
	}
	if _, err := New(Config{Directory: NewMemory()}); err == nil {
		t.Fatal("без Sessions сервер собрался")
	}
}

func do(t *testing.T, handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// blockingDirectory держит проверку пароля, пока тест не отпустит: так видно,
// что вторая попытка получает busy, а не ждёт молча.
type blockingDirectory struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (d *blockingDirectory) Authenticate(login, _ string) (Account, error) {
	d.once.Do(func() { close(d.started) })
	<-d.release
	return Account{ID: "a1", Login: login}, nil
}

func (d *blockingDirectory) ByID(id string) (Account, bool) {
	return Account{ID: id, Login: "alice"}, true
}

func (d *blockingDirectory) Campaigns(string) []Campaign { return nil }
