package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// User is an account. PwHash never leaves the store.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// Session is a logged-in browser.
type Session struct {
	UserID   int64
	Username string
	CSRF     string
}

// APIKey is a key's metadata; the key itself is only shown at creation.
type APIKey struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

var (
	dummyOnce sync.Once
	dummyHash []byte
)

// ErrBadCredentials is returned for a wrong username or password.
var ErrBadCredentials = errors.New("wrong username or password")

// RandomToken returns n random bytes, hex-encoded.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// HashPassword bcrypts a password.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// CountUsers reports how many accounts exist.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.dbR.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser adds an account.
func (s *Store) CreateUser(username, password string) (*User, error) {
	h, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	res, err := s.dbW.Exec(`INSERT INTO users(username, pw_hash) VALUES(?,?)`, username, h)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &User{ID: id, Username: username}, nil
}

// CheckPassword returns the user when username/password match.
func (s *Store) CheckPassword(username, password string) (*User, error) {
	var u User
	var h string
	err := s.dbR.QueryRow(`SELECT id, username, pw_hash FROM users WHERE username=?`, username).Scan(&u.ID, &u.Username, &h)
	if err == sql.ErrNoRows {
		// Spend comparable time so a missing user isn't distinguishable.
		dummyOnce.Do(func() { dummyHash, _ = bcrypt.GenerateFromPassword([]byte("x"), bcrypt.DefaultCost) })
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(h), []byte(password)) != nil {
		return nil, ErrBadCredentials
	}
	return &u, nil
}

// SetPassword replaces a user's password and ends their other sessions.
func (s *Store) SetPassword(userID int64, password string) error {
	h, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.dbW.Exec(`UPDATE users SET pw_hash=? WHERE id=?`, h, userID)
	return err
}

// CreateSession starts a session and returns its token and CSRF token.
func (s *Store) CreateSession(userID int64, ttl time.Duration) (token, csrf string, err error) {
	token, csrf = RandomToken(32), RandomToken(16)
	_, err = s.dbW.Exec(`INSERT INTO sessions(token_hash, user_id, csrf, expires_at) VALUES(?,?,?,?)`,
		sha(token), userID, csrf, time.Now().Add(ttl).UTC().Format(time.RFC3339))
	return token, csrf, err
}

// SessionByToken returns the live session for token, or nil.
func (s *Store) SessionByToken(token string) (*Session, error) {
	var ss Session
	err := s.dbR.QueryRow(`SELECT s.user_id, u.username, s.csrf FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires_at > ?`, sha(token), time.Now().UTC().Format(time.RFC3339)).Scan(&ss.UserID, &ss.Username, &ss.CSRF)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &ss, err
}

// DeleteSession ends one session.
func (s *Store) DeleteSession(token string) error {
	_, err := s.dbW.Exec(`DELETE FROM sessions WHERE token_hash=?`, sha(token))
	return err
}

// DeleteUserSessions ends every session of a user except keepToken's.
func (s *Store) DeleteUserSessions(userID int64, keepToken string) error {
	_, err := s.dbW.Exec(`DELETE FROM sessions WHERE user_id=? AND token_hash != ?`, userID, sha(keepToken))
	return err
}

// PruneSessions drops expired sessions.
func (s *Store) PruneSessions() error {
	_, err := s.dbW.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, time.Now().UTC().Format(time.RFC3339))
	return err
}

// CreateAPIKey stores a new key and returns it (shown once) with its row.
func (s *Store) CreateAPIKey(name string) (string, *APIKey, error) {
	key := "dk_" + RandomToken(24)
	prefix := key[:9]
	res, err := s.dbW.Exec(`INSERT INTO api_keys(name, key_hash, prefix) VALUES(?,?,?)`, name, sha(key), prefix)
	if err != nil {
		return "", nil, err
	}
	id, _ := res.LastInsertId()
	return key, &APIKey{ID: id, Name: name, Prefix: prefix}, nil
}

// ListAPIKeys returns every key's metadata.
func (s *Store) ListAPIKeys() ([]APIKey, error) {
	rows, err := s.dbR.Query(`SELECT id, name, prefix, created_at, last_used_at FROM api_keys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteAPIKey revokes a key.
func (s *Store) DeleteAPIKey(id int64) error {
	_, err := s.dbW.Exec(`DELETE FROM api_keys WHERE id=?`, id)
	return err
}

// CheckAPIKey reports whether key is valid, recording its use.
func (s *Store) CheckAPIKey(key string) bool {
	res, err := s.dbW.Exec(`UPDATE api_keys SET last_used_at=? WHERE key_hash=?`, time.Now().UTC().Format(time.RFC3339), sha(key))
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n == 1
}
