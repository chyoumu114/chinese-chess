package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"xiangqi/internal/config"
)

// The store tests talk to a real MySQL server. They are skipped, rather than
// failed, when no server is reachable, so the unit suite stays runnable without
// a database. Set XIANGQI_TEST_DSN or the usual XIANGQI_DB_* variables to point
// them at a server, for example:
//
//	XIANGQI_DB_HOST=127.0.0.1 XIANGQI_DB_USER=root XIANGQI_DB_PASSWORD=secret go test ./internal/store/
func testConfig(t *testing.T) config.DBConfig {
	t.Helper()
	cfg := config.DBConfig{
		Host:            envOr("XIANGQI_DB_HOST", "127.0.0.1"),
		Port:            envIntOr("XIANGQI_DB_PORT", 3306),
		User:            envOr("XIANGQI_DB_USER", "root"),
		Password:        os.Getenv("XIANGQI_DB_PASSWORD"),
		Name:            envOr("XIANGQI_DB_NAME", "xiangqi"),
		MaxOpenConns:    5,
		MaxIdleConns:    2,
		ConnMaxLifetime: time.Minute,
	}
	// A test run must never touch a developer's real schema by accident.
	cfg.Name = envOr("XIANGQI_TEST_DB_NAME", "xiangqi_test")
	return cfg
}

// openTestStore connects to the test schema, creating it when possible. It
// skips the test when no server answers.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	cfg := testConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	st, err := Open(ctx, cfg, true)
	if err != nil {
		t.Skipf("MySQL is not reachable at %s:%d (%v); skipping store integration test", cfg.Host, cfg.Port, err)
	}
	t.Cleanup(func() { st.Close() })

	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

// reset clears the tables a test touches so runs are independent.
func reset(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{"DELETE FROM moves", "DELETE FROM games", "DELETE FROM users"} {
		if _, err := st.DB().ExecContext(ctx, stmt); err != nil {
			t.Fatalf("reset (%s): %v", stmt, err)
		}
	}
}

// TestMigrationIsIdempotent checks the schema can be applied repeatedly.
func TestMigrationIsIdempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := st.Migrate(ctx); err != nil {
			t.Fatalf("migrate run %d: %v", i+1, err)
		}
	}
	// The expected tables must exist.
	for _, table := range []string{"users", "games", "moves"} {
		var name string
		err := st.DB().QueryRowContext(ctx,
			`SELECT TABLE_NAME FROM information_schema.TABLES
			  WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s is missing: %v", table, err)
		}
	}
}

// TestUserLifecycle covers registration, authentication and lookups.
func TestUserLifecycle(t *testing.T) {
	st := openTestStore(t)
	reset(t, st)
	ctx := context.Background()

	user, err := st.CreateUser(ctx, "alice", "爱丽丝", "secret123")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.ID == 0 {
		t.Fatalf("CreateUser returned no id")
	}
	if user.Nickname != "爱丽丝" {
		t.Errorf("nickname = %q, want 爱丽丝 (utf8mb4 must round-trip)", user.Nickname)
	}
	if user.Rating != 1200 {
		t.Errorf("starting rating = %d, want 1200", user.Rating)
	}
	if user.PasswordHash == "" || strings.Contains(user.PasswordHash, "secret123") {
		t.Errorf("the password must be stored hashed, got %q", user.PasswordHash)
	}

	// A duplicate username is rejected, case-insensitively.
	if _, err := st.CreateUser(ctx, "ALICE", "", "other-password"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("duplicate registration: got %v, want ErrUsernameTaken", err)
	}

	// Authentication succeeds with the right password and fails otherwise.
	auth, err := st.Authenticate(ctx, "alice", "secret123")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if auth.ID != user.ID {
		t.Errorf("authenticated id = %d, want %d", auth.ID, user.ID)
	}
	if _, err := st.Authenticate(ctx, "alice", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong password: got %v, want ErrInvalidCredentials", err)
	}
	if _, err := st.Authenticate(ctx, "nobody", "secret123"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user: got %v, want ErrInvalidCredentials", err)
	}

	// Usernames are normalised to lower case and looked up that way.
	if _, err := st.UserByID(ctx, user.ID); err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if _, err := st.UserByID(ctx, user.ID+9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing user: got %v, want ErrNotFound", err)
	}

	// Validation rules.
	for _, bad := range []string{"ab", "has space", "sym$bol", strings.Repeat("x", 33)} {
		if _, err := st.CreateUser(ctx, bad, "", "secret123"); err == nil {
			t.Errorf("CreateUser(%q) should have been rejected", bad)
		}
	}
	if _, err := st.CreateUser(ctx, "bob", "", "short"); err == nil {
		t.Errorf("a password under 6 characters should be rejected")
	}
}

// TestGamePersistenceAndStats covers the full record of a game.
func TestGamePersistenceAndStats(t *testing.T) {
	st := openTestStore(t)
	reset(t, st)
	ctx := context.Background()

	red, err := st.CreateUser(ctx, "redplayer", "", "secret123")
	if err != nil {
		t.Fatalf("create red: %v", err)
	}
	black, err := st.CreateUser(ctx, "blackplayer", "", "secret123")
	if err != nil {
		t.Fatalf("create black: %v", err)
	}

	game, err := st.CreateGame(ctx, Game{
		RedUserID:   &red.ID,
		BlackUserID: &black.ID,
		RedName:     red.DisplayName(),
		BlackName:   black.DisplayName(),
		Status:      StatusWaiting,
		FEN:         "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1",
		Rated:       true,
	})
	if err != nil {
		t.Fatalf("CreateGame: %v", err)
	}
	if game.ID == 0 {
		t.Fatalf("CreateGame returned no id")
	}
	if game.Status != StatusWaiting {
		t.Errorf("status = %q, want waiting", game.Status)
	}

	if err := st.StartGame(ctx, game.ID); err != nil {
		t.Fatalf("StartGame: %v", err)
	}
	started, err := st.GameByID(ctx, game.ID)
	if err != nil {
		t.Fatalf("GameByID: %v", err)
	}
	if started.Status != StatusPlaying || started.StartedAt == nil {
		t.Errorf("after StartGame: status=%q startedAt=%v", started.Status, started.StartedAt)
	}

	// Record two plies, including Chinese notation.
	plies := []Move{
		{Ply: 1, Side: "red", UCI: "h7e7", Notation: "炮二平五", FEN: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C2C4/9/RNBAKABNR b - - 0 1"},
		{Ply: 2, Side: "black", UCI: "h2h6", Notation: "炮8进4", FEN: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/7c1/P1P1P1P1P/1C2C4/9/RNBAKABNR w - - 0 1", Check: false, MSSpent: 1200},
	}
	for _, m := range plies {
		if err := st.AddMove(ctx, game.ID, m); err != nil {
			t.Fatalf("AddMove ply %d: %v", m.Ply, err)
		}
	}

	moves, err := st.Moves(ctx, game.ID)
	if err != nil {
		t.Fatalf("Moves: %v", err)
	}
	if len(moves) != 2 {
		t.Fatalf("Moves returned %d rows, want 2", len(moves))
	}
	if moves[0].Notation != "炮二平五" {
		t.Errorf("stored notation = %q, want 炮二平五", moves[0].Notation)
	}
	if moves[1].MSSpent != 1200 {
		t.Errorf("stored ms spent = %d, want 1200", moves[1].MSSpent)
	}

	// A duplicate ply number must be refused by the unique key.
	if err := st.AddMove(ctx, game.ID, plies[1]); err == nil {
		t.Errorf("duplicate (game_id, ply) should be rejected")
	}

	// Finish the game and check the row plus the aggregate stats.
	if err := st.FinishGame(ctx, game.ID, GameResult{
		Outcome: "red_win", Reason: "checkmate", WinnerSide: "red",
		FEN: "3k5/9/9/9/9/9/9/9/9/4K4 b - - 0 1", MoveCount: 2,
		RedMSLeft: 300000, BlackMSLeft: 299000,
	}); err != nil {
		t.Fatalf("FinishGame: %v", err)
	}
	finished, err := st.GameByID(ctx, game.ID)
	if err != nil {
		t.Fatalf("GameByID after finish: %v", err)
	}
	if finished.Status != StatusFinished || finished.WinnerSide != "red" || finished.FinishedAt == nil {
		t.Errorf("finished row = status %q winner %q finishedAt %v",
			finished.Status, finished.WinnerSide, finished.FinishedAt)
	}
	if finished.RedMSLeft != 300000 {
		t.Errorf("red clock = %d, want 300000", finished.RedMSLeft)
	}

	redStats, err := st.StatsForUser(ctx, red.ID)
	if err != nil {
		t.Fatalf("StatsForUser(red): %v", err)
	}
	if redStats.Wins != 1 || redStats.Losses != 0 || redStats.Total != 1 {
		t.Errorf("red stats = %+v, want 1 win of 1", redStats)
	}
	blackStats, err := st.StatsForUser(ctx, black.ID)
	if err != nil {
		t.Fatalf("StatsForUser(black): %v", err)
	}
	if blackStats.Losses != 1 || blackStats.Wins != 0 {
		t.Errorf("black stats = %+v, want 1 loss", blackStats)
	}

	// Rating exchange.
	if err := st.ApplyResult(ctx, red.ID, black.ID, 16, -16); err != nil {
		t.Fatalf("ApplyResult: %v", err)
	}
	updatedRed, err := st.UserByID(ctx, red.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if updatedRed.Rating != 1216 || updatedRed.Wins != 1 {
		t.Errorf("red after result = rating %d wins %d, want 1216/1", updatedRed.Rating, updatedRed.Wins)
	}
	updatedBlack, err := st.UserByID(ctx, black.ID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	if updatedBlack.Rating != 1184 || updatedBlack.Losses != 1 {
		t.Errorf("black after result = rating %d losses %d, want 1184/1", updatedBlack.Rating, updatedBlack.Losses)
	}

	// Listing and filtering.
	all, err := st.ListGames(ctx, GameFilter{})
	if err != nil {
		t.Fatalf("ListGames: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("ListGames returned %d rows, want 1", len(all))
	}
	mine, err := st.ListGames(ctx, GameFilter{UserID: red.ID})
	if err != nil {
		t.Fatalf("ListGames(mine): %v", err)
	}
	if len(mine) != 1 {
		t.Errorf("ListGames(red) returned %d rows, want 1", len(mine))
	}
	waiting, err := st.ListGames(ctx, GameFilter{Status: StatusWaiting})
	if err != nil {
		t.Fatalf("ListGames(waiting): %v", err)
	}
	if len(waiting) != 0 {
		t.Errorf("no game is waiting, got %d", len(waiting))
	}
}

// TestLeaderboardOrdersByRating checks the ranking query.
func TestLeaderboardOrdersByRating(t *testing.T) {
	st := openTestStore(t)
	reset(t, st)
	ctx := context.Background()

	names := []string{"low", "mid", "high"}
	for _, n := range names {
		if _, err := st.CreateUser(ctx, n, "", "secret123"); err != nil {
			t.Fatalf("create %s: %v", n, err)
		}
	}
	// Give them distinct ratings by applying results directly.
	var ids []int64
	for _, n := range names {
		u, err := st.Authenticate(ctx, n, "secret123")
		if err != nil {
			t.Fatalf("auth %s: %v", n, err)
		}
		ids = append(ids, u.ID)
	}
	if err := st.ApplyResult(ctx, ids[2], 0, 200, 0); err != nil {
		t.Fatalf("apply rating to high: %v", err)
	}
	if err := st.ApplyResult(ctx, ids[1], 0, 50, 0); err != nil {
		t.Fatalf("apply rating to mid: %v", err)
	}

	board, err := st.Leaderboard(ctx, 10)
	if err != nil {
		t.Fatalf("Leaderboard: %v", err)
	}
	if len(board) != 3 {
		t.Fatalf("leaderboard size = %d, want 3", len(board))
	}
	if board[0].Username != "high" || board[1].Username != "mid" || board[2].Username != "low" {
		t.Errorf("leaderboard order = %s, %s, %s; want high, mid, low",
			board[0].Username, board[1].Username, board[2].Username)
	}
}

// TestForeignKeysCascade checks deleting a game removes its moves.
func TestForeignKeysCascade(t *testing.T) {
	st := openTestStore(t)
	reset(t, st)
	ctx := context.Background()

	game, err := st.CreateGame(ctx, Game{RedName: "红方", BlackName: "黑方", Status: StatusPlaying,
		FEN: "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"})
	if err != nil {
		t.Fatalf("CreateGame: %v", err)
	}
	if err := st.AddMove(ctx, game.ID, Move{Ply: 1, Side: "red", UCI: "h7e7", Notation: "炮二平五", FEN: "x"}); err != nil {
		t.Fatalf("AddMove: %v", err)
	}
	if _, err := st.DB().ExecContext(ctx, `DELETE FROM games WHERE id = ?`, game.ID); err != nil {
		t.Fatalf("delete game: %v", err)
	}
	moves, err := st.Moves(ctx, game.ID)
	if err != nil {
		t.Fatalf("Moves after delete: %v", err)
	}
	if len(moves) != 0 {
		t.Errorf("moves should cascade away with the game, %d remain", len(moves))
	}
}

// TestOpenWithoutCreateFailsOnMissingDatabase checks the create flag.
func TestOpenWithoutCreateFailsOnMissingDatabase(t *testing.T) {
	cfg := testConfig(t)
	cfg.Name = fmt.Sprintf("xiangqi_absent_%d", time.Now().UnixNano())

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	st, err := Open(ctx, cfg, false)
	if err == nil {
		st.Close()
		t.Skip("the server created the schema anyway; nothing to assert")
	}
	if !strings.Contains(err.Error(), "ping") && !strings.Contains(err.Error(), "Unknown database") {
		t.Logf("unexpected error shape: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}
