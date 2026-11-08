package api

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Memory — аккаунты и сессии в памяти. Это не боевое хранилище, а опора для
// случаев API и тестов; база появится отдельным пакетом и за тем же интерфейсом.
type Memory struct {
	mu       sync.RWMutex
	nextID   int
	accounts map[string]*memAccount
	byID     map[string]*memAccount
	tokens   map[string]memSession
}

type memAccount struct {
	account   Account
	password  string
	campaigns []Campaign
}

type memSession struct {
	accountID string
	expires   time.Time
}

// NewMemory — пустое хранилище.
func NewMemory() *Memory {
	return &Memory{
		accounts: map[string]*memAccount{},
		byID:     map[string]*memAccount{},
		tokens:   map[string]memSession{},
	}
}

// AddAccount заводит человека и возвращает его запись. Идентификаторы идут
// по порядку, чтобы случаи API могли сравнить ответ целиком.
func (m *Memory) AddAccount(login, password string, campaigns ...Campaign) Account {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	account := Account{ID: fmt.Sprintf("a%d", m.nextID), Login: login}
	entry := &memAccount{account: account, password: password, campaigns: campaigns}
	m.accounts[login] = entry
	m.byID[account.ID] = entry
	return account
}

func (m *Memory) Authenticate(login, password string) (Account, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.accounts[login]
	if !ok || entry.password != password {
		return Account{}, ErrBadCredentials
	}
	return entry.account, nil
}

func (m *Memory) ByID(id string) (Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.byID[id]
	if !ok {
		return Account{}, false
	}
	return entry.account, true
}

func (m *Memory) Campaigns(accountID string) []Campaign {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.byID[accountID]
	if !ok {
		return nil
	}
	out := make([]Campaign, len(entry.campaigns))
	copy(out, entry.campaigns)
	return out
}

func (m *Memory) Create(token, accountID string, expires time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tokens[token]; ok {
		return errors.New("такой токен уже есть")
	}
	m.tokens[token] = memSession{accountID: accountID, expires: expires}
	return nil
}

func (m *Memory) Lookup(token string) (string, time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	session, ok := m.tokens[token]
	if !ok {
		return "", time.Time{}, false
	}
	return session.accountID, session.expires, true
}

func (m *Memory) Delete(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tokens, token)
}
