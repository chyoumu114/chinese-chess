package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Game status values stored in games.status.
const (
	StatusWaiting  = "waiting"
	StatusPlaying  = "playing"
	StatusFinished = "finished"
)

// Game is one recorded match.
type Game struct {
	ID          int64      `json:"id"`
	RedUserID   *int64     `json:"redUserId,omitempty"`
	BlackUserID *int64     `json:"blackUserId,omitempty"`
	RedName     string     `json:"redName"`
	BlackName   string     `json:"blackName"`
	Status      string     `json:"status"`
	Outcome     string     `json:"outcome"`
	Reason      string     `json:"reason"`
	WinnerSide  string     `json:"winnerSide,omitempty"`
	FEN         string     `json:"fen"`
	MoveCount   int        `json:"moveCount"`
	RedMSLeft   int64      `json:"redMsLeft"`
	BlackMSLeft int64      `json:"blackMsLeft"`
	Rated       bool       `json:"rated"`
	AILevel     int        `json:"aiLevel"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

// Move is one recorded ply.
type Move struct {
	Ply      int       `json:"ply"`
	Side     string    `json:"side"`
	UCI      string    `json:"uci"`
	Notation string    `json:"notation"`
	FEN      string    `json:"fen"`
	Check    bool      `json:"check"`
	MSSpent  int       `json:"msSpent"`
	PlayedAt time.Time `json:"playedAt"`
}

const gameColumns = `id, red_user_id, black_user_id, red_name, black_name, status, outcome, reason,
	winner_side, fen, move_count, red_ms_left, black_ms_left, rated, ai_level,
	created_at, started_at, finished_at`

func scanGame(row interface {
	Scan(dest ...any) error
}) (Game, error) {
	var g Game
	var redID, blackID sql.NullInt64
	var started, finished sql.NullTime
	err := row.Scan(&g.ID, &redID, &blackID, &g.RedName, &g.BlackName, &g.Status, &g.Outcome,
		&g.Reason, &g.WinnerSide, &g.FEN, &g.MoveCount, &g.RedMSLeft, &g.BlackMSLeft,
		&g.Rated, &g.AILevel, &g.CreatedAt, &started, &finished)
	if err != nil {
		return Game{}, wrap(err)
	}
	if redID.Valid {
		v := redID.Int64
		g.RedUserID = &v
	}
	if blackID.Valid {
		v := blackID.Int64
		g.BlackUserID = &v
	}
	if started.Valid {
		t := started.Time
		g.StartedAt = &t
	}
	if finished.Valid {
		t := finished.Time
		g.FinishedAt = &t
	}
	return g, nil
}

// CreateGame inserts a new match row and returns it with its assigned id.
func (s *Store) CreateGame(ctx context.Context, g Game) (Game, error) {
	status := g.Status
	if status == "" {
		status = StatusWaiting
	}
	outcome := g.Outcome
	if outcome == "" {
		outcome = "ongoing"
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO games (red_user_id, black_user_id, red_name, black_name, status, outcome,
		                    fen, red_ms_left, black_ms_left, rated, ai_level)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullID(g.RedUserID), nullID(g.BlackUserID), g.RedName, g.BlackName, status, outcome,
		g.FEN, g.RedMSLeft, g.BlackMSLeft, g.Rated, g.AILevel)
	if err != nil {
		return Game{}, fmt.Errorf("store: create game: %w", wrap(err))
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Game{}, fmt.Errorf("store: create game id: %w", err)
	}
	return s.GameByID(ctx, id)
}

// GameByID loads one match.
func (s *Store) GameByID(ctx context.Context, id int64) (Game, error) {
	return scanGame(s.db.QueryRowContext(ctx,
		`SELECT `+gameColumns+` FROM games WHERE id = ?`, id))
}

// StartGame marks a waiting match as playing.
func (s *Store) StartGame(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET status = ?, started_at = NOW() WHERE id = ?`, StatusPlaying, id)
	return wrap(err)
}

// AddMove appends a ply and advances the game's denormalised counters.
func (s *Store) AddMove(ctx context.Context, gameID int64, m Move) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin move tx: %w", err)
	}
	defer tx.Rollback()

	checks := 0
	if m.Check {
		checks = 1
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO moves (game_id, ply, side, uci, notation, fen, check_give, ms_spent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		gameID, m.Ply, m.Side, m.UCI, m.Notation, m.FEN, checks, m.MSSpent); err != nil {
		return fmt.Errorf("store: insert move: %w", wrap(err))
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE games SET fen = ?, move_count = ?, status = ? WHERE id = ?`,
		m.FEN, m.Ply, StatusPlaying, gameID); err != nil {
		return fmt.Errorf("store: bump game counters: %w", wrap(err))
	}
	return tx.Commit()
}

// TruncateMoves removes every ply after keepPly and restores the game's
// denormalised position. It is the persistence half of a takeback: without it the
// next move that reuses a ply number collides with uk_moves_game_ply and the
// store silently drops the new branch.
func (s *Store) TruncateMoves(ctx context.Context, gameID int64, keepPly int, fen string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin truncate tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM moves WHERE game_id = ? AND ply > ?`, gameID, keepPly); err != nil {
		return fmt.Errorf("store: truncate moves: %w", wrap(err))
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE games SET fen = ?, move_count = ? WHERE id = ?`, fen, keepPly, gameID); err != nil {
		return fmt.Errorf("store: reset game counters: %w", wrap(err))
	}
	return tx.Commit()
}

// GameResult carries everything needed to record a finished game. It is a
// standalone struct, rather than the Game row type, so that packages which
// manage live games can persist results without importing this package's row
// types — and so that this package never has to import them.
type GameResult struct {
	Outcome     string
	Reason      string
	WinnerSide  string
	FEN         string
	MoveCount   int
	RedMSLeft   int64
	BlackMSLeft int64
}

// FinishGame records the result, the final position and the remaining clocks.
func (s *Store) FinishGame(ctx context.Context, id int64, r GameResult) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games
		    SET status = ?, outcome = ?, reason = ?, winner_side = ?, fen = ?,
		        move_count = ?, red_ms_left = ?, black_ms_left = ?, finished_at = NOW()
		  WHERE id = ?`,
		StatusFinished, r.Outcome, r.Reason, r.WinnerSide, r.FEN,
		r.MoveCount, r.RedMSLeft, r.BlackMSLeft, id)
	return wrap(err)
}

// SetClocks stores the remaining time for both sides.
func (s *Store) SetClocks(ctx context.Context, id int64, redMS, blackMS int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE games SET red_ms_left = ?, black_ms_left = ? WHERE id = ?`, redMS, blackMS, id)
	return wrap(err)
}

// Moves returns the played plies of a game in order.
func (s *Store) Moves(ctx context.Context, gameID int64) ([]Move, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT ply, side, uci, notation, fen, check_give, ms_spent, created_at
		   FROM moves WHERE game_id = ? ORDER BY ply ASC`, gameID)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()

	out := make([]Move, 0, 64)
	for rows.Next() {
		var m Move
		var check int
		if err := rows.Scan(&m.Ply, &m.Side, &m.UCI, &m.Notation, &m.FEN, &check, &m.MSSpent, &m.PlayedAt); err != nil {
			return nil, wrap(err)
		}
		m.Check = check != 0
		out = append(out, m)
	}
	return out, wrap(rows.Err())
}

// GameFilter narrows a game history query.
type GameFilter struct {
	// UserID limits results to games this account took part in.
	UserID int64
	// Status filters on games.status when set.
	Status string
	// Limit caps the number of rows; it defaults to 20 and caps at 100.
	Limit int
	// Offset skips rows for paging.
	Offset int
}

// ListGames returns matching games, newest first.
func (s *Store) ListGames(ctx context.Context, f GameFilter) ([]Game, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	query := `SELECT ` + gameColumns + ` FROM games`
	var where []string
	var args []any
	if f.UserID != 0 {
		where = append(where, "(red_user_id = ? OR black_user_id = ?)")
		args = append(args, f.UserID, f.UserID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if len(where) > 0 {
		query += " WHERE " + joinAnd(where)
	}
	query += " ORDER BY id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap(err)
	}
	defer rows.Close()

	out := make([]Game, 0, limit)
	for rows.Next() {
		g, err := scanGame(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, wrap(rows.Err())
}

// UserStats aggregates a player's record from the finished games table, which
// is authoritative even if the denormalised counters drift.
type UserStats struct {
	Wins   int `json:"wins"`
	Losses int `json:"losses"`
	Draws  int `json:"draws"`
	Total  int `json:"total"`
}

// StatsForUser counts finished games won, lost and drawn by the account.
func (s *Store) StatsForUser(ctx context.Context, userID int64) (UserStats, error) {
	var st UserStats
	err := s.db.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN (winner_side = 'red'  AND red_user_id  = ?)
		                      OR (winner_side = 'black' AND black_user_id = ?) THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN (winner_side = 'black' AND red_user_id  = ?)
		                      OR (winner_side = 'red'   AND black_user_id = ?) THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN outcome = 'draw'
		                      AND (red_user_id = ? OR black_user_id = ?) THEN 1 ELSE 0 END), 0),
		  COALESCE(SUM(1), 0)
		FROM games
		WHERE status = 'finished' AND (red_user_id = ? OR black_user_id = ?)`,
		userID, userID, userID, userID, userID, userID, userID, userID).
		Scan(&st.Wins, &st.Losses, &st.Draws, &st.Total)
	if err != nil {
		return UserStats{}, wrap(err)
	}
	return st, nil
}

func nullID(id *int64) any {
	if id == nil {
		return nil
	}
	return *id
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}
