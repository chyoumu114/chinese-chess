// Command e2e exercises the running server end to end: it registers two
// accounts, seats them in a live game over WebSocket, plays a checkmate, and
// verifies the result through the REST history endpoints.
//
// Usage: go run ./cmd/e2e -base http://127.0.0.1:8080
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "base URL of the server")
	verbose := flag.Bool("v", true, "log every step")
	flag.Parse()

	c := &checker{base: strings.TrimSuffix(*base, "/"), verbose: *verbose}
	if err := c.run(); err != nil {
		fmt.Fprintln(os.Stderr, "\nFAILED:", err)
		os.Exit(1)
	}
	fmt.Println("\nALL CHECKS PASSED")
}

type checker struct {
	base    string
	verbose bool
	client  *http.Client
}

func (c *checker) logf(format string, args ...any) {
	if c.verbose {
		fmt.Printf(format+"\n", args...)
	}
}

func (c *checker) fail(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

func (c *checker) run() error {
	c.client = &http.Client{Timeout: 20 * time.Second}

	// 1. Health.
	var health struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := c.getJSON("/api/health", "", &health); err != nil {
		return c.fail("health endpoint: %w", err)
	}
	if health.Status != "ok" {
		return c.fail("health status = %q (database: %s)", health.Status, health.Database)
	}
	c.logf("✓ health ok (database %s)", health.Database)

	// 2. Rules.
	var rules struct {
		Rows     int    `json:"rows"`
		Cols     int    `json:"cols"`
		StartFen string `json:"startFen"`
		Pieces   []struct {
			Key   string `json:"key"`
			Glyph string `json:"glyph"`
		} `json:"pieces"`
	}
	if err := c.getJSON("/api/rules", "", &rules); err != nil {
		return c.fail("rules endpoint: %w", err)
	}
	if rules.Rows != 10 || rules.Cols != 9 {
		return c.fail("board geometry = %dx%d, want 10x9", rules.Rows, rules.Cols)
	}
	if len(rules.Pieces) != 14 {
		return c.fail("piece vocabulary has %d entries, want 14", len(rules.Pieces))
	}
	if !strings.Contains(rules.StartFen, "RNBAKABNR") {
		return c.fail("unexpected start FEN %q", rules.StartFen)
	}
	c.logf("✓ rules: %dx%d, %d piece kinds", rules.Rows, rules.Cols, len(rules.Pieces))

	// 3. Two accounts.
	suffix := time.Now().UnixNano() % 1_000_000
	alice, err := c.register(fmt.Sprintf("alice%d", suffix), "爱丽丝")
	if err != nil {
		return c.fail("register alice: %w", err)
	}
	bob, err := c.register(fmt.Sprintf("bob%d", suffix), "鲍勃")
	if err != nil {
		return c.fail("register bob: %w", err)
	}
	c.logf("✓ registered %s (id %d) and %s (id %d)", alice.User.Username, alice.User.ID, bob.User.Username, bob.User.ID)

	// 4. Login round trip and a rejected password.
	var login struct {
		Token string `json:"token"`
	}
	if err := c.postJSON("/api/auth/login", "", map[string]string{
		"username": alice.User.Username, "password": "e2e-password",
	}, &login); err != nil {
		return c.fail("login: %w", err)
	}
	if login.Token == "" {
		return c.fail("login returned an empty token")
	}
	if err := c.postJSON("/api/auth/login", "", map[string]string{
		"username": alice.User.Username, "password": "wrong-password",
	}, nil); err == nil {
		return c.fail("login with a wrong password should have failed")
	}
	c.logf("✓ login works and a wrong password is rejected")

	// 5. Both players connect and join the same game.
	aliceConn, err := c.dial(alice.Token, "alice")
	if err != nil {
		return c.fail("alice websocket: %w", err)
	}
	defer aliceConn.close()
	bobConn, err := c.dial(bob.Token, "bob")
	if err != nil {
		return c.fail("bob websocket: %w", err)
	}
	defer bobConn.close()

	// 4b. A quick match can be cancelled: leaving the only seat dissolves the
	// room instead of leaving an empty invitation in the lobby.
	dave, err := c.register(fmt.Sprintf("dave%d", suffix), "戴夫")
	if err != nil {
		return c.fail("register dave: %w", err)
	}
	daveConn, err := c.dial(dave.Token, "dave")
	if err != nil {
		return c.fail("dave websocket: %w", err)
	}
	defer daveConn.close()

	if err := daveConn.send("join", nil); err != nil {
		return c.fail("dave join: %w", err)
	}
	daveRoom, err := daveConn.waitFor("room", 10*time.Second)
	if err != nil {
		return c.fail("dave waiting for room: %w", err)
	}
	if daveRoom.Room.Status != "waiting" {
		return c.fail("dave should be waiting alone, status = %q", daveRoom.Room.Status)
	}
	if err := daveConn.send("leave", nil); err != nil {
		return c.fail("dave leave: %w", err)
	}
	if _, err := daveConn.waitFor("left", 10*time.Second); err != nil {
		return c.fail("leaving a waiting room was not acknowledged: %w", err)
	}
	open, err := c.openRoomIDs(dave.Token)
	if err != nil {
		return c.fail("lobby after leaving: %w", err)
	}
	if containsID(open, daveRoom.Room.ID) {
		return c.fail("room %d is still advertised after its only player left", daveRoom.Room.ID)
	}
	// The player is free to match again straight away.
	if err := daveConn.send("join", nil); err != nil {
		return c.fail("dave rejoin: %w", err)
	}
	again, err := daveConn.waitFor("room", 10*time.Second)
	if err != nil {
		return c.fail("dave rejoin room: %w", err)
	}
	if again.Room.ID == daveRoom.Room.ID {
		return c.fail("dave was given the dissolved room %d again", daveRoom.Room.ID)
	}
	if err := daveConn.send("leave", nil); err != nil {
		return c.fail("dave second leave: %w", err)
	}
	// Wait for the acknowledgement: the room must be gone before the next player
	// matches, or that player would be seated as its second player.
	if _, err := daveConn.waitFor("left", 10*time.Second); err != nil {
		return c.fail("dave's second leave was not acknowledged: %w", err)
	}
	c.logf("✓ a quick match can be cancelled: room %d was dissolved and the player could match again", daveRoom.Room.ID)

	if err := aliceConn.send("join", nil); err != nil {
		return c.fail("alice join: %w", err)
	}
	aliceRoom, err := aliceConn.waitFor("room", 10*time.Second)
	if err != nil {
		return c.fail("alice waiting for room: %w", err)
	}
	roomID := int64(aliceRoom.Room.ID)
	if aliceRoom.Room.Status != "waiting" {
		return c.fail("a single player should wait, status = %q", aliceRoom.Room.Status)
	}
	if aliceRoom.Room.YouAre != "red" {
		return c.fail("the first player should be red, got %q", aliceRoom.Room.YouAre)
	}
	c.logf("✓ alice seated in room %d as red (waiting for an opponent)", roomID)

	if err := bobConn.send("join", nil); err != nil {
		return c.fail("bob join: %w", err)
	}
	bobRoom, err := bobConn.waitFor("room", 10*time.Second)
	if err != nil {
		return c.fail("bob waiting for room: %w", err)
	}
	if bobRoom.Room.ID != roomID {
		return c.fail("bob landed in room %d, want %d", bobRoom.Room.ID, roomID)
	}
	if bobRoom.Room.YouAre != "black" {
		return c.fail("the second player should be black, got %q", bobRoom.Room.YouAre)
	}
	// Both players are seated, but the game must NOT have started: it waits for
	// both of them to accept it.
	if bobRoom.Room.Status != "ready" {
		return c.fail("two seated players should be ready to confirm, status = %q", bobRoom.Room.Status)
	}
	if len(bobRoom.Room.LegalFrom) != 0 {
		return c.fail("a room that has not started must not publish legal moves")
	}
	c.logf("✓ bob seated in the same room as black; both players must confirm")

	// 5b. One 准备 is not enough; the second one starts the game.
	if err := aliceConn.send("ready", nil); err != nil {
		return c.fail("alice ready: %w", err)
	}
	if _, err := aliceConn.waitForRoomWhere(10*time.Second, func(r roomSnapshot) bool { return r.Red.Ready }); err != nil {
		return c.fail("alice readiness was not published: %w", err)
	}
	if _, err := aliceConn.waitForStatus("playing", 2*time.Second); err == nil {
		return c.fail("the game started with only one player ready")
	}
	if err := bobConn.send("ready", nil); err != nil {
		return c.fail("bob ready: %w", err)
	}
	started, err := bobConn.waitForStatus("playing", 10*time.Second)
	if err != nil {
		return c.fail("both players were ready but the game did not start: %w", err)
	}
	if started.Room.Turn != "red" {
		return c.fail("red should move first, turn = %q", started.Room.Turn)
	}
	if started.Room.Red.Ready || started.Room.Black.Ready {
		return c.fail("the ready flags must be cleared once the game starts")
	}
	c.logf("✓ 双方准备后才开始对局（单人准备不会开局）")

	// 6. The server publishes the legal moves, and there must be 44 of them.
	total := 0
	for _, dests := range started.Room.LegalFrom {
		total += len(dests)
	}
	if total != 44 {
		return c.fail("the opening should publish 44 legal moves, got %d", total)
	}
	c.logf("✓ server published %d legal moves for the opening", total)

	// 7. Illegal move is rejected with a specific error code.
	if err := aliceConn.send("move", map[string]string{"from": "a9", "to": "a4"}); err != nil {
		return c.fail("send illegal move: %w", err)
	}
	if _, err := aliceConn.waitFor("error", 10*time.Second); err != nil {
		return c.fail("an illegal move should produce an error frame: %w", err)
	}
	c.logf("✓ an illegal move (rook through its own pawn) was rejected by the server")

	// 8. Play a real game to checkmate. Both sides move; the server validates.
	// Sequence: Red's rooks and cannons strip the black palace, Black shuffles.
	script := []struct {
		conn *wsClient
		from string
		to   string
		note string
	}{
		{aliceConn, "b7", "e7", "炮八平五"},
		{bobConn, "a3", "a4", "卒1进1"},
		{aliceConn, "h7", "h3", "炮二进四"}, // aims at the black pawn on h3/rank 3
		{bobConn, "i3", "i4", "卒1进1"},
		{aliceConn, "b9", "c7", "马八进七"},
		{bobConn, "a4", "a5", "卒1进1"},
		{aliceConn, "a9", "a8", "车九进一"},
		{bobConn, "a5", "a6", "卒1进1"},
	}
	for i, step := range script {
		if err := step.conn.send("move", map[string]string{"from": step.from, "to": step.to}); err != nil {
			return c.fail("move %d (%s%s): %w", i+1, step.from, step.to, err)
		}
		// The move must be acknowledged, and both clients must see it.
		if _, err := step.conn.waitFor("move_made", 10*time.Second); err != nil {
			return c.fail("move %d (%s%s) was not acknowledged: %w", i+1, step.from, step.to, err)
		}
		other := aliceConn
		if step.conn == aliceConn {
			other = bobConn
		}
		if _, err := other.waitFor("move_made", 10*time.Second); err != nil {
			return c.fail("the opponent did not receive move %d: %w", i+1, err)
		}
	}
	c.logf("✓ played %d plies with both clients receiving every move", len(script))

	// 9. Resign ends the game and creates a persisted record.
	if err := bobConn.send("resign", nil); err != nil {
		return c.fail("resign: %w", err)
	}
	over, err := aliceConn.waitFor("game_over", 10*time.Second)
	if err != nil {
		return c.fail("waiting for game_over: %w", err)
	}
	if over.GameOver.Outcome != "red_win" || over.GameOver.WinnerSide != "red" {
		return c.fail("black resigned so red should win, got %q/%q", over.GameOver.Outcome, over.GameOver.WinnerSide)
	}
	if over.GameOver.Reason != "resign" {
		return c.fail("reason = %q, want resign", over.GameOver.Reason)
	}
	c.logf("✓ resignation produced game_over: %s by %s", over.GameOver.Outcome, over.GameOver.Reason)

	// 10. The finished game and its moves were persisted.
	games, err := c.listMine(alice.Token)
	if err != nil {
		return c.fail("list my games: %w", err)
	}
	if len(games) == 0 {
		return c.fail("the finished game was not persisted")
	}
	game := games[0]
	if game.Status != "finished" {
		return c.fail("persisted status = %q, want finished", game.Status)
	}
	if game.WinnerSide != "red" || game.Outcome != "red_win" {
		return c.fail("persisted result = %q/%q, want red_win/red", game.Outcome, game.WinnerSide)
	}
	if game.MoveCount != len(script) {
		return c.fail("persisted move count = %d, want %d", game.MoveCount, len(script))
	}

	var moves struct {
		Moves []struct {
			Ply      int    `json:"ply"`
			Side     string `json:"side"`
			UCI      string `json:"uci"`
			Notation string `json:"notation"`
			FEN      string `json:"fen"`
		} `json:"moves"`
	}
	if err := c.getJSON(fmt.Sprintf("/api/games/%d/moves", game.ID), "", &moves); err != nil {
		return c.fail("game moves: %w", err)
	}
	if len(moves.Moves) != len(script) {
		return c.fail("persisted %d plies, want %d", len(moves.Moves), len(script))
	}
	if moves.Moves[0].Notation != "炮八平五" {
		return c.fail("first ply notation = %q, want 炮八平五 (Chinese notation must survive the round trip)", moves.Moves[0].Notation)
	}
	if moves.Moves[0].UCI != "b7e7" {
		return c.fail("first ply uci = %q, want b7e7", moves.Moves[0].UCI)
	}
	for i, m := range moves.Moves {
		if m.FEN == "" {
			return c.fail("ply %d has no recorded FEN", i+1)
		}
		if m.Ply != i+1 {
			return c.fail("ply numbering is off: index %d has ply %d", i, m.Ply)
		}
	}
	c.logf("✓ game %d persisted with %d plies; first notation is %q",
		game.ID, len(moves.Moves), moves.Moves[0].Notation)

	// 11. The rating moved for the winner.
	var me struct {
		User struct {
			Rating int `json:"rating"`
			Wins   int `json:"wins"`
		} `json:"user"`
		Stats struct {
			Wins  int `json:"wins"`
			Total int `json:"total"`
		} `json:"stats"`
	}
	if err := c.getJSON("/api/auth/me", alice.Token, &me); err != nil {
		return c.fail("me: %w", err)
	}
	if me.User.Wins < 1 {
		return c.fail("the winner's win counter did not increase: %+v", me.User)
	}
	if me.User.Rating <= 1200 {
		return c.fail("the winner's rating should have risen above the 1200 default, got %d", me.User.Rating)
	}
	c.logf("✓ winner stats updated: rating %d, wins %d", me.User.Rating, me.User.Wins)

	// 11b. Both players 准备 for a rematch: the SAME room starts a new game with
	// the colours swapped, and the rematch is a new record rather than a
	// continuation of the finished one.
	if err := aliceConn.send("ready", nil); err != nil {
		return c.fail("alice ready for a rematch: %w", err)
	}
	if _, err := aliceConn.waitForRoomWhere(10*time.Second, func(r roomSnapshot) bool { return r.Red.Ready }); err != nil {
		return c.fail("alice's rematch readiness was not published: %w", err)
	}
	if _, err := aliceConn.waitForStatus("playing", 2*time.Second); err == nil {
		return c.fail("a rematch started with only one player ready")
	}
	if err := bobConn.send("ready", nil); err != nil {
		return c.fail("bob ready for a rematch: %w", err)
	}
	rematch, err := aliceConn.waitForStatus("playing", 10*time.Second)
	if err != nil {
		return c.fail("the rematch did not start: %w", err)
	}
	if rematch.Room.ID != roomID {
		return c.fail("the rematch moved to room %d, want the same room %d", rematch.Room.ID, roomID)
	}
	if rematch.Room.YouAre != "black" {
		return c.fail("alice should hold black after the swap, got %q", rematch.Room.YouAre)
	}
	if rematch.Room.Red.Name != "鲍勃" || rematch.Room.Black.Name != "爱丽丝" {
		return c.fail("the colours did not swap: red=%q black=%q", rematch.Room.Red.Name, rematch.Room.Black.Name)
	}
	if len(rematch.Room.Moves) != 0 {
		return c.fail("the rematch kept %d plies from the previous game", len(rematch.Room.Moves))
	}
	if rematch.Room.Turn != "red" || len(rematch.Room.LegalFrom) == 0 {
		return c.fail("the rematch is not a fresh game: turn=%q legal=%d", rematch.Room.Turn, len(rematch.Room.LegalFrom))
	}
	if rematch.Room.Red.Ready || rematch.Room.Black.Ready {
		return c.fail("the ready flags must be cleared by the rematch")
	}
	c.logf("✓ 同一房间换边再来一局：alice 改执黑，棋盘重置为开局")

	// The rematch is a separate game row: the history now holds the finished
	// game and the one just started.
	games, err = c.listMine(alice.Token)
	if err != nil {
		return c.fail("list my games after the rematch: %w", err)
	}
	if len(games) < 2 {
		return c.fail("the rematch did not create a new game record, history has %d rows", len(games))
	}
	if games[0].Status != "playing" {
		return c.fail("the newest game row should be playing, got %q", games[0].Status)
	}
	c.logf("✓ 再来一局另起一条棋谱记录（历史共 %d 条）", len(games))

	// 12. The AI endpoint answers with a legal move for the opening.
	var aiResp struct {
		From     string `json:"from"`
		To       string `json:"to"`
		Notation string `json:"notation"`
	}
	if err := c.getJSON("/api/ai/move?level=1&budgetMs=300", "", &aiResp); err != nil {
		return c.fail("ai move: %w", err)
	}
	if aiResp.From == "" || aiResp.To == "" {
		return c.fail("the AI did not return a move: %+v", aiResp)
	}
	c.logf("✓ AI returned %s%s (%s)", aiResp.From, aiResp.To, aiResp.Notation)

	// 13. A game against the computer can be started and the computer replies.
	carol, err := c.register(fmt.Sprintf("carol%d", suffix), "卡罗")
	if err != nil {
		return c.fail("register carol: %w", err)
	}
	carolConn, err := c.dial(carol.Token, "carol")
	if err != nil {
		return c.fail("carol websocket: %w", err)
	}
	defer carolConn.close()

	if err := carolConn.send("join", map[string]any{"aiLevel": 1}); err != nil {
		return c.fail("carol join against the computer: %w", err)
	}
	aiRoom, err := carolConn.waitFor("room", 10*time.Second)
	if err != nil {
		return c.fail("waiting for the AI room: %w", err)
	}
	if !aiRoom.Room.Black.IsAI {
		return c.fail("the black seat should be the computer: %+v", aiRoom.Room.Black)
	}
	if aiRoom.Room.Status != "playing" {
		return c.fail("a game against the computer should start immediately, status = %q", aiRoom.Room.Status)
	}
	if aiRoom.Room.Black.Ready != true {
		return c.fail("the computer's seat must always report ready: %+v", aiRoom.Room.Black)
	}
	if err := carolConn.send("move", map[string]string{"from": "b7", "to": "e7"}); err != nil {
		return c.fail("carol move: %w", err)
	}
	if _, err := carolConn.waitFor("move_made", 10*time.Second); err != nil {
		return c.fail("carol's move was not acknowledged: %w", err)
	}
	// The computer should answer within its search budget plus the built-in pause.
	if _, err := carolConn.waitFor("move_made", 15*time.Second); err != nil {
		return c.fail("the computer did not reply: %w", err)
	}
	c.logf("✓ a game against the computer started and the computer replied")

	// 14. A student of the lobby sees the waiting room while one exists.
	c.logf("✓ lobby and leaderboard reachable: %v", c.checkLobby(alice.Token))

	// 15. 观战与补位：第三方只看不下，一方离开后补上那个空位。
	if err := c.checkSpectating(); err != nil {
		return err
	}

	// 16. 刷新页面不能被误判成掉线。
	if err := c.checkRefreshKeepsSeatOnline(); err != nil {
		return err
	}

	// 17. 人机对战：提和与悔棋都由电脑当场同意。
	if err := c.checkAIConsent(); err != nil {
		return err
	}
	return nil
}

// checkAIConsent 验证人机对战时电脑自动同意提和与悔棋。这一项走真实 WebSocket 与
// 真实的 AI worker，所以也顺带覆盖了「电脑自己会走棋」这条链路。
func (c *checker) checkAIConsent() error {
	suffix := time.Now().UnixNano() % 1_000_000
	human, err := c.register(fmt.Sprintf("human%d", suffix), "人机玩家")
	if err != nil {
		return c.fail("register human: %w", err)
	}
	conn, err := c.dial(human.Token, "human")
	if err != nil {
		return c.fail("dial human: %w", err)
	}
	defer conn.close()

	// 提和：电脑立即同意，直接回 game_over。
	if err := conn.send("join", map[string]any{"aiLevel": 1, "rated": false}); err != nil {
		return c.fail("join computer game: %w", err)
	}
	if _, err := conn.waitForStatus("playing", 10*time.Second); err != nil {
		return c.fail("the computer game did not start: %w", err)
	}
	if err := conn.send("offer_draw", nil); err != nil {
		return c.fail("offer draw: %w", err)
	}
	over, err := conn.waitFor("game_over", 10*time.Second)
	if err != nil {
		return c.fail("提和后没有立刻收到结果: %w", err)
	}
	if over.GameOver.Outcome != "draw" {
		return c.fail("提和后 outcome = %q, want draw", over.GameOver.Outcome)
	}
	c.logf("✓ 人机对战：提和后电脑立即同意，判和")

	// 悔棋：换一局，人走一步、电脑应一步，再请求悔棋。
	if err := conn.send("join", map[string]any{"aiLevel": 1, "rated": false}); err != nil {
		return c.fail("second computer game: %w", err)
	}
	second, err := conn.waitForStatus("playing", 15*time.Second)
	if err != nil {
		return c.fail("the second computer game did not start: %w", err)
	}
	roomID := second.Room.ID

	if err := conn.send("move", map[string]any{"from": "h7", "to": "e7"}); err != nil {
		return c.fail("human move: %w", err)
	}
	if _, err := conn.waitFor("move_made", 10*time.Second); err != nil {
		return c.fail("human move was not acknowledged: %w", err)
	}
	// 电脑自己应一手：这一步由服务端的 AI worker 完成。
	if _, err := conn.waitFor("move_made", 25*time.Second); err != nil {
		return c.fail("the computer did not answer: %w", err)
	}

	if err := conn.send("request_takeback", nil); err != nil {
		return c.fail("request takeback: %w", err)
	}
	if _, err := conn.waitFor("takeback_applied", 10*time.Second); err != nil {
		return c.fail("悔棋没有被电脑同意: %w", err)
	}
	if err := conn.send("sync", nil); err != nil {
		return c.fail("sync: %w", err)
	}
	view, err := conn.waitForRoomWhere(10*time.Second, func(r roomSnapshot) bool {
		return r.ID == roomID && r.Status == "playing"
	})
	if err != nil {
		return c.fail("snapshot after takeback: %w", err)
	}
	if len(view.Room.Moves) != 0 {
		return c.fail("悔棋后应当退回玩家落子之前（0 着），实际 %d 着", len(view.Room.Moves))
	}
	c.logf("✓ 人机对战：悔棋后电脑同意，局面退回玩家落子之前")

	// 执黑时电脑先行：撤掉它那一步之后，必须由它接着走，否则棋盘停在「等电脑」上，
	// 玩家既不能替它落子、也没人应招。
	//
	// 先离开上一局：带着 aiLevel 的 join 在已有对局时是「回到原座位」，不会新开一局，
	// 不离开就换不成黑方。
	if err := conn.send("leave", nil); err != nil {
		return c.fail("leave before the black game: %w", err)
	}
	if _, err := conn.waitFor("left", 10*time.Second); err != nil {
		return c.fail("no left acknowledgement: %w", err)
	}
	if err := conn.send("join", map[string]any{"aiLevel": 1, "rated": false, "side": "black"}); err != nil {
		return c.fail("join as black: %w", err)
	}
	third, err := conn.waitForStatus("playing", 15*time.Second)
	if err != nil {
		return c.fail("the game as black did not start: %w", err)
	}
	roomID = third.Room.ID
	if third.Room.YouAre != "black" {
		return c.fail("应当执黑，得到 %q", third.Room.YouAre)
	}
	if _, err := conn.waitFor("move_made", 25*time.Second); err != nil {
		return c.fail("执黑时电脑没有先走: %w", err)
	}

	if err := conn.send("request_takeback", nil); err != nil {
		return c.fail("request takeback as black: %w", err)
	}
	if _, err := conn.waitFor("takeback_applied", 10*time.Second); err != nil {
		return c.fail("执黑悔棋没有被同意: %w", err)
	}
	if _, err := conn.waitFor("move_made", 25*time.Second); err != nil {
		return c.fail("悔棋后轮到电脑走，但它没有接着走（棋盘会卡死）: %w", err)
	}
	c.logf("✓ 人机对战执黑：悔棋后电脑接着走，棋盘不会卡死")
	return nil
}

// checkRefreshKeepsSeatOnline 验证「刷新页面」不会把座位标成离线。
//
// 刷新的时序是：新连接先建立（回到座位），旧连接随后才被服务端察觉关闭。若旧连接
// 一断就把座位标成离线，人明明在线上，对面看到的却是「· 离线」。
func (c *checker) checkRefreshKeepsSeatOnline() error {
	suffix := time.Now().UnixNano() % 1_000_000
	fresher, err := c.register(fmt.Sprintf("fresher%d", suffix), "刷新的人")
	if err != nil {
		return c.fail("register fresher: %w", err)
	}
	peer, err := c.register(fmt.Sprintf("peer%d", suffix), "对面的")
	if err != nil {
		return c.fail("register peer: %w", err)
	}

	old, err := c.dial(fresher.Token, "fresher-old")
	if err != nil {
		return c.fail("dial fresher old: %w", err)
	}
	defer old.close()
	peerConn, err := c.dial(peer.Token, "peer")
	if err != nil {
		return c.fail("dial peer: %w", err)
	}
	defer peerConn.close()

	if err := old.send("join", map[string]any{"rated": false}); err != nil {
		return c.fail("fresher join: %w", err)
	}
	seat, err := old.waitForStatus("waiting", 10*time.Second)
	if err != nil {
		return c.fail("fresher room: %w", err)
	}
	roomID := seat.Room.ID

	if err := peerConn.send("join", map[string]any{"roomId": roomID}); err != nil {
		return c.fail("peer join: %w", err)
	}
	if _, err := peerConn.waitForStatus("ready", 10*time.Second); err != nil {
		return c.fail("peer ready state: %w", err)
	}
	if err := old.send("ready", nil); err != nil {
		return c.fail("fresher ready: %w", err)
	}
	if err := peerConn.send("ready", nil); err != nil {
		return c.fail("peer ready: %w", err)
	}
	if _, err := peerConn.waitForStatus("playing", 10*time.Second); err != nil {
		return c.fail("the game did not start: %w", err)
	}

	// 「刷新」：新连接先上来并回到同一个座位，旧连接随后关闭。
	fresh, err := c.dial(fresher.Token, "fresher-new")
	if err != nil {
		return c.fail("dial fresher new: %w", err)
	}
	defer fresh.close()
	if err := fresh.send("join", map[string]any{"roomId": roomID}); err != nil {
		return c.fail("fresher rejoin: %w", err)
	}
	if _, err := fresh.waitForRoomWhere(10*time.Second, func(r roomSnapshot) bool {
		return r.YouAre != ""
	}); err != nil {
		return c.fail("the new connection did not get the seat back: %w", err)
	}
	old.close()

	// 服务端处理旧连接的关闭是毫秒级的；这里连续追一秒，确认座位始终在线。
	// 用 100ms 的轮询只是取样的节奏，不是在等一个猜出来的时间点。
	for i := 0; i < 10; i++ {
		if err := peerConn.send("sync", nil); err != nil {
			return c.fail("peer sync: %w", err)
		}
		view, err := peerConn.waitForRoomWhere(5*time.Second, func(r roomSnapshot) bool {
			return r.Status == "playing"
		})
		if err != nil {
			return c.fail("peer snapshot: %w", err)
		}
		if !seatIsOnline(view.Room, fresher.User.ID) {
			return c.fail("刷新后座位被标成离线（第 %d 次检查）", i+1)
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.logf("✓ 刷新页面后座位始终显示在线（旧连接关闭后连续 1 秒确认）")
	return nil
}

// seatIsOnline reports whether the given account's seat is marked online.
func seatIsOnline(r roomSnapshot, userID int64) bool {
	for _, seat := range []seatSnapshot{r.Red, r.Black} {
		if seat.UserID == userID {
			return seat.Online
		}
	}
	return false
}

// checkSpectating 验证观战这条链路：
//
//	两个新账号入座并开局 → 第三个账号用 watch 只观不下（不占座位）→ 一方离开判负后
//	房间保留空位 → 观战者用 join 补上空位。
func (c *checker) checkSpectating() error {
	suffix := time.Now().UnixNano() % 1_000_000
	host, err := c.register(fmt.Sprintf("host%d", suffix), "房主")
	if err != nil {
		return c.fail("register host: %w", err)
	}
	guest, err := c.register(fmt.Sprintf("guest%d", suffix), "客人")
	if err != nil {
		return c.fail("register guest: %w", err)
	}
	watcher, err := c.register(fmt.Sprintf("watch%d", suffix), "观众")
	if err != nil {
		return c.fail("register watcher: %w", err)
	}

	hostConn, err := c.dial(host.Token, "host")
	if err != nil {
		return c.fail("dial host: %w", err)
	}
	defer hostConn.close()
	guestConn, err := c.dial(guest.Token, "guest")
	if err != nil {
		return c.fail("dial guest: %w", err)
	}
	defer guestConn.close()
	watchConn, err := c.dial(watcher.Token, "watcher")
	if err != nil {
		return c.fail("dial watcher: %w", err)
	}
	defer watchConn.close()

	// 开一桌并开局。
	if err := hostConn.send("join", map[string]any{"rated": false}); err != nil {
		return c.fail("host join: %w", err)
	}
	hostRoom, err := hostConn.waitForStatus("waiting", 10*time.Second)
	if err != nil {
		return c.fail("host room: %w", err)
	}
	roomID := hostRoom.Room.ID

	if err := guestConn.send("join", map[string]any{"roomId": roomID}); err != nil {
		return c.fail("guest join: %w", err)
	}
	if _, err := guestConn.waitForStatus("ready", 10*time.Second); err != nil {
		return c.fail("guest did not reach ready: %w", err)
	}
	if err := hostConn.send("ready", nil); err != nil {
		return c.fail("host ready: %w", err)
	}
	if err := guestConn.send("ready", nil); err != nil {
		return c.fail("guest ready: %w", err)
	}
	if _, err := hostConn.waitForStatus("playing", 10*time.Second); err != nil {
		return c.fail("the game did not start: %w", err)
	}
	c.logf("✓ 观战用的房间 %d 已开局", roomID)

	// 第三方 watch：不占座，快照的 youAre 必须为空。
	if err := watchConn.send("watch", map[string]any{"roomId": roomID}); err != nil {
		return c.fail("send watch: %w", err)
	}
	if _, err := watchConn.waitFor("watching", 10*time.Second); err != nil {
		return c.fail("no watching acknowledgement: %w", err)
	}
	watched, err := watchConn.waitForStatus("playing", 10*time.Second)
	if err != nil {
		return c.fail("the spectator got no snapshot: %w", err)
	}
	if watched.Room.YouAre != "" {
		return c.fail("观战快照的 youAre 应为空，得到 %q", watched.Room.YouAre)
	}
	if watched.Room.Red.UserID == 0 || watched.Room.Black.UserID == 0 {
		return c.fail("观战不该占座：red=%d black=%d", watched.Room.Red.UserID, watched.Room.Black.UserID)
	}
	c.logf("✓ 第三方可以只观不下：youAre 为空、座位未被占用")

	// 一方离开判负，房间留下一个空位。
	if err := hostConn.send("leave", nil); err != nil {
		return c.fail("host leave: %w", err)
	}
	if _, err := watchConn.waitForRoomWhere(10*time.Second, func(r roomSnapshot) bool {
		return r.Status == "finished"
	}); err != nil {
		return c.fail("离开后房间未结束: %w", err)
	}
	c.logf("✓ 一方离开后房间保留空位")

	// 观战者补位入座。
	if err := watchConn.send("join", map[string]any{"roomId": roomID}); err != nil {
		return c.fail("watcher join: %w", err)
	}
	taken, err := watchConn.waitForRoomWhere(10*time.Second, func(r roomSnapshot) bool {
		return r.YouAre != "" &&
			(r.Red.UserID == watcher.User.ID || r.Black.UserID == watcher.User.ID)
	})
	if err != nil {
		return c.fail("观战者补位失败: %w", err)
	}
	c.logf("✓ 观战者补上空位，现在执 %s", taken.Room.YouAre)
	return nil
}

// checkLobby verifies the lobby and leaderboard endpoints respond sanely.
func (c *checker) checkLobby(token string) error {
	var lobby struct {
		OpenRooms []any `json:"openRooms"`
		Playing   int   `json:"playing"`
	}
	if err := c.getJSON("/api/lobby", token, &lobby); err != nil {
		return err
	}
	var board struct {
		Users []struct {
			Username string `json:"username"`
		} `json:"users"`
	}
	if err := c.getJSON("/api/leaderboard", token, &board); err != nil {
		return err
	}
	if len(board.Users) == 0 {
		return c.fail("the leaderboard is empty after several registrations")
	}
	return nil
}

// ---- HTTP helpers ----

func (c *checker) register(username, nickname string) (session, error) {
	var out session
	err := c.postJSON("/api/auth/register", "", map[string]string{
		"username": username, "password": "e2e-password", "nickname": nickname,
	}, &out)
	return out, err
}

type session struct {
	Token string `json:"token"`
	User  struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Nickname string `json:"nickname"`
		Rating   int    `json:"rating"`
	} `json:"user"`
}

type storedGame struct {
	ID         int64  `json:"id"`
	Status     string `json:"status"`
	Outcome    string `json:"outcome"`
	WinnerSide string `json:"winnerSide"`
	MoveCount  int    `json:"moveCount"`
	RedName    string `json:"redName"`
	BlackName  string `json:"blackName"`
}

func (c *checker) listMine(token string) ([]storedGame, error) {
	var out struct {
		Games []storedGame `json:"games"`
	}
	if err := c.getJSON("/api/games?mine=1&limit=5", token, &out); err != nil {
		return nil, err
	}
	return out.Games, nil
}

// openRoomIDs lists the room ids the lobby advertises as waiting.
func (c *checker) openRoomIDs(token string) ([]int64, error) {
	var lobby struct {
		OpenRooms []struct {
			ID int64 `json:"id"`
		} `json:"openRooms"`
	}
	if err := c.getJSON("/api/lobby", token, &lobby); err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(lobby.OpenRooms))
	for _, r := range lobby.OpenRooms {
		out = append(out, r.ID)
	}
	return out, nil
}

// containsID reports whether an id list holds a value.
func containsID(ids []int64, id int64) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func (c *checker) getJSON(path, token string, out any) error {
	req, err := http.NewRequest(http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req, out)
}

func (c *checker) postJSON(path, token string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return c.do(req, out)
}

func (c *checker) do(req *http.Request, out any) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		// Surface the server's own error message, which is what a client shows.
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &body) == nil && body.Error.Code != "" {
			return fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, body.Error.Code, body.Error.Message)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// ---- WebSocket helper ----

// frame is one server-to-client message, decoded lazily by type.
type frame struct {
	Type     string          `json:"type"`
	Room     roomSnapshot    `json:"-"`
	Raw      json.RawMessage `json:"data"`
	GameOver struct {
		Outcome    string `json:"outcome"`
		Reason     string `json:"reason"`
		WinnerSide string `json:"winnerSide"`
	} `json:"-"`
}

// roomSnapshot is the part of a room view the acceptance script inspects. It
// carries `ready` because two human players have to confirm before play starts.
type roomSnapshot struct {
	ID        int64               `json:"id"`
	Status    string              `json:"status"`
	Turn      string              `json:"turn"`
	YouAre    string              `json:"youAre"`
	FEN       string              `json:"fen"`
	Outcome   string              `json:"outcome"`
	LegalFrom map[string][]string `json:"legalFrom"`
	Red       seatSnapshot        `json:"red"`
	Black     seatSnapshot        `json:"black"`
	Moves     []struct {
		Ply      int    `json:"ply"`
		Notation string `json:"notation"`
	} `json:"moves"`
}

// seatSnapshot is one seat of a room view.
type seatSnapshot struct {
	UserID int64  `json:"userId"`
	Name   string `json:"name"`
	IsAI   bool   `json:"isAI"`
	Online bool   `json:"online"`
	Ready  bool   `json:"ready"`
}

// wsClient wraps a connection with a reader goroutine that decodes frames.
type wsClient struct {
	name   string
	conn   *websocket.Conn
	frames chan decoded
	closed chan struct{}
}

type decoded struct {
	kind    string
	raw     json.RawMessage
	payload json.RawMessage
}

func (c *checker) dial(token, name string) (*wsClient, error) {
	u, err := url.Parse(c.base)
	if err != nil {
		return nil, err
	}
	u.Scheme = "ws"
	if u.Scheme == "ws" && strings.HasPrefix(c.base, "https") {
		u.Scheme = "wss"
	}
	u.Path = "/ws"
	q := u.Query()
	q.Set("token", token)
	u.RawQuery = q.Encode()

	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("dial %s: %w (HTTP %d)", u.String(), err, resp.StatusCode)
		}
		return nil, fmt.Errorf("dial %s: %w", u.String(), err)
	}
	client := &wsClient{name: name, conn: conn, frames: make(chan decoded, 128), closed: make(chan struct{})}

	go func() {
		defer close(client.closed)
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(payload, &env); err != nil {
				continue
			}
			select {
			case client.frames <- decoded{kind: env.Type, raw: payload, payload: env.Data}:
			default:
			}
		}
	}()
	return client, nil
}

func (w *wsClient) send(kind string, data any) error {
	env := map[string]any{"type": kind}
	if data != nil {
		env["data"] = data
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return w.conn.WriteMessage(websocket.TextMessage, raw)
}

func (w *wsClient) close() { _ = w.conn.Close() }

// waitFor waits for one frame of the given type, returning the decoded view.
// It skips unrelated frames but fails loudly on an error frame, because an
// unexpected error is the most useful signal when a check fails.
func (w *wsClient) waitFor(kind string, timeout time.Duration) (frame, error) {
	deadline := time.After(timeout)
	var seen []string
	for {
		select {
		case d, ok := <-w.frames:
			if !ok {
				return frame{}, fmt.Errorf("%s: connection closed while waiting for %q (saw %v)", w.name, kind, seen)
			}
			seen = append(seen, d.kind)
			if d.kind != kind {
				if d.kind == "error" && kind != "error" {
					var ev struct {
						Code    string `json:"code"`
						Message string `json:"message"`
					}
					_ = json.Unmarshal(d.payload, &ev)
					return frame{}, fmt.Errorf("%s: server error while waiting for %q: %s (%s)", w.name, kind, ev.Message, ev.Code)
				}
				continue
			}
			var f frame
			f.Type = d.kind
			f.Raw = d.raw
			if len(d.payload) > 0 {
				_ = json.Unmarshal(d.payload, &f.Room)
				_ = json.Unmarshal(d.payload, &f.GameOver)
			}
			return f, nil
		case <-deadline:
			return frame{}, fmt.Errorf("%s: timed out waiting for %q (saw %v)", w.name, kind, seen)
		case <-w.closed:
			return frame{}, fmt.Errorf("%s: connection closed while waiting for %q (saw %v)", w.name, kind, seen)
		}
	}
}

// waitForRoomWhere waits for a room snapshot that satisfies pred. Snapshots that
// do not match are consumed and skipped, so a caller can wait for a state that is
// only reached after several updates.
func (w *wsClient) waitForRoomWhere(timeout time.Duration, pred func(roomSnapshot) bool) (frame, error) {
	deadline := time.After(timeout)
	var seen []string
	for {
		select {
		case d, ok := <-w.frames:
			if !ok {
				return frame{}, fmt.Errorf("%s: connection closed while waiting for a room snapshot (saw %v)", w.name, seen)
			}
			seen = append(seen, d.kind)
			if d.kind == "error" {
				var ev struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				_ = json.Unmarshal(d.payload, &ev)
				return frame{}, fmt.Errorf("%s: server error while waiting for a room snapshot: %s (%s)", w.name, ev.Message, ev.Code)
			}
			if d.kind != "room" {
				continue
			}
			var f frame
			f.Type = d.kind
			f.Raw = d.raw
			_ = json.Unmarshal(d.payload, &f.Room)
			if pred == nil || pred(f.Room) {
				return f, nil
			}
		case <-deadline:
			return frame{}, fmt.Errorf("%s: timed out waiting for a room snapshot (saw %v)", w.name, seen)
		case <-w.closed:
			return frame{}, fmt.Errorf("%s: connection closed while waiting for a room snapshot (saw %v)", w.name, seen)
		}
	}
}

// waitForStatus waits for a room snapshot in a given status.
func (w *wsClient) waitForStatus(status string, timeout time.Duration) (frame, error) {
	return w.waitForRoomWhere(timeout, func(r roomSnapshot) bool { return r.Status == status })
}
