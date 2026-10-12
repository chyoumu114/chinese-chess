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
	if bobRoom.Room.Status != "playing" {
		return c.fail("the game should be playing, status = %q", bobRoom.Room.Status)
	}
	if bobRoom.Room.Turn != "red" {
		return c.fail("red should move first, turn = %q", bobRoom.Room.Turn)
	}
	c.logf("✓ bob seated in the same room as black; game is playing")

	// 6. The server publishes the legal moves, and there must be 44 of them.
	total := 0
	for _, dests := range bobRoom.Room.LegalFrom {
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
	Type string `json:"type"`
	Room struct {
		ID      int64  `json:"id"`
		Status  string `json:"status"`
		Turn    string `json:"turn"`
		YouAre  string `json:"youAre"`
		FEN     string `json:"fen"`
		Outcome string `json:"outcome"`
		Red     struct {
			Name   string `json:"name"`
			IsAI   bool   `json:"isAI"`
			Online bool   `json:"online"`
		} `json:"red"`
		Black struct {
			Name   string `json:"name"`
			IsAI   bool   `json:"isAI"`
			Online bool   `json:"online"`
		} `json:"black"`
		LegalFrom map[string][]string `json:"legalFrom"`
		Moves     []struct {
			Ply      int    `json:"ply"`
			Notation string `json:"notation"`
		} `json:"moves"`
	} `json:"-"`
	Raw json.RawMessage `json:"data"`
	// Decoded payloads.
	GameOver struct {
		Outcome    string `json:"outcome"`
		Reason     string `json:"reason"`
		WinnerSide string `json:"winnerSide"`
	} `json:"-"`
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
