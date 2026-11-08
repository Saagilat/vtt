package storage

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Параметры Argon2id. Одна проверка за раз держится уровнем API; 64 МиБ на проверку —
// предложение из docs/open-questions.md, здесь оно становится числом.
const (
	argonMemory  = 64 * 1024 // КиБ
	argonTime    = 1
	argonThreads = 3
	argonKeyLen  = 32
	argonSaltLen = 16
)

// hashPassword возвращает строку в формате PHC:
// $argon2id$v=19$m=65536,t=1,p=3$<соль>$<хеш>.
func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(sum)), nil
}

// verifyPassword сверяет пароль с PHC-строкой. Ошибка — только если строка битая;
// неверный пароль — это false, а не ошибка.
func verifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("хэш пароля не в формате PHC")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("неизвестная версия argon2")
	}
	var memory, timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false, errors.New("параметры argon2 не читаются")
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("соль не читается: %w", err)
	}
	sum, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("хеш не читается: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(sum)))
	return subtle.ConstantTimeCompare(got, sum) == 1, nil
}
