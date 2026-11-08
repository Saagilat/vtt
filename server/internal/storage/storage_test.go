package storage

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Saagilat/vtt/server/internal/api"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "vtt.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestAccountLifecycle(t *testing.T) {
	db := openTemp(t)
	created, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if created.ID == "" || created.Login != "alice" {
		t.Fatalf("аккаунт: %+v", created)
	}

	got, err := db.Authenticate("alice", "пароль")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if got != created {
		t.Fatalf("вошёл не тот: %+v, ждали %+v", got, created)
	}

	if _, err := db.Authenticate("alice", "не тот"); !errors.Is(err, api.ErrBadCredentials) {
		t.Fatalf("неверный пароль: %v", err)
	}
	if _, err := db.Authenticate("nobody", "пароль"); !errors.Is(err, api.ErrBadCredentials) {
		t.Fatalf("неизвестный логин: %v", err)
	}
	if byID, ok := db.ByID(created.ID); !ok || byID != created {
		t.Fatalf("ByID: %+v, %v", byID, ok)
	}
}

func TestDuplicateLoginRejected(t *testing.T) {
	db := openTemp(t)
	if _, err := db.CreateAccount("alice", "пароль"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateAccount("alice", "другой"); err == nil {
		t.Fatal("второй аккаунт с тем же логином заведён")
	}
}

func TestCampaignsOfAccount(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.CreateAccount("bob", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	first, err := db.CreateCampaign("Алая тропа")
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CreateCampaign("Тени над Валдхеймом")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddMember(alice.ID, first, api.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMember(alice.ID, second, api.RolePlayer); err != nil {
		t.Fatal(err)
	}
	if err := db.AddMember(bob.ID, first, api.RoleMaster); err != nil {
		t.Fatal(err)
	}

	got := db.Campaigns(alice.ID)
	want := []api.Campaign{
		{ID: first, Name: "Алая тропа", Role: api.RoleOwner},
		{ID: second, Name: "Тени над Валдхеймом", Role: api.RolePlayer},
	}
	if len(got) != len(want) {
		t.Fatalf("кампании: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("кампания %d: %+v, ждали %+v", i, got[i], want[i])
		}
	}
	if role := db.Campaigns(bob.ID); len(role) != 1 || role[0].Role != api.RoleMaster {
		t.Fatalf("роль bob: %+v", role)
	}
}

func TestUnknownRoleRejected(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := db.CreateCampaign("Алая тропа")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddMember(alice.ID, campaign, "король"); err == nil {
		t.Fatal("незнакомая роль принята")
	}
}

func TestSessions(t *testing.T) {
	db := openTemp(t)
	alice, err := db.CreateAccount("alice", "пароль")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	if err := db.Create("token-1", alice.ID, expires); err != nil {
		t.Fatal(err)
	}
	accountID, gotExpires, ok := db.Lookup("token-1")
	if !ok || accountID != alice.ID || !gotExpires.Equal(expires) {
		t.Fatalf("Lookup: %q, %v, %v", accountID, gotExpires, ok)
	}
	db.Delete("token-1")
	if _, _, ok := db.Lookup("token-1"); ok {
		t.Fatal("токен не удалён")
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vtt.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("первый Open: %v", err)
	}
	if _, err := first.CreateAccount("alice", "пароль"); err != nil {
		t.Fatal(err)
	}
	first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("второй Open: %v", err)
	}
	defer second.Close()
	if _, err := second.Authenticate("alice", "пароль"); err != nil {
		t.Fatalf("аккаунт не пережил повторное открытие: %v", err)
	}
}
