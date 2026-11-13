// Package memstore is an in-memory implementation of the API's persistence
// surface. It exists so the whole HTTP and WebSocket path can be exercised
// end to end without a MySQL server, which is what `-store memory` selects.
//
// It is deliberately not a production store: everything lives in process
// memory and is lost on restart.
package memstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"xiangqi/internal/auth"
	"xiangqi/internal/store"
)

// Store is a thread-safe in-memory database.
type Store struct {
	mu     sync.RWMutex
	users  map[int64]store.User
	games  map[int64]store.Game
	moves  map[int64][]store.Move
	nextID int64
	byName map[string]int64
}

// New returns an empty store.
func New() *Store {
	return &Store{
		users:  make(map[int64]store.User),
		games:  make(map[int64]store.Game),
		moves:  make(map[int64][]store.Move),
		byName: make(map[string]int64),
	}
}

// Ping always succeeds: memory is always reachable.
func (s *Store) Ping(context.Context) error { return nil }

func (s *Store) newID() int64 {
	s.nextID++
	return s.nextID
}

// CreateUser registers an account, hashing the password exactly as the MySQL
// store does so the two behave identically from the API's point of view.
func (s *Store) CreateUser(ctx context.Context, username, nickname, password string) (store.User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if err := store.ValidateUsername(username); err != nil {
		return store.User{}, err
	}
	if err := store.ValidatePassword(password); err != nil {
		return store.User{}, err
	}
	nickname = strings.TrimSpace(nickname)
	if nickname == "" {
		nickname = username
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return store.User{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byName[username]; exists {
		return store.User{}, store.ErrUsernameTaken
	}
	id := s.newID()
	user := store.User{
		ID: id, Username: username, Nickname: nickname, PasswordHash: hash,
		Rating: 1200, CreatedAt: time.Now(),
	}
	s.users[id] = user
	s.byName[username] = id
	return user, nil
}

// Authenticate verifies a password.
func (s *Store) Authenticate(ctx context.Context, username, password string) (store.User, error) {
	username = strings.ToLower(strings.TrimSpace(username))

	s.mu.RLock()
	id, ok := s.byName[username]
	if !ok {
		s.mu.RUnlock()
		// Hash anyway so a missing account and a wrong password cost the same.
		_, _ = auth.HashPassword(password)
		return store.User{}, store.ErrInvalidCredentials
	}
	user := s.users[id]
	s.mu.RUnlock()

	ok, err := auth.VerifyPassword(password, user.PasswordHash)
	if err != nil || !ok {
		return store.User{}, store.ErrInvalidCredentials
	}
	return user, nil
}

// UserByID loads one account.
func (s *Store) UserByID(ctx context.Context, id int64) (store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	user, ok := s.users[id]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return user, nil
}

// Leaderboard returns accounts ordered by rating.
func (s *Store) Leaderboard(ctx context.Context, limit int) ([]store.User, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	s.mu.RLock()
	out := make([]store.User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	s.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rating != out[j].Rating {
			return out[i].Rating > out[j].Rating
		}
		if out[i].Wins != out[j].Wins {
			return out[i].Wins > out[j].Wins
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ApplyResult updates the counters and ratings of both seats.
func (s *Store) ApplyResult(ctx context.Context, redID, blackID int64, redDelta, blackDelta int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	apply := func(id int64, delta int, win, loss, draw int) {
		if id == 0 {
			return
		}
		u, ok := s.users[id]
		if !ok {
			return
		}
		u.Rating += delta
		if u.Rating < 100 {
			u.Rating = 100
		}
		u.Wins += win
		u.Losses += loss
		u.Draws += draw
		s.users[id] = u
	}
	apply(redID, redDelta, b2i(redDelta > 0), b2i(redDelta < 0), b2i(redDelta == 0))
	apply(blackID, blackDelta, b2i(blackDelta > 0), b2i(blackDelta < 0), b2i(blackDelta == 0))
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ---- games ----

// CreateGame inserts a game row.
func (s *Store) CreateGame(ctx context.Context, g store.Game) (store.Game, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.newID()
	g.ID = id
	if g.Status == "" {
		g.Status = store.StatusWaiting
	}
	if g.Outcome == "" {
		g.Outcome = "ongoing"
	}
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now()
	}
	s.games[id] = g
	s.moves[id] = nil
	return g, nil
}

// GameByID loads one game.
func (s *Store) GameByID(ctx context.Context, id int64) (store.Game, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g, ok := s.games[id]
	if !ok {
		return store.Game{}, store.ErrNotFound
	}
	return g, nil
}

// StartGame marks a game as playing.
func (s *Store) StartGame(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.games[id]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now()
	g.Status = store.StatusPlaying
	g.StartedAt = &now
	s.games[id] = g
	return nil
}

// AddMove appends a ply.
func (s *Store) AddMove(ctx context.Context, gameID int64, m store.Move) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.games[gameID]
	if !ok {
		return store.ErrNotFound
	}
	for _, existing := range s.moves[gameID] {
		if existing.Ply == m.Ply {
			return fmt.Errorf("%w: ply %d already recorded", store.ErrDuplicate, m.Ply)
		}
	}
	if m.PlayedAt.IsZero() {
		m.PlayedAt = time.Now()
	}
	s.moves[gameID] = append(s.moves[gameID], m)
	g.FEN = m.FEN
	g.MoveCount = m.Ply
	g.Status = store.StatusPlaying
	s.games[gameID] = g
	return nil
}

// TruncateMoves drops plies after keepPly and restores the game's position. It
// mirrors the SQL backend so the in-memory path behaves the same way during a
// takeback.
func (s *Store) TruncateMoves(ctx context.Context, gameID int64, keepPly int, fen string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	g, ok := s.games[gameID]
	if !ok {
		return store.ErrNotFound
	}
	kept := s.moves[gameID][:0]
	for _, m := range s.moves[gameID] {
		if m.Ply <= keepPly {
			kept = append(kept, m)
		}
	}
	s.moves[gameID] = kept
	g.FEN = fen
	g.MoveCount = keepPly
	s.games[gameID] = g
	return nil
}

// FinishGame records the result.
func (s *Store) FinishGame(ctx context.Context, id int64, r store.GameResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.games[id]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now()
	g.Status = store.StatusFinished
	g.Outcome = r.Outcome
	g.Reason = r.Reason
	g.WinnerSide = r.WinnerSide
	g.FEN = r.FEN
	g.MoveCount = r.MoveCount
	g.RedMSLeft = r.RedMSLeft
	g.BlackMSLeft = r.BlackMSLeft
	g.FinishedAt = &now
	s.games[id] = g
	return nil
}

// SetClocks stores the remaining time.
func (s *Store) SetClocks(ctx context.Context, id int64, redMS, blackMS int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.games[id]
	if !ok {
		return store.ErrNotFound
	}
	g.RedMSLeft = redMS
	g.BlackMSLeft = blackMS
	s.games[id] = g
	return nil
}

// Moves returns the played plies of a game.
func (s *Store) Moves(ctx context.Context, gameID int64) ([]store.Move, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.games[gameID]; !ok {
		return nil, store.ErrNotFound
	}
	out := make([]store.Move, len(s.moves[gameID]))
	copy(out, s.moves[gameID])
	sort.Slice(out, func(i, j int) bool { return out[i].Ply < out[j].Ply })
	return out, nil
}

// ListGames returns matching games, newest first.
func (s *Store) ListGames(ctx context.Context, f store.GameFilter) ([]store.Game, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	s.mu.RLock()
	all := make([]store.Game, 0, len(s.games))
	for _, g := range s.games {
		if f.UserID != 0 {
			isRed := g.RedUserID != nil && *g.RedUserID == f.UserID
			isBlack := g.BlackUserID != nil && *g.BlackUserID == f.UserID
			if !isRed && !isBlack {
				continue
			}
		}
		if f.Status != "" && g.Status != f.Status {
			continue
		}
		all = append(all, g)
	}
	s.mu.RUnlock()

	sort.Slice(all, func(i, j int) bool { return all[i].ID > all[j].ID })
	if f.Offset > 0 {
		if f.Offset >= len(all) {
			return []store.Game{}, nil
		}
		all = all[f.Offset:]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// StatsForUser aggregates a player's record from finished games.
func (s *Store) StatsForUser(ctx context.Context, userID int64) (store.UserStats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var st store.UserStats
	for _, g := range s.games {
		if g.Status != store.StatusFinished {
			continue
		}
		isRed := g.RedUserID != nil && *g.RedUserID == userID
		isBlack := g.BlackUserID != nil && *g.BlackUserID == userID
		if !isRed && !isBlack {
			continue
		}
		st.Total++
		switch {
		case g.Outcome == "draw":
			st.Draws++
		case g.WinnerSide == "red" && isRed, g.WinnerSide == "black" && isBlack:
			st.Wins++
		case g.WinnerSide == "red" && isBlack, g.WinnerSide == "black" && isRed:
			st.Losses++
		}
	}
	return st, nil
}
