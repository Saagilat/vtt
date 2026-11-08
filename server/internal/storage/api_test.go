package storage

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Saagilat/vtt/server/internal/api"
)

// TestAPIOverStorage — сквозная проверка: вход через HTTP, /v1/me и кампания из базы.
// Здесь сходятся обе половины: API не знает про SQLite, база — про HTTP.
func TestAPIOverStorage(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := db.CreateCampaign("Тени над Валдхеймом")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddMember(alice.ID, campaign, api.RoleOwner); err != nil {
		t.Fatal(err)
	}

	handler, err := api.New(api.Config{Directory: db, Sessions: db})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	body, err := json.Marshal(map[string]string{"login": "alice", "password": "пароль"})
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	handler.ServeHTTP(login, httptest.NewRequest("POST", "/v1/sessions", bytes.NewReader(body)))
	if login.Code != http.StatusOK {
		t.Fatalf("вход: код %d, тело %s", login.Code, login.Body)
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatalf("вход: %v", err)
	}
	if session.Token == "" {
		t.Fatal("вход без токена")
	}

	me := httptest.NewRequest("GET", "/v1/me", nil)
	me.Header.Set("Authorization", "Bearer "+session.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, me)
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/me: код %d, тело %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Тени над Валдхеймом") ||
		!strings.Contains(rec.Body.String(), `"owner"`) {
		t.Fatalf("/v1/me: %s", rec.Body)
	}
}
