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
func (d *DB) campaigns(accountID string, archived bool) []api.Campaign {
	condition := "c.archived_at IS NULL"
	if archived {
		condition = "c.archived_at IS NOT NULL"
	}
	rows, err := d.db.Query(`SELECT c.id, c.name, m.role
		FROM memberships m JOIN campaigns c ON c.id = m.campaign_id
		WHERE m.account_id = ? AND `+condition+` ORDER BY c.name, c.id`, accountID)
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
		campaign.Archived = archived
		out = append(out, campaign)
	}
	return out
}

// Campaigns — активные кампании участника.
func (d *DB) Campaigns(accountID string) []api.Campaign {
	return d.campaigns(accountID, false)
}

// ArchivedCampaigns — кампании участника, убранные в архив.
func (d *DB) ArchivedCampaigns(accountID string) []api.Campaign {
	return d.campaigns(accountID, true)
}

// SetCampaignArchived убирает кампанию в архив или возвращает обратно.
func (d *DB) SetCampaignArchived(campaignID string, archived bool) bool {
	var value any
	if archived {
		value = time.Now().UTC().Format(time.RFC3339)
	}
	result, err := d.db.Exec(`UPDATE campaigns SET archived_at = ? WHERE id = ?`,
		value, campaignID)
	if err != nil {
		return false
	}
	affected, err := result.RowsAffected()
	return err == nil && affected > 0
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

// SetMemberRole меняет роль участника. Владельца не переназначаем.
func (d *DB) SetMemberRole(campaignID, accountID, role string) bool {
	if !validRole(role) || role == api.RoleOwner {
		return false
	}
	result, err := d.db.Exec(`UPDATE memberships SET role = ?
		WHERE campaign_id = ? AND account_id = ? AND role != ?`,
		role, campaignID, accountID, api.RoleOwner)
	if err != nil {
		return false
	}
	affected, err := result.RowsAffected()
	return err == nil && affected > 0
}

// RemoveMember убирает участника; владельца кампании удалить нельзя.
func (d *DB) RemoveMember(campaignID, accountID string) bool {
	result, err := d.db.Exec(`DELETE FROM memberships
		WHERE campaign_id = ? AND account_id = ? AND role != ?`,
		campaignID, accountID, api.RoleOwner)
	if err != nil {
		return false
	}
	affected, err := result.RowsAffected()
	return err == nil && affected > 0
}

// TransferOwnership передаёт кампанию другому участнику; прежний владелец
// становится мастером. Обе записи меняются одной транзакцией.
func (d *DB) TransferOwnership(campaignID, ownerID, targetID string) bool {
	if ownerID == targetID {
		return false
	}
	tx, err := d.db.Begin()
	if err != nil {
		return false
	}
	defer tx.Rollback()
	var role string
	if err := tx.QueryRow(`SELECT role FROM memberships
		WHERE campaign_id = ? AND account_id = ?`, campaignID, ownerID).Scan(&role); err != nil ||
		role != api.RoleOwner {
		return false
	}
	if err := tx.QueryRow(`SELECT role FROM memberships
		WHERE campaign_id = ? AND account_id = ?`, campaignID, targetID).Scan(&role); err != nil {
		return false
	}
	if _, err := tx.Exec(`UPDATE memberships SET role = ?
		WHERE campaign_id = ? AND account_id = ?`, api.RoleOwner, campaignID, targetID); err != nil {
		return false
	}
	if _, err := tx.Exec(`UPDATE memberships SET role = ?
		WHERE campaign_id = ? AND account_id = ?`, api.RoleMaster, campaignID, ownerID); err != nil {
		return false
	}
	return tx.Commit() == nil
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

// CreateInvite заводит одноразовый код входа в кампанию.
func (d *DB) CreateInvite(campaignID, createdBy string, expires time.Time) (api.Invite, error) {
	for attempt := 0; attempt < 5; attempt++ {
		code, err := newInviteCode()
		if err != nil {
			return api.Invite{}, err
		}
		_, err = d.db.Exec(`INSERT INTO invites
			(code, campaign_id, created_by, created_at, expires_at)
			VALUES (?, ?, ?, ?, ?)`,
			code, campaignID, createdBy,
			time.Now().UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339))
		if err == nil {
			return api.Invite{
				Code:       code,
				CampaignID: campaignID,
				ExpiresAt:  expires.UTC().Format(time.RFC3339),
			}, nil
		}
	}
	return api.Invite{}, errors.New("storage: код приглашения не создался")
}

func newInviteCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("storage: код приглашения: %w", err)
	}
	code := make([]byte, len(raw))
	for i, value := range raw {
		code[i] = alphabet[int(value)%len(alphabet)]
	}
	return string(code), nil
}

// AcceptInvite вступает по коду. Повторное принятие тем же человеком — успех,
// чужое повторное использование — api.ErrInviteUsed.
func (d *DB) AcceptInvite(code, accountID string, now time.Time) (api.Campaign, error) {
	tx, err := d.db.Begin()
	if err != nil {
		return api.Campaign{}, err
	}
	defer tx.Rollback()

	var campaignID, expiresAt string
	var acceptedBy sql.NullString
	err = tx.QueryRow(`SELECT campaign_id, expires_at, accepted_by
		FROM invites WHERE code = ?`, code).
		Scan(&campaignID, &expiresAt, &acceptedBy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return api.Campaign{}, api.ErrInviteNotFound
		}
		return api.Campaign{}, err
	}
	expires, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return api.Campaign{}, err
	}
	if now.After(expires) {
		return api.Campaign{}, api.ErrInviteExpired
	}
	if acceptedBy.Valid && acceptedBy.String != accountID {
		return api.Campaign{}, api.ErrInviteUsed
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO memberships
		(account_id, campaign_id, role) VALUES (?, ?, ?)`,
		accountID, campaignID, api.RolePlayer); err != nil {
		return api.Campaign{}, err
	}
	if _, err := tx.Exec(`UPDATE invites SET accepted_by = ?, accepted_at = ?
		WHERE code = ?`, accountID, now.UTC().Format(time.RFC3339), code); err != nil {
		return api.Campaign{}, err
	}
	var campaign api.Campaign
	if err := tx.QueryRow(`SELECT c.id, c.name, m.role
		FROM campaigns c JOIN memberships m ON m.campaign_id = c.id
		WHERE c.id = ? AND m.account_id = ?`, campaignID, accountID).
		Scan(&campaign.ID, &campaign.Name, &campaign.Role); err != nil {
		return api.Campaign{}, err
	}
	if err := tx.Commit(); err != nil {
		return api.Campaign{}, err
	}
	return campaign, nil
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

// UpdateSceneAnnotation заменяет разметку сцены; проверку контракта делает API.
// expectedVersion > 0 включает проверку: устаревшая правка не применяется.
func (d *DB) UpdateSceneAnnotation(campaignID, sceneID string, annotation []byte, expectedVersion int) (api.Scene, bool) {
	query := `UPDATE scenes SET annotation = ?, version = version + 1
		WHERE campaign_id = ? AND id = ?`
	args := []any{string(annotation), campaignID, sceneID}
	if expectedVersion > 0 {
		query += " AND version = ?"
		args = append(args, expectedVersion)
	}
	result, err := d.db.Exec(query, args...)
	if err != nil {
		return api.Scene{}, false
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return api.Scene{}, false
	}
	return d.Scene(campaignID, sceneID)
}

func (d *DB) sceneRow(query string, args ...any) (api.Scene, bool) {
	var (
		scene      api.Scene
		annotation string
	)
	err := d.db.QueryRow(query, args...).Scan(&scene.ID, &scene.Name, &annotation,
		&scene.Light, &scene.Active, &scene.Version, &scene.Image.ID, &scene.Image.SHA256,
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
	return d.sceneRow(`SELECT s.id, s.name, s.annotation, s.light, s.active, s.version,
		i.id, i.sha256, i.width, i.height, i.mime
		FROM scenes s JOIN images i ON i.id = s.image_id
		WHERE s.campaign_id = ? AND s.id = ?`, campaignID, sceneID)
}

// Doors — состояние дверей сцены: true — открыта.
func (d *DB) Doors(campaignID, sceneID string) map[string]bool {
	rows, err := d.db.Query(`SELECT ds.door_id, ds.open
		FROM door_states ds JOIN scenes s ON s.id = ds.scene_id
		WHERE s.campaign_id = ? AND ds.scene_id = ?`, campaignID, sceneID)
	if err != nil {
		return map[string]bool{}
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var open int
		if err := rows.Scan(&id, &open); err != nil {
			return map[string]bool{}
		}
		out[id] = open != 0
	}
	return out
}

// SetDoor сохраняет состояние двери.
func (d *DB) SetDoor(campaignID, sceneID, doorID string, open bool) bool {
	var exists int
	if err := d.db.QueryRow("SELECT 1 FROM scenes WHERE campaign_id = ? AND id = ?",
		campaignID, sceneID).Scan(&exists); err != nil {
		return false
	}
	value := 0
	if open {
		value = 1
	}
	_, err := d.db.Exec(`INSERT INTO door_states (scene_id, door_id, open) VALUES (?, ?, ?)
		ON CONFLICT(scene_id, door_id) DO UPDATE SET open = excluded.open`,
		sceneID, doorID, value)
	return err == nil
}

// ExploredMask — накопленная видимость игрока на сцене.
func (d *DB) ExploredMask(campaignID, sceneID, accountID string) []byte {
	var data []byte
	err := d.db.QueryRow(`SELECT data FROM explored_masks
		WHERE campaign_id = ? AND scene_id = ? AND account_id = ?`,
		campaignID, sceneID, accountID).Scan(&data)
	if err != nil {
		return nil
	}
	return data
}

// SaveExploredMask сохраняет накопленную маску; повторная запись заменяет её.
func (d *DB) SaveExploredMask(campaignID, sceneID, accountID string, data []byte) bool {
	_, err := d.db.Exec(`INSERT INTO explored_masks
		(campaign_id, scene_id, account_id, data, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(campaign_id, scene_id, account_id)
		DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		campaignID, sceneID, accountID, data, time.Now().UTC().Format(time.RFC3339))
	return err == nil
}

func (d *DB) characterRow(query string, args ...any) (api.Character, bool) {
	var character api.Character
	var data string
	err := d.db.QueryRow(query, args...).Scan(&character.ID, &character.Name,
		&data, &character.Approved, &character.OwnerID, &character.OwnerLogin)
	if err != nil {
		return api.Character{}, false
	}
	character.Data = json.RawMessage(data)
	return character, true
}

// Characters — все листы кампании с логинами владельцев.
func (d *DB) Characters(campaignID string) []api.Character {
	rows, err := d.db.Query(`SELECT c.id, c.name, c.data, c.approved,
		COALESCE(c.owner_id, ''), COALESCE(a.login, '')
		FROM characters c LEFT JOIN accounts a ON a.id = c.owner_id
		WHERE c.campaign_id = ? ORDER BY c.name, c.id`, campaignID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]api.Character, 0, 8)
	for rows.Next() {
		var character api.Character
		var data string
		if err := rows.Scan(&character.ID, &character.Name, &data, &character.Approved,
			&character.OwnerID, &character.OwnerLogin); err != nil {
			return nil
		}
		character.Data = json.RawMessage(data)
		out = append(out, character)
	}
	return out
}

// Character — один лист.
func (d *DB) Character(campaignID, characterID string) (api.Character, bool) {
	return d.characterRow(`SELECT c.id, c.name, c.data, c.approved,
		COALESCE(c.owner_id, ''), COALESCE(a.login, '')
		FROM characters c LEFT JOIN accounts a ON a.id = c.owner_id
		WHERE c.campaign_id = ? AND c.id = ?`, campaignID, characterID)
}

// CreateCharacter заводит лист. Права владельца проверяет API.
func (d *DB) CreateCharacter(campaignID, ownerID, name string, data []byte) (api.Character, bool) {
	if strings.TrimSpace(name) == "" {
		name = "Персонаж"
	}
	id, err := newID("h")
	if err != nil {
		return api.Character{}, false
	}
	var owner any
	if ownerID != "" {
		owner = ownerID
	}
	_, err = d.db.Exec(`INSERT INTO characters
		(id, campaign_id, owner_id, name, data, approved, created_at)
		VALUES (?, ?, ?, ?, ?, 0, ?)`,
		id, campaignID, owner, name, string(data), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return api.Character{}, false
	}
	return d.Character(campaignID, id)
}

// UpdateCharacter сохраняет лист; права проверяет API.
func (d *DB) UpdateCharacter(campaignID, characterID, name string, data []byte) (api.Character, bool) {
	if strings.TrimSpace(name) == "" {
		name = "Персонаж"
	}
	result, err := d.db.Exec(`UPDATE characters SET name = ?, data = ?
		WHERE campaign_id = ? AND id = ?`, name, string(data), campaignID, characterID)
	if err != nil {
		return api.Character{}, false
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return api.Character{}, false
	}
	return d.Character(campaignID, characterID)
}

func (d *DB) noteRow(query string, args ...any) (api.Note, bool) {
	var note api.Note
	err := d.db.QueryRow(query, args...).Scan(&note.ID, &note.Title, &note.Body,
		&note.AuthorID, &note.AuthorLogin, &note.Visibility, &note.UpdatedAt)
	if err != nil {
		return api.Note{}, false
	}
	return note, true
}

func noteVisibility(value string) string {
	if value == "private" {
		return "private"
	}
	return "shared"
}

// Notes — журнал кампании, видимый человеку: общие и его личные записи.
func (d *DB) Notes(campaignID, accountID string) []api.Note {
	rows, err := d.db.Query(`SELECT n.id, n.title, n.body,
		COALESCE(n.author_id, ''), COALESCE(a.login, ''), n.visibility, n.updated_at
		FROM notes n LEFT JOIN accounts a ON a.id = n.author_id
		WHERE n.campaign_id = ? AND (n.visibility = 'shared' OR n.author_id = ?)
		ORDER BY n.updated_at DESC, n.id DESC`, campaignID, accountID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]api.Note, 0, 8)
	for rows.Next() {
		var note api.Note
		if err := rows.Scan(&note.ID, &note.Title, &note.Body,
			&note.AuthorID, &note.AuthorLogin, &note.Visibility, &note.UpdatedAt); err != nil {
			return nil
		}
		out = append(out, note)
	}
	return out
}

// Note — одна запись журнала.
func (d *DB) Note(campaignID, noteID string) (api.Note, bool) {
	return d.noteRow(`SELECT n.id, n.title, n.body,
		COALESCE(n.author_id, ''), COALESCE(a.login, ''), n.visibility, n.updated_at
		FROM notes n LEFT JOIN accounts a ON a.id = n.author_id
		WHERE n.campaign_id = ? AND n.id = ?`, campaignID, noteID)
}

// CreateNote заводит запись журнала от имени автора.
func (d *DB) CreateNote(campaignID, authorID, title, body, visibility string) (api.Note, bool) {
	if strings.TrimSpace(title) == "" {
		title = "Заметка"
	}
	id, err := newID("n")
	if err != nil {
		return api.Note{}, false
	}
	var author any
	if authorID != "" {
		author = authorID
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := d.db.Exec(`INSERT INTO notes
		(id, campaign_id, author_id, title, body, visibility, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, campaignID, author, title, body, noteVisibility(visibility), now, now); err != nil {
		return api.Note{}, false
	}
	return d.Note(campaignID, id)
}

// UpdateNote сохраняет заголовок, текст и видимость; права проверяет API.
func (d *DB) UpdateNote(campaignID, noteID, title, body, visibility string) (api.Note, bool) {
	if strings.TrimSpace(title) == "" {
		title = "Заметка"
	}
	result, err := d.db.Exec(`UPDATE notes SET title = ?, body = ?, visibility = ?, updated_at = ?
		WHERE campaign_id = ? AND id = ?`,
		title, body, noteVisibility(visibility),
		time.Now().UTC().Format(time.RFC3339Nano), campaignID, noteID)
	if err != nil {
		return api.Note{}, false
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return api.Note{}, false
	}
	return d.Note(campaignID, noteID)
}

// DeleteNote убирает запись журнала.
func (d *DB) DeleteNote(campaignID, noteID string) bool {
	result, err := d.db.Exec(`DELETE FROM notes WHERE campaign_id = ? AND id = ?`,
		campaignID, noteID)
	if err != nil {
		return false
	}
	affected, err := result.RowsAffected()
	return err == nil && affected > 0
}

// AddChatMessage сохраняет сообщение и возвращает его с логинами автора и получателя.
func (d *DB) AddChatMessage(campaignID, accountID, toAccountID, text, channel string) (api.ChatMessage, bool) {
	id, err := newID("m")
	if err != nil {
		return api.ChatMessage{}, false
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	var to any
	if toAccountID != "" {
		to = toAccountID
	}
	if _, err := d.db.Exec(`INSERT INTO chat_messages
		(id, campaign_id, account_id, to_account_id, text, channel, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, campaignID, accountID, to, text, channel, created); err != nil {
		return api.ChatMessage{}, false
	}
	var message api.ChatMessage
	err = d.db.QueryRow(`SELECT m.id, m.text, m.channel,
		COALESCE(m.to_account_id, ''), COALESCE(t.login, ''), m.created_at, a.id, a.login
		FROM chat_messages m
		JOIN accounts a ON a.id = m.account_id
		LEFT JOIN accounts t ON t.id = m.to_account_id
		WHERE m.id = ?`, id).
		Scan(&message.ID, &message.Text, &message.Channel,
			&message.ToAccountID, &message.ToLogin, &message.CreatedAt,
			&message.Actor.ID, &message.Actor.Login)
	if err != nil {
		return api.ChatMessage{}, false
	}
	return message, true
}

// ChatMessages отдаёт последние сообщения в хронологическом порядке.
func (d *DB) ChatMessages(campaignID string, limit int) []api.ChatMessage {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.db.Query(`SELECT m.id, m.text, m.channel,
		COALESCE(m.to_account_id, ''), COALESCE(t.login, ''), m.created_at, a.id, a.login
		FROM chat_messages m
		JOIN accounts a ON a.id = m.account_id
		LEFT JOIN accounts t ON t.id = m.to_account_id
		WHERE m.campaign_id = ? ORDER BY m.created_at DESC, m.id DESC LIMIT ?`,
		campaignID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]api.ChatMessage, 0, limit)
	for rows.Next() {
		var message api.ChatMessage
		if err := rows.Scan(&message.ID, &message.Text, &message.Channel,
			&message.ToAccountID, &message.ToLogin, &message.CreatedAt,
			&message.Actor.ID, &message.Actor.Login); err != nil {
			return nil
		}
		out = append(out, message)
	}
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out
}

func (d *DB) SetSceneLight(campaignID, sceneID, light string) bool {
	if light != "bright" && light != "dark" {
		return false
	}
	result, err := d.db.Exec(`UPDATE scenes SET light = ?
		WHERE campaign_id = ? AND id = ?`, light, campaignID, sceneID)
	if err != nil {
		return false
	}
	affected, err := result.RowsAffected()
	return err == nil && affected > 0
}

func (d *DB) SetActiveScene(campaignID, sceneID string) bool {
	tx, err := d.db.Begin()
	if err != nil {
		return false
	}
	var exists int
	if err := tx.QueryRow("SELECT 1 FROM scenes WHERE campaign_id = ? AND id = ?",
		campaignID, sceneID).Scan(&exists); err != nil {
		tx.Rollback()
		return false
	}
	if _, err := tx.Exec("UPDATE scenes SET active = 0 WHERE campaign_id = ?", campaignID); err != nil {
		tx.Rollback()
		return false
	}
	if _, err := tx.Exec("UPDATE scenes SET active = 1 WHERE campaign_id = ? AND id = ?",
		campaignID, sceneID); err != nil {
		tx.Rollback()
		return false
	}
	return tx.Commit() == nil
}

// Turn — чей ход на конкретной сцене и какой круг. Пустая фишка значит «бой не начат».
func (d *DB) Turn(campaignID, sceneID string) (string, int) {
	var tokenID string
	var round int
	err := d.db.QueryRow(`SELECT token_id, round FROM scene_turns
		WHERE campaign_id = ? AND scene_id = ?`, campaignID, sceneID).
		Scan(&tokenID, &round)
	if err != nil {
		return "", 0
	}
	return tokenID, round
}

// SetTurn сохраняет текущий ход сцены. Пустая фишка и круг 0 — бой не начат.
func (d *DB) SetTurn(campaignID, sceneID, tokenID string, round int) bool {
	if round < 0 {
		round = 0
	}
	_, err := d.db.Exec(`INSERT INTO scene_turns (campaign_id, scene_id, token_id, round)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(scene_id) DO UPDATE SET token_id = excluded.token_id, round = excluded.round`,
		campaignID, sceneID, tokenID, round)
	return err == nil
}

func (d *DB) Tokens(campaignID, sceneID string) []api.Token {
	rows, err := d.db.Query(`SELECT t.id, t.scene_id, t.name, t.x, t.y, t.color,
		COALESCE(t.owner_id, ''), COALESCE(a.login, ''),
		COALESCE(t.character_id, ''), COALESCE(ch.name, ''),
		t.hp_current, t.hp_max, t.initiative, t.darkvision_ft
		FROM tokens t LEFT JOIN accounts a ON a.id = t.owner_id
		LEFT JOIN characters ch ON ch.id = t.character_id
		WHERE t.campaign_id = ? AND t.scene_id = ? ORDER BY t.created_at, t.id`,
		campaignID, sceneID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]api.Token, 0, 8)
	for rows.Next() {
		var token api.Token
		if err := rows.Scan(&token.ID, &token.SceneID, &token.Name,
			&token.X, &token.Y, &token.Color, &token.OwnerID, &token.OwnerLogin,
			&token.CharacterID, &token.CharacterName,
			&token.HP, &token.HPMax, &token.Initiative, &token.Darkvision); err != nil {
			return nil
		}
		out = append(out, token)
	}
	return out
}

// Token — одна фишка сцены.
func (d *DB) Token(campaignID, sceneID, tokenID string) (api.Token, bool) {
	var token api.Token
	err := d.db.QueryRow(`SELECT t.id, t.scene_id, t.name, t.x, t.y, t.color,
		COALESCE(t.owner_id, ''), COALESCE(a.login, ''),
		COALESCE(t.character_id, ''), COALESCE(ch.name, ''),
		t.hp_current, t.hp_max, t.initiative, t.darkvision_ft
		FROM tokens t LEFT JOIN accounts a ON a.id = t.owner_id
		LEFT JOIN characters ch ON ch.id = t.character_id
		WHERE t.campaign_id = ? AND t.scene_id = ? AND t.id = ?`,
		campaignID, sceneID, tokenID).
		Scan(&token.ID, &token.SceneID, &token.Name, &token.X, &token.Y,
			&token.Color, &token.OwnerID, &token.OwnerLogin,
			&token.CharacterID, &token.CharacterName,
			&token.HP, &token.HPMax, &token.Initiative, &token.Darkvision)
	if err != nil {
		return api.Token{}, false
	}
	return token, true
}

// CreateToken ставит фишку на сцену кампании.
func (d *DB) CreateToken(campaignID, sceneID, ownerID, name string, x, y float64) (api.Token, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Фишка"
	}
	var exists int
	if err := d.db.QueryRow("SELECT 1 FROM scenes WHERE campaign_id = ? AND id = ?",
		campaignID, sceneID).Scan(&exists); err != nil {
		return api.Token{}, false
	}
	id, err := newID("t")
	if err != nil {
		return api.Token{}, false
	}
	if _, err := d.db.Exec(`INSERT INTO tokens
		(id, campaign_id, scene_id, owner_id, name, x, y, color, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '#e8c547', ?)`,
		id, campaignID, sceneID, ownerID, name, x, y,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return api.Token{}, false
	}
	return d.Token(campaignID, sceneID, id)
}

// MoveToken двигает фишку; вызывающий уже проверил права.
func (d *DB) MoveToken(campaignID, sceneID, tokenID string, x, y float64) (api.Token, bool) {
	if _, err := d.db.Exec(`UPDATE tokens SET x = ?, y = ?
		WHERE campaign_id = ? AND scene_id = ? AND id = ?`,
		x, y, campaignID, sceneID, tokenID); err != nil {
		return api.Token{}, false
	}
	return d.Token(campaignID, sceneID, tokenID)
}

// SetTokenDarkvision сохраняет дальность тёмного зрения в футах.
func (d *DB) SetTokenDarkvision(campaignID, sceneID, tokenID string, darkvision int) (api.Token, bool) {
	if _, err := d.db.Exec(`UPDATE tokens SET darkvision_ft = ?
		WHERE campaign_id = ? AND scene_id = ? AND id = ?`,
		darkvision, campaignID, sceneID, tokenID); err != nil {
		return api.Token{}, false
	}
	return d.Token(campaignID, sceneID, tokenID)
}

func (d *DB) SetTokenStats(campaignID, sceneID, tokenID string, hp, hpMax, initiative int) (api.Token, bool) {
	if _, err := d.db.Exec(`UPDATE tokens SET hp_current = ?, hp_max = ?, initiative = ?
		WHERE campaign_id = ? AND scene_id = ? AND id = ?`,
		hp, hpMax, initiative, campaignID, sceneID, tokenID); err != nil {
		return api.Token{}, false
	}
	return d.Token(campaignID, sceneID, tokenID)
}

// SetTokenCharacter связывает фишку с листом; пустой characterID снимает связь.
func (d *DB) SetTokenCharacter(campaignID, sceneID, tokenID, characterID string) (api.Token, bool) {
	var character any
	if characterID != "" {
		character = characterID
	}
	if _, err := d.db.Exec(`UPDATE tokens SET character_id = ?
		WHERE campaign_id = ? AND scene_id = ? AND id = ?`,
		character, campaignID, sceneID, tokenID); err != nil {
		return api.Token{}, false
	}
	return d.Token(campaignID, sceneID, tokenID)
}

// SetCharacterHP меняет HP в данных листа, сохраняя остальные поля.
func (d *DB) SetCharacterHP(campaignID, characterID string, hp, hpMax int) (api.Character, bool) {
	character, ok := d.Character(campaignID, characterID)
	if !ok {
		return api.Character{}, false
	}
	sheet := map[string]any{}
	if err := json.Unmarshal(character.Data, &sheet); err != nil {
		sheet = map[string]any{}
	}
	sheet["hp"] = hp
	sheet["max_hp"] = hpMax
	data, err := json.Marshal(sheet)
	if err != nil {
		return api.Character{}, false
	}
	return d.UpdateCharacter(campaignID, characterID, character.Name, data)
}

// SetCharacterTokensHP переносит HP листа на все связанные фишки кампании.
func (d *DB) SetCharacterTokensHP(campaignID, characterID string, hp, hpMax int) bool {
	_, err := d.db.Exec(`UPDATE tokens SET hp_current = ?, hp_max = ?
		WHERE campaign_id = ? AND character_id = ?`,
		hp, hpMax, campaignID, characterID)
	return err == nil
}

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
