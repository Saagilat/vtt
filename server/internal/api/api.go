// Package api — HTTP-интерфейс игры по server/API.md.
//
// Первый срез: вход, "мои кампании" и выход. Поведение зафиксировано случаями
// в testdata/cases: те же файлы сможет прогнать чужой клиент, не читая этот код.
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// SessionTTL — срок токена: 30 суток (server/API.md, "Вход и аккаунт").
	SessionTTL = 30 * 24 * time.Hour
	// maxBody — предел тела JSON-запроса. Импорт карты пойдёт своим маршрутом
	// и со своим пределом.
	maxBody = 1 << 20
)

// Роли в кампании — закрытый список из server/API.md.
const (
	RoleOwner    = "owner"
	RoleMaster   = "master"
	RolePlayer   = "player"
	RoleObserver = "observer"
)

// Account — вошедший человек.
type Account struct {
	ID    string `json:"id"`
	Login string `json:"login"`
}

// Campaign — кампания глазами участника.
type Campaign struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// Directory — аккаунты и их членство. Реализация с базой появится позже;
// пока интерфейс отделяет HTTP от хранения.
type Directory interface {
	// Authenticate проверяет пару и возвращает аккаунт. Неверный логин и неверный
	// пароль неразличимы: одна и та же ошибка ErrBadCredentials.
	Authenticate(login, password string) (Account, error)
	// ByID нужен, чтобы по токену получить аккаунт.
	ByID(id string) (Account, bool)
	// Campaigns — кампании, где человек участник, с его ролью в каждой.
	Campaigns(accountID string) []Campaign
}

// ErrBadCredentials — один ответ на неверный логин и неверный пароль.
var ErrBadCredentials = errors.New("неверный логин или пароль")

// Sessions — хранилище токенов.
type Sessions interface {
	Create(token, accountID string, expires time.Time) error
	Lookup(token string) (accountID string, expires time.Time, ok bool)
	Delete(token string)
}

// Config — что нужно серверу, чтобы отвечать.
type Config struct {
	Directory Directory
	Sessions  Sessions
	// Now и Token подменяются в тестах: иначе случаи не смогут сравнить ответ
	// целиком. В бою — время и crypto/rand.
	Now   func() time.Time
	Token func() string
}

type server struct {
	dir      Directory
	sessions Sessions
	now      func() time.Time
	token    func() string
	// auth — одна проверка пароля одновременно (server/API.md, "busy").
	auth    chan struct{}
	handler http.Handler
}

// New собирает обработчик. Ошибка — только если не передали зависимости.
func New(cfg Config) (http.Handler, error) {
	if cfg.Directory == nil {
		return nil, errors.New("api: нужен Directory")
	}
	if cfg.Sessions == nil {
		return nil, errors.New("api: нужны Sessions")
	}
	s := &server{
		dir:      cfg.Directory,
		sessions: cfg.Sessions,
		now:      cfg.Now,
		token:    cfg.Token,
		auth:     make(chan struct{}, 1),
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.token == nil {
		s.token = randomToken
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sessions", s.login)
	mux.HandleFunc("DELETE /v1/sessions/current", s.logout)
	mux.HandleFunc("GET /v1/me", s.me)
	mux.HandleFunc("/", s.notFound)
	s.handler = mux
	return s, nil
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

// login — единственный вход в игру: логин и пароль, ответ — bearer-токен.
func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Login == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "нужны login и password")
		return
	}
	select {
	case s.auth <- struct{}{}:
		defer func() { <-s.auth }()
	default:
		writeError(w, http.StatusServiceUnavailable, "busy", "проверка пароля занята, повторите")
		return
	}
	account, err := s.dir.Authenticate(req.Login, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "неверный логин или пароль")
		return
	}
	expires := s.now().UTC().Add(SessionTTL)
	token := s.token()
	if err := s.sessions.Create(token, account.ID, expires); err != nil {
		writeError(w, http.StatusServiceUnavailable, "busy", "сессия не создалась, повторите")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"expires_at": expires.Format(time.RFC3339),
		"account":    account,
	})
}

// logout — отзыв текущего токена; тело не нужно, поэтому 204.
func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	_, token, ok := s.whoami(w, r)
	if !ok {
		return
	}
	s.sessions.Delete(token)
	w.WriteHeader(http.StatusNoContent)
}

// me — аккаунт и кампании с ролью в каждой.
func (s *server) me(w http.ResponseWriter, r *http.Request) {
	account, _, ok := s.whoami(w, r)
	if !ok {
		return
	}
	campaigns := s.dir.Campaigns(account.ID)
	if campaigns == nil {
		campaigns = []Campaign{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account":   account,
		"campaigns": campaigns,
	})
}

func (s *server) notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", "такого пути нет")
}

// whoami — аккаунт по bearer-токену. Пишет отказ и возвращает false, если нельзя.
func (s *server) whoami(w http.ResponseWriter, r *http.Request) (Account, string, bool) {
	token := bearer(r)
	if token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "нужен токен")
		return Account{}, "", false
	}
	accountID, expires, ok := s.sessions.Lookup(token)
	if !ok || !s.now().Before(expires) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "токен истёк или неизвестен")
		return Account{}, "", false
	}
	account, ok := s.dir.ByID(accountID)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "аккаунта больше нет")
		return Account{}, "", false
	}
	return account, token, true
}

func bearer(r *http.Request) string {
	const prefix = "Bearer "
	head := r.Header.Get("Authorization")
	if !strings.HasPrefix(head, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(head, prefix))
}

// decode читает ровно один объект: лишние поля и лишние данные — ошибка,
// как и в контракте разметки.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "тело не разбирается: "+err.Error())
		return false
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "bad_request", "после объекта есть лишние данные")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand падать не должен; если падает — это не повод пускать без токена.
		panic(fmt.Sprintf("api: нет случайных чисел: %v", err))
	}
	return hex.EncodeToString(b[:])
}
