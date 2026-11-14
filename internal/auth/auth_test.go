package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"proximity/internal/config"
)

func TestBootstrap_Environment(t *testing.T) {
	directory := t.TempDir()
	store, err := config.Open(directory)
	require.NoError(t, err)
	t.Setenv("PROXIMITY_ADMIN_USER", "root")
	t.Setenv("PROXIMITY_ADMIN_PASSWORD", "secret123")
	message, err := Bootstrap(store, directory)
	require.NoError(t, err)
	require.Equal(t, "created administrator 'root' with password from environment", message)
	snapshot := store.Snapshot()
	user, found := snapshot.FindUser("root")
	require.True(t, found)
	require.Equal(t, config.RoleAdministrator, user.Role)
	require.True(t, VerifyPassword(user.PasswordHash, "secret123"))
	require.NoFileExists(t, filepath.Join(directory, initialPasswordFile))
	{
		message, err := Bootstrap(store, directory)
		require.NoError(t, err)
		require.Empty(t, message)
		require.Len(t, store.Snapshot().Users, 1)
	}
}

func TestBootstrap_Generated(t *testing.T) {
	directory := t.TempDir()
	store, err := config.Open(directory)
	require.NoError(t, err)
	t.Setenv("PROXIMITY_ADMIN_USER", "")
	t.Setenv("PROXIMITY_ADMIN_PASSWORD", "")
	message, err := Bootstrap(store, directory)
	require.NoError(t, err)
	require.Contains(t, message, "created administrator 'admin' with password '")
	data, err := os.ReadFile(filepath.Join(directory, initialPasswordFile))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	require.Equal(t, "admin", lines[0])
	require.Len(t, lines[1], 16)
	require.Contains(t, message, "'"+lines[1]+"'")
	snapshot := store.Snapshot()
	user, found := snapshot.FindUser("admin")
	require.True(t, found)
	require.True(t, VerifyPassword(user.PasswordHash, lines[1]))
	{
		directory := t.TempDir()
		store, err := config.Open(directory)
		require.NoError(t, err)
		t.Setenv("PROXIMITY_ADMIN_PASSWORD", "short")
		_, err = Bootstrap(store, directory)
		require.ErrorContains(t, err, "failed to hash password of 'admin'")
		t.Setenv("PROXIMITY_ADMIN_USER", "Bad Name")
		t.Setenv("PROXIMITY_ADMIN_PASSWORD", "secret123")
		_, err = Bootstrap(store, directory)
		require.ErrorContains(t, err, "failed to create administrator 'Bad Name'")
	}
}

func TestHashPassword_Verify(t *testing.T) {
	hash, err := HashPassword("correct horse")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(hash, "pbkdf2-sha256$600000$"))
	require.True(t, VerifyPassword(hash, "correct horse"))
	require.False(t, VerifyPassword(hash, "wrong horse"))
	{
		_, err := HashPassword("12345")
		require.EqualError(t, err, "invalid password length (5)")
	}
	{
		parts := strings.Split(hash, "$")
		require.False(t, VerifyPassword("", "correct horse"))
		require.False(t, VerifyPassword("md5$1$a$b", "correct horse"))
		require.False(t, VerifyPassword(parts[0]+"$x$"+parts[2]+"$"+parts[3], "correct horse"))
		require.False(t, VerifyPassword(parts[0]+"$0$"+parts[2]+"$"+parts[3], "correct horse"))
		require.False(t, VerifyPassword(parts[0]+"$1$!$"+parts[3], "correct horse"))
		require.False(t, VerifyPassword(parts[0]+"$1$"+parts[2]+"$!", "correct horse"))
	}
}

func TestLevel_Roles(t *testing.T) {
	require.Equal(t, 3, Level(config.RoleAdministrator))
	require.Equal(t, 2, Level(config.RoleOperator))
	require.Equal(t, 1, Level(config.RoleUser))
	require.Equal(t, 0, Level(config.RoleAnonymous))
	require.Equal(t, 0, Level("unknown"))
}

func TestParseToken_Problems(t *testing.T) {
	now := time.Unix(1700000000, 0)
	user := config.User{Name: "alice", Role: config.RoleOperator, PasswordHash: "x", Epoch: 2}
	users := config.Default()
	users.Users = []config.User{user}
	token, err := MakeToken("secret", user, now.Add(time.Hour))
	require.NoError(t, err)
	{
		result, err := ParseToken("secret", token, users, now)
		require.NoError(t, err)
		require.Equal(t, user, result)
	}
	{
		_, err := ParseToken("other", token, users, now)
		require.EqualError(t, err, "invalid token signature")
		_, err = ParseToken("secret", "no-dot", users, now)
		require.EqualError(t, err, "invalid token signature")
		_, err = ParseToken("secret", "!!!."+sign("secret", "!!!"), users, now)
		require.ErrorContains(t, err, "failed to decode token")
		_, err = ParseToken("secret", "e30x."+sign("secret", "e30x"), users, now)
		require.ErrorContains(t, err, "failed to decode token")
	}
	{
		_, err := ParseToken("secret", token, users, now.Add(time.Hour))
		require.EqualError(t, err, "token expired")
	}
	{
		users.Users[0].Epoch = 3
		_, err := ParseToken("secret", token, users, now)
		require.EqualError(t, err, "token revoked")
		users.Users = []config.User{}
		_, err = ParseToken("secret", token, users, now)
		require.EqualError(t, err, "token revoked")
	}
}
