package storage

import "testing"

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := hashPassword("пароль")
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if ok, err := verifyPassword(hash, "пароль"); err != nil || !ok {
		t.Fatalf("свой пароль не сошёлся: ok=%v err=%v", ok, err)
	}
	if ok, err := verifyPassword(hash, "не тот"); err != nil || ok {
		t.Fatalf("чужой пароль принят: ok=%v err=%v", ok, err)
	}
	if _, err := verifyPassword("не PHC", "пароль"); err == nil {
		t.Fatal("битый хэш не отвергнут")
	}
}

func TestPasswordHashesDiffer(t *testing.T) {
	// Соль случайная: два хэша одного пароля не совпадают.
	first, err := hashPassword("пароль")
	if err != nil {
		t.Fatal(err)
	}
	second, err := hashPassword("пароль")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("два хэша одного пароля совпали — соль не работает")
	}
}
