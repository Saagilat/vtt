// Package storage — база сервера: аккаунты, кампании, членство и сессии.
//
// Драйвер — modernc.org/sqlite: чистый Go, статический бинарник без gcc и без
// зависимости от glibc машины сборки. Сервер живёт на дешёвом VPS, а нагрузка
// здесь — несколько человек, поэтому скорость C-драйвера не нужна, а простота
// поставки нужна. Правило записано в server/README.md.
package storage

import (
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Saagilat/vtt/server/internal/api"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DB — база сервера. Реализует api.Directory и api.Sessions.
type DB struct {
	db *sql.DB

	dummyOnce sync.Once
	dummyHash string
}

// Open открывает базу, включает WAL и доводит схему до последней миграции.
func Open(path string) (*DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("storage: пустой путь к базе")
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: открыть %s: %w", path, err)
	}
	// Одна запись за раз: SQLite всё равно сериализует писателей, а на одном ядре
	// лишние соединения только добавляют поводов для busy.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

// Close закрывает базу.
func (d *DB) Close() error { return d.db.Close() }

// migrate применяет миграции по порядку имён, каждую в своей транзакции.
// Вперёд и только вперёд: назад — из бэкапа (server/README.md).
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("storage: версия схемы: %w", err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("storage: миграции: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for i, name := range names {
		number := i + 1
		if number <= version {
			continue
		}
		body, err := migrations.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("storage: миграция %s: %w", name, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("storage: миграция %s: %w", name, err)
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("storage: миграция %s: %w", name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", number)); err != nil {
			tx.Rollback()
			return fmt.Errorf("storage: миграция %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("storage: миграция %s: %w", name, err)
		}
	}
	return nil
}

// CreateAccount заводит человека. Пока это единственный способ: регистрацию
// и панель администратора сделаем отдельно.
func (d *DB) CreateAccount(login, password string) (api.Account, error) {
	login = strings.TrimSpace(login)
	if login == "" || password == "" {
		return api.Account{}, errors.New("storage: нужны логин и пароль")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return api.Account{}, fmt.Errorf("storage: пароль: %w", err)
	}
	id, err := newID("a")
	if err != nil {
		return api.Account{}, err
	}
	_, err = d.db.Exec("INSERT INTO accounts (id, login, password_hash) VALUES (?, ?, ?)", id, login, hash)
	if err != nil {
		return api.Account{}, fmt.Errorf("storage: аккаунт %q: %w", login, err)
	}
	return api.Account{ID: id, Login: login}, nil
}

// Authenticate проверяет пару. Неверный логин и неверный пароль неразличимы
// и по ответу, и по времени: для несуществующего логина проверка всё равно идёт.
func (d *DB) Authenticate(login, password string) (api.Account, error) {
	var account api.Account
	var hash string
	err := d.db.QueryRow("SELECT id, login, password_hash FROM accounts WHERE login = ?", login).
		Scan(&account.ID, &account.Login, &hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, _ = verifyPassword(d.dummy(), password)
		return api.Account{}, api.ErrBadCredentials
	case err != nil:
		return api.Account{}, err
	}
	ok, err := verifyPassword(hash, password)
	if err != nil {
		return api.Account{}, err
	}
	if !ok {
		return api.Account{}, api.ErrBadCredentials
	}
	return account, nil
}

// dummy — хэш-заглушка, чтобы вход с несуществующим логином стоил столько же
// времени, сколько с существующим.
func (d *DB) dummy() string {
	d.dummyOnce.Do(func() {
		hash, err := hashPassword("неверный пароль")
		if err == nil {
			d.dummyHash = hash
		}
	})
	return d.dummyHash
}

// ByID возвращает аккаунт по идентификатору.
func (d *DB) ByID(id string) (api.Account, bool) {
	var account api.Account
	err := d.db.QueryRow("SELECT id, login FROM accounts WHERE id = ?", id).
		Scan(&account.ID, &account.Login)
	if err != nil {
		return api.Account{}, false
	}
	return account, true
}

// Campaigns — кампании, где человек участник, с его ролью в каждой.
func (d *DB) Campaigns(accountID string) []api.Campaign {
	rows, err := d.db.Query(`SELECT c.id, c.name, m.role
		FROM memberships m JOIN campaigns c ON c.id = m.campaign_id
		WHERE m.account_id = ? ORDER BY c.name, c.id`, accountID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]api.Campaign, 0, 4)
	for rows.Next() {
		var campaign api.Campaign
		if err := rows.Scan(&campaign.ID, &campaign.Name, &campaign.Role); err != nil {
			return nil
		}
		out = append(out, campaign)
	}
	return out
}

// CreateCampaign заводит кампанию; участников добавляет AddMember.
func (d *DB) CreateCampaign(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("storage: пустое имя кампании")
	}
	id, err := newID("c")
	if err != nil {
		return "", err
	}
	if _, err := d.db.Exec("INSERT INTO campaigns (id, name) VALUES (?, ?)", id, name); err != nil {
		return "", fmt.Errorf("storage: кампания %q: %w", name, err)
	}
	return id, nil
}

// AddMember добавляет человека в кампанию с ролью из закрытого списка.
func (d *DB) AddMember(accountID, campaignID, role string) error {
	if !validRole(role) {
		return fmt.Errorf("storage: незнакомая роль %q", role)
	}
	_, err := d.db.Exec("INSERT INTO memberships (account_id, campaign_id, role) VALUES (?, ?, ?)",
		accountID, campaignID, role)
	if err != nil {
		return fmt.Errorf("storage: участник: %w", err)
	}
	return nil
}

func validRole(role string) bool {
	switch role {
	case api.RoleOwner, api.RoleMaster, api.RolePlayer, api.RoleObserver:
		return true
	default:
		return false
	}
}

// Create — положить токен в базу (api.Sessions).
func (d *DB) Create(token, accountID string, expires time.Time) error {
	_, err := d.db.Exec("INSERT INTO sessions (token, account_id, expires_at) VALUES (?, ?, ?)",
		token, accountID, expires.UTC().Format(time.RFC3339))
	return err
}

// Lookup — найти токен (api.Sessions).
func (d *DB) Lookup(token string) (string, time.Time, bool) {
	var accountID, expires string
	err := d.db.QueryRow("SELECT account_id, expires_at FROM sessions WHERE token = ?", token).
		Scan(&accountID, &expires)
	if err != nil {
		return "", time.Time{}, false
	}
	at, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return "", time.Time{}, false
	}
	return accountID, at, true
}

// Delete — убрать токен (api.Sessions).
func (d *DB) Delete(token string) {
	_, _ = d.db.Exec("DELETE FROM sessions WHERE token = ?", token)
}

// ImageBytes отдаёт байты картинки кампании.
func (d *DB) ImageBytes(campaignID, imageID string) ([]byte, string, bool) {
	var data []byte
	var mime string
	err := d.db.QueryRow(`SELECT data, mime FROM images WHERE campaign_id = ? AND id = ?`,
		campaignID, imageID).Scan(&data, &mime)
	if err != nil {
		return nil, "", false
	}
	return data, mime, true
}

func (d *DB) CreateCampaignFor(ownerID, name string) (api.Campaign, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return api.Campaign{}, errors.New("storage: пустое имя кампании")
	}
	id, err := newID("c")
	if err != nil {
		return api.Campaign{}, err
	}
	tx, err := d.db.Begin()
	if err != nil {
		return api.Campaign{}, err
	}
	if _, err := tx.Exec("INSERT INTO campaigns (id, name) VALUES (?, ?)", id, name); err != nil {
		tx.Rollback()
		return api.Campaign{}, fmt.Errorf("storage: кампания %q: %w", name, err)
	}
	if _, err := tx.Exec("INSERT INTO memberships (account_id, campaign_id, role) VALUES (?, ?, ?)",
		ownerID, id, api.RoleOwner); err != nil {
		tx.Rollback()
		return api.Campaign{}, fmt.Errorf("storage: владелец кампании: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return api.Campaign{}, err
	}
	return api.Campaign{ID: id, Name: name, Role: api.RoleOwner}, nil
}

// Campaign — кампания с составом, если человек в ней состоит.
func (d *DB) Campaign(accountID, campaignID string) (api.CampaignDetail, bool) {
	var detail api.CampaignDetail
	err := d.db.QueryRow(`SELECT c.id, c.name, m.role
		FROM campaigns c JOIN memberships m ON m.campaign_id = c.id
		WHERE c.id = ? AND m.account_id = ?`, campaignID, accountID).
		Scan(&detail.ID, &detail.Name, &detail.Role)
	if err != nil {
		return api.CampaignDetail{}, false
	}
	rows, err := d.db.Query(`SELECT a.id, a.login, m.role
		FROM memberships m JOIN accounts a ON a.id = m.account_id
		WHERE m.campaign_id = ? ORDER BY a.login`, campaignID)
	if err != nil {
		return api.CampaignDetail{}, false
	}
	defer rows.Close()
	for rows.Next() {
		var member api.Member
		if err := rows.Scan(&member.ID, &member.Login, &member.Role); err != nil {
			return api.CampaignDetail{}, false
		}
		detail.Members = append(detail.Members, member)
	}
	return detail, true
}

// AddImage кладёт картинку в библиотеку кампании; повтор по sha256 не плодит копию.
func (d *DB) AddImage(campaignID string, data []byte, mime string, width, height int, sha string) (api.Image, error) {
	id, err := newID("i")
	if err != nil {
		return api.Image{}, err
	}
	if _, err := d.db.Exec(`INSERT OR IGNORE INTO images
		(id, campaign_id, sha256, width, height, mime, data) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, campaignID, sha, width, height, mime, data); err != nil {
		return api.Image{}, fmt.Errorf("storage: картинка: %w", err)
	}
	var image api.Image
	err = d.db.QueryRow(`SELECT id, sha256, width, height, mime FROM images
		WHERE campaign_id = ? AND sha256 = ?`, campaignID, sha).
		Scan(&image.ID, &image.SHA256, &image.Width, &image.Height, &image.Mime)
	if err != nil {
		return api.Image{}, err
	}
	return image, nil
}

// CreateScene сохраняет сцену вместе с разметкой.
func (d *DB) CreateScene(campaignID, imageID, name string, annotation []byte, active bool) (api.Scene, error) {
	id, err := newID("s")
	if err != nil {
		return api.Scene{}, err
	}
	activeValue := 0
	if active {
		activeValue = 1
	}
	if _, err := d.db.Exec(`INSERT INTO scenes
		(id, campaign_id, image_id, name, annotation, light, active, created_at)
		VALUES (?, ?, ?, ?, ?, 'bright', ?, ?)`,
		id, campaignID, imageID, name, string(annotation), activeValue,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return api.Scene{}, fmt.Errorf("storage: сцена: %w", err)
	}
	scene, ok := d.Scene(campaignID, id)
	if !ok {
		return api.Scene{}, errors.New("storage: сцена не читается после записи")
	}
	return scene, nil
}

func (d *DB) sceneRow(query string, args ...any) (api.Scene, bool) {
	var (
		scene      api.Scene
		annotation string
	)
	err := d.db.QueryRow(query, args...).Scan(&scene.ID, &scene.Name, &annotation,
		&scene.Light, &scene.Active, &scene.Image.ID, &scene.Image.SHA256,
		&scene.Image.Width, &scene.Image.Height, &scene.Image.Mime)
	if err != nil {
		return api.Scene{}, false
	}
	var parts map[string]json.RawMessage
	if err := json.Unmarshal([]byte(annotation), &parts); err != nil {
		return api.Scene{}, false
	}
	scene.Grid = parts["grid"]
	scene.Walls = parts["walls"]
	scene.Doors = parts["doors"]
	scene.Areas = parts["areas"]
	return scene, true
}

// Scene — сцена кампании вместе с картинкой и геометрией.
func (d *DB) Scene(campaignID, sceneID string) (api.Scene, bool) {
	return d.sceneRow(`SELECT s.id, s.name, s.annotation, s.light, s.active,
		i.id, i.sha256, i.width, i.height, i.mime
		FROM scenes s JOIN images i ON i.id = s.image_id
		WHERE s.campaign_id = ? AND s.id = ?`, campaignID, sceneID)
}

// Scenes — список сцен кампании без геометрии.
func (d *DB) Scenes(campaignID string) []api.SceneSummary {
	rows, err := d.db.Query(`SELECT s.id, s.name, s.active,
		i.id, i.sha256, i.width, i.height, i.mime
		FROM scenes s JOIN images i ON i.id = s.image_id
		WHERE s.campaign_id = ? ORDER BY s.created_at, s.id`, campaignID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]api.SceneSummary, 0, 4)
	for rows.Next() {
		var item api.SceneSummary
		if err := rows.Scan(&item.ID, &item.Name, &item.Active,
			&item.Image.ID, &item.Image.SHA256, &item.Image.Width,
			&item.Image.Height, &item.Image.Mime); err != nil {
			return nil
		}
		out = append(out, item)
	}
	return out
}

func newID(prefix string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("storage: идентификатор: %w", err)
	}
	return prefix + hex.EncodeToString(b[:]), nil
}
