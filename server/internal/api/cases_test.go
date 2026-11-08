package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Пароли и время фиксированы: случай сравнивает ответ целиком, и случайные
// значения в нём были бы непроверяемыми.
var (
	testPasswords = map[string]string{
		"alice": "правильный пароль",
		"bob":   "другой пароль",
	}
	testStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
)

// clock — время, которое двигает тест: иначе срок токена не проверить.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func newClock() *clock { return &clock{at: testStart} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// fixture — сервер с двумя людьми и одной кампанией. Токены идут по порядку,
// чтобы случаи API могли сверить ответ целиком.
func fixture(t *testing.T) (http.Handler, *Memory, *clock) {
	t.Helper()
	mem := NewMemory()
	mem.AddAccount("alice", testPasswords["alice"],
		Campaign{ID: "c1", Name: "Тени над Валдхеймом", Role: RoleOwner})
	mem.AddAccount("bob", testPasswords["bob"],
		Campaign{ID: "c1", Name: "Тени над Валдхеймом", Role: RolePlayer})

	clk := newClock()
	var mu sync.Mutex
	n := 0
	handler, err := New(Config{
		Directory: mem,
		Sessions:  mem,
		Now:       clk.Now,
		Token: func() string {
			mu.Lock()
			defer mu.Unlock()
			n++
			return fmt.Sprintf("token-%d", n)
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return handler, mem, clk
}

type caseRequest struct {
	Method  string          `json:"method"`
	Path    string          `json:"path"`
	Auth    string          `json:"auth,omitempty"`
	Token   string          `json:"token,omitempty"`
	Body    json.RawMessage `json:"body,omitempty"`
	BodyRaw string          `json:"body_raw,omitempty"`
}

type caseExpect struct {
	Status int             `json:"status"`
	Error  string          `json:"error,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// TestAPICases гоняет случаи из testdata/cases: папка — один запрос и один
// ожидаемый ответ. Те же файлы может взять чужой клиент.
func TestAPICases(t *testing.T) {
	entries, err := os.ReadDir("testdata/cases")
	if err != nil {
		t.Fatalf("нет случаев API: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("нет случаев API")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t.Run(entry.Name(), func(t *testing.T) {
			req := readCase[caseRequest](t, entry.Name(), "request.json")
			want := readCase[caseExpect](t, entry.Name(), "expect.json")

			handler, _, _ := fixture(t)
			token := req.Token
			if req.Auth != "" {
				token = login(t, handler, req.Auth)
			}

			body := req.Body
			if req.BodyRaw != "" {
				body = []byte(req.BodyRaw)
			}
			httpReq := httptest.NewRequest(req.Method, req.Path, bytes.NewReader(body))
			if token != "" {
				httpReq.Header.Set("Authorization", "Bearer "+token)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httpReq)

			if rec.Code != want.Status {
				t.Fatalf("код %d, ожидали %d; тело: %s", rec.Code, want.Status, rec.Body)
			}
			switch {
			case want.Error != "":
				var got struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("ответ не JSON: %v; тело: %s", err, rec.Body)
				}
				if got.Error.Code != want.Error {
					t.Fatalf("код ошибки %q, ожидали %q", got.Error.Code, want.Error)
				}
			case want.Body != nil:
				assertJSON(t, rec.Body.Bytes(), want.Body)
			default:
				if len(bytes.TrimSpace(rec.Body.Bytes())) != 0 {
					t.Fatalf("ожидали пустое тело, получили %s", rec.Body)
				}
			}
		})
	}
}

func readCase[T any](t *testing.T, name, file string) T {
	t.Helper()
	path := filepath.Join("testdata", "cases", name, file)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return value
}

func login(t *testing.T, handler http.Handler, name string) string {
	t.Helper()
	password, ok := testPasswords[name]
	if !ok {
		t.Fatalf("в фикстуре нет пароля для %q", name)
	}
	body, err := json.Marshal(map[string]string{"login": name, "password": password})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/sessions", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("вход %s: код %d, тело %s", name, rec.Code, rec.Body)
	}
	var got struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("вход %s: %v", name, err)
	}
	return got.Token
}

func assertJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("ответ не JSON: %v; тело: %s", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("ожидание не JSON: %v; файл: %s", err, want)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("ответ не совпал: получили %s, ожидали %s", got, want)
	}
}
