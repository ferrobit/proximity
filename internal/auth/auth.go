package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"proximity/internal/config"
)

type claims struct {
	Name    string `json:"n"`
	Epoch   int    `json:"e"`
	Expires int64  `json:"x"`
}

const hashScheme = "pbkdf2-sha256"
const hashIterations = 600000
const hashLength = 32
const minPasswordLength = 6

func HashPassword(password string) (string, error) {
	if len(password) < minPasswordLength {
		return "", fmt.Errorf("invalid password length (%v)", len(password))
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to read random data: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, hashIterations, hashLength)
	if err != nil {
		return "", fmt.Errorf("failed to derive password key: %w", err)
	}
	return fmt.Sprintf("%v$%v$%v$%v", hashScheme, hashIterations,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(hash string, password string) bool {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(expected))
	return err == nil && subtle.ConstantTimeCompare(key, expected) == 1
}

func MakeToken(secret string, user config.User, expires time.Time) (string, error) {
	payload, err := json.Marshal(claims{Name: user.Name, Epoch: user.Epoch, Expires: expires.Unix()})
	if err != nil {
		return "", fmt.Errorf("failed to encode token: %w", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + sign(secret, body), nil
}

func ParseToken(secret string, token string, users config.Config, now time.Time) (config.User,
	error) {
	body, signature, found := strings.Cut(token, ".")
	if !found || !hmac.Equal([]byte(signature), []byte(sign(secret, body))) {
		return config.User{}, errors.New("invalid token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return config.User{}, fmt.Errorf("failed to decode token: %w", err)
	}
	result := claims{}
	if err = json.Unmarshal(payload, &result); err != nil {
		return config.User{}, fmt.Errorf("failed to decode token: %w", err)
	} else if now.Unix() >= result.Expires {
		return config.User{}, errors.New("token expired")
	} else if user, found := users.FindUser(result.Name); !found || user.Epoch != result.Epoch {
		return config.User{}, errors.New("token revoked")
	} else {
		return user, nil
	}
}

func Level(role string) int {
	switch role {
	case config.RoleAdministrator:
		return 3
	case config.RoleOperator:
		return 2
	case config.RoleUser:
		return 1
	default:
		return 0
	}
}

func Bootstrap(store *config.Store) (string, error) {
	name, password := os.Getenv("PROXIMITY_ADMIN_USER"), os.Getenv("PROXIMITY_ADMIN_PASSWORD")
	if len(store.Snapshot().Users) != 0 || password == "" {
		return "", nil
	} else if name == "" {
		name = "admin"
	}
	if err := CreateAdministrator(store, name, password); err != nil {
		return "", err
	}
	return fmt.Sprintf("created administrator '%v' with password from environment", name), nil
}

func CreateAdministrator(store *config.Store, name string, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("failed to hash password of '%v': %w", name, err)
	}
	user := config.User{Name: name, Role: config.RoleAdministrator, PasswordHash: hash}
	if err = store.Update(func(config *config.Config) error {
		if len(config.Users) != 0 {
			return errors.New("administrator already exists")
		}
		return config.AddUser(user)
	}); err != nil {
		return fmt.Errorf("failed to create administrator '%v': %w", name, err)
	}
	return nil
}

func sign(secret string, body string) string {
	digest := hmac.New(sha256.New, []byte(secret))
	digest.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(digest.Sum(nil))
}
