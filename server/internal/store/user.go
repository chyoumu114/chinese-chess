package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"xiangqi/internal/auth"
)

// User is a registered account.
type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	Nickname     string     `json:"nickname"`
	Rating       int        `json:"rating"`
	Wins         int        `json:"wins"`
	Losses       int        `json:"losses"`
	Draws        int        `json:"draws"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastSeenAt   *time.Time `json:"lastSeenAt,omitempty"`
	PasswordHash string     `json:"-"`
}

// DisplayName returns the nickname when set, otherwise the username.
func (u User) DisplayName() string {
	if strings.TrimSpace(u.Nickname) != "" {
		return u.Nickname
	}
	return u.Username
}

// GamesPlayed returns the number of finished games.
func (u User) GamesPlayed() int { return u.Wins + u.Losses + u.Draws }

// Validation errors shared with the API layer.
var (
	ErrUsernameTaken      = errors.New("store: username is already taken")
	ErrInvalidCredentials = errors.New("store: invalid username or password")
)

// userColumns is the shared SELECT list, kept in one place so every scan matches.
const userColumns = `id, username, nickname, password_hash, rating, wins, losses, draws, created_at, last_seen_at`

func scanUser(row interface {
	Scan(dest ...any) error
}) (User, error) {
	var u User
	var lastSeen sql.NullTime
	err := row.Scan(&u.ID, &u.Username, &u.Nickname, &u.PasswordHash,
		&u.Rating, &u.Wins, &u.Losses, &u.Draws, &u.CreatedAt, &lastSeen)
	if err != nil {
		return User{}, wrap(err)
	}
	if lastSeen.Valid {
		t := lastSeen.Time
		u.LastSeenAt = &t
	}
	return u, nil
}

// CreateUser registers a new account, hashing the password before storing it.
func (s *Store) CreateUser(ctx context.Context, username, nickname, password string) (User, error) {
	username = normalizeUsername(username)
	if err := ValidateUsername(username); err != nil {
		return User{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, err
	}
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		nickname = username
	}
	if len([]rune(nickname)) > 32 {
		return User{}, fmt.Errorf("store: nickname must be at most 32 characters")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return User{}, err
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, nickname, password_hash) VALUES (?, ?, ?)`,
		username, nickname, hash)
	if err != nil {
		if errors.Is(wrap(err), ErrDuplicate) {
			return User{}, ErrUsernameTaken
		}
		return User{}, fmt.Errorf("store: create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, fmt.Errorf("store: create user id: %w", err)
	}
	return s.UserByID(ctx, id)
}

// Authenticate verifies a username and password and returns the account.
func (s *Store) Authenticate(ctx context.Context, username, password string) (User, error) {
	username = normalizeUsername(username)
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ?`, username))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Hash anyway so a missing account and a wrong password take a
			// comparable amount of time.
			_, _ = auth.HashPassword(password)
			return User{}, ErrInvalidCredentials
		}
		return User{}, err
	}
	ok, err := auth.VerifyPassword(password, u.PasswordHash)
	if err != nil || !ok {
		return User{}, ErrInvalidCredentials
	}
	if err := s.TouchLastSeen(ctx, u.ID); err != nil {
		return User{}, err
	}
	return u, nil
}

// UserByID loads one account.
func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// TouchLastSeen records that the account was seen just now.
func (s *Store) TouchLastSeen(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET last_seen_at = NOW() WHERE id = ?`, id)
	return wrap(err)
}

// Leaderboard returns the highest-rated accounts.
func (s *Store) Leaderboard(ctx context.Context, limit int) ([]User, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users
		 ORDER BY rating DESC, wins DESC, id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()

	out := make([]User, 0, limit)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, wrap(rows.Err())
}

// ApplyResult updates both players' win/loss/draw counters and Elo ratings.
// ratingDelta values are added to the current ratings. Either user ID may be
// zero, which means the seat was taken by the computer or was empty.
func (s *Store) ApplyResult(ctx context.Context, redID, blackID int64, redDelta, blackDelta int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin result tx: %w", err)
	}
	defer tx.Rollback()

	type change struct {
		id              int64
		delta           int
		win, loss, draw int
	}
	var changes []change
	if redID != 0 {
		changes = append(changes, change{redID, redDelta, boolToInt(redDelta > 0), boolToInt(redDelta < 0), boolToInt(redDelta == 0)})
	}
	if blackID != 0 {
		changes = append(changes, change{blackID, blackDelta, boolToInt(blackDelta > 0), boolToInt(blackDelta < 0), boolToInt(blackDelta == 0)})
	}
	for _, c := range changes {
		if _, err := tx.ExecContext(ctx,
			`UPDATE users
			    SET rating = GREATEST(100, rating + ?),
			        wins   = wins   + ?,
			        losses = losses + ?,
			        draws  = draws  + ?
			  WHERE id = ?`,
			c.delta, c.win, c.loss, c.draw, c.id); err != nil {
			return fmt.Errorf("store: update user %d: %w", c.id, wrap(err))
		}
	}
	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ValidateUsername enforces the username rules shared by the API and the store.
func ValidateUsername(username string) error {
	if len(username) < 3 || len(username) > 32 {
		return fmt.Errorf("store: username must be 3 to 32 characters")
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Errorf("store: username may contain only letters, digits, underscore and hyphen")
		}
	}
	return nil
}

// ValidatePassword enforces the minimum password strength.
func ValidatePassword(password string) error {
	if len(password) < 6 {
		return fmt.Errorf("store: password must be at least 6 characters")
	}
	if len(password) > 128 {
		return fmt.Errorf("store: password must be at most 128 characters")
	}
	return nil
}

// normalizeUsername lowercases and trims a username so accounts are
// case-insensitive.
func normalizeUsername(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
