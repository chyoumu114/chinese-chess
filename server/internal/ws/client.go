package ws

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"xiangqi/internal/ai"
	"xiangqi/internal/game"
	"xiangqi/internal/gamehall"
)

// Client is one WebSocket connection.
type Client struct {
	hub      *Hub
	conn     *websocket.Conn
	identity Identity

	// send is the outbound queue. Only the write pump drains it.
	send chan Envelope

	// rooms is the set of rooms this connection is watching. It is only touched
	// while holding the hub mutex.
	rooms map[int64]struct{}

	closed    chan struct{}
	closeOnce sync.Once
}

// enqueue queues an envelope, dropping the connection when the client is too
// far behind to keep the game consistent.
func (c *Client) enqueue(env Envelope) {
	select {
	case c.send <- env:
	case <-c.closed:
	default:
		// The peer is not draining its socket. Closing is the only safe option:
		// silently dropping game messages would leave the client with a stale
		// board.
		c.hub.logger.Warn("websocket send buffer full, closing", "user", c.identity.UserID)
		c.close()
	}
}

// enqueueRoomSnapshot sends the current room snapshot with the receiver's own
// side filled in.
func (c *Client) enqueueRoomSnapshot(roomID int64) {
	view, ok := c.hub.session.View(roomID)
	if !ok {
		return
	}
	if side, found := c.hub.session.SideOfUserIn(roomID, c.identity.UserID); found {
		view.YouAre = gamehall.SideName(side)
	}
	c.enqueue(NewEnvelope(TypeRoom, view))
}

// close shuts the connection down exactly once.
func (c *Client) close() {
	c.closeOnce.Do(func() { close(c.closed) })
}

// writePump serialises every write onto the socket and keeps it alive with
// pings.
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case <-c.closed:
			// Tell the peer why, best effort, then go.
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			_ = c.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			return
		case env := <-c.send:
			payload, err := json.Marshal(env)
			if err != nil {
				c.hub.logger.Error("websocket marshal failed", "type", env.Type, "error", err)
				continue
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// readPump reads commands until the peer goes away.
func (c *Client) readPump() {
	defer func() {
		c.hub.detachClient(c)
		c.close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		return c.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	// Greet the client before anything else so it knows who it is.
	c.enqueue(NewEnvelope(TypeWelcome, WelcomeView{
		UserID:   c.identity.UserID,
		Name:     c.identity.Name,
		Guest:    c.identity.Guest,
		ServerAt: time.Now().UnixMilli(),
	}))
	c.hub.sendPresence()

	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.hub.logger.Debug("websocket closed unexpectedly", "error", err)
			}
			return
		}
		var env Envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			c.enqueue(NewEnvelope(TypeError, ErrorView{Code: "bad_frame", Message: "消息格式不正确"}))
			continue
		}
		c.handle(env)
	}
}

// handle dispatches one inbound message.
func (c *Client) handle(env Envelope) {
	ctx := context.Background()

	switch env.Type {
	case TypePing:
		c.enqueue(NewEnvelope(TypePong, nil))

	case TypeSync:
		if roomID, ok := c.hub.session.RoomIDForUser(c.identity.UserID); ok && c.identity.UserID != 0 {
			c.hub.attachRoom(c, roomID)
			c.enqueueRoomSnapshot(roomID)
			return
		}
		c.hub.sendPresence()

	case TypeJoin:
		var req JoinRequest
		if err := env.Decode(&req); err != nil {
			c.enqueue(NewEnvelope(TypeError, ErrorView{Code: "bad_payload", Message: "加入参数不正确"}))
			return
		}
		c.handleJoin(ctx, req)

	case TypeReady:
		c.withRoom(func(roomID int64) {
			out, err := c.hub.session.Ready(ctx, roomID, c.identity.UserID)
			if err != nil {
				c.enqueue(NewEnvelope(TypeError, describeError(err)))
				return
			}
			if out.Started {
				// A game that starts now needs its own row in the games table,
				// and the computer may be the first to move.
				c.hub.persistIfPlaying(ctx, roomID)
				c.hub.requestAIMoveIfNeeded(ctx, roomID)
			}
		})

	case TypeLeave:
		c.handleLeave(ctx)

	case TypeMove:
		c.withRoom(func(roomID int64) {
			var req MoveRequest
			if err := env.Decode(&req); err != nil {
				c.enqueue(NewEnvelope(TypeError, ErrorView{Code: "bad_payload", Message: "走法格式不正确"}))
				return
			}
			c.handleMove(ctx, roomID, req)
		})

	case TypeResign:
		c.withRoom(func(roomID int64) {
			res, err := c.hub.session.Resign(ctx, roomID, c.identity.UserID)
			if err != nil {
				c.enqueue(NewEnvelope(TypeError, describeError(err)))
				return
			}
			c.hub.announceResult(roomID, res)
		})

	case TypeOfferDraw:
		c.withRoom(func(roomID int64) {
			side, err := c.hub.session.OfferDraw(roomID, c.identity.UserID)
			if err != nil {
				c.enqueue(NewEnvelope(TypeError, describeError(err)))
				return
			}
			c.hub.sendToRoom(roomID, NewEnvelope(TypeDrawOffered, map[string]any{
				"roomId": roomID, "by": gamehall.SideName(side), "name": c.identity.Name,
			}))
		})

	case TypeRespondDraw:
		c.withRoom(func(roomID int64) {
			var req RespondRequest
			if err := env.Decode(&req); err != nil {
				c.enqueue(NewEnvelope(TypeError, ErrorView{Code: "bad_payload", Message: "参数不正确"}))
				return
			}
			res, err := c.hub.session.RespondDraw(ctx, roomID, c.identity.UserID, req.Accept)
			if err != nil {
				c.enqueue(NewEnvelope(TypeError, describeError(err)))
				return
			}
			if !req.Accept {
				c.hub.sendToRoom(roomID, NewEnvelope(TypeDrawDeclined, map[string]any{
					"roomId": roomID, "by": c.identity.Name,
				}))
				return
			}
			c.hub.announceResult(roomID, res)
		})

	case TypeRequestTakeback:
		c.withRoom(func(roomID int64) {
			side, err := c.hub.session.RequestTakeback(roomID, c.identity.UserID)
			if err != nil {
				c.enqueue(NewEnvelope(TypeError, describeError(err)))
				return
			}
			c.hub.sendToRoom(roomID, NewEnvelope(TypeTakebackRequested, map[string]any{
				"roomId": roomID, "by": gamehall.SideName(side), "name": c.identity.Name,
			}))
		})

	case TypeRespondTakeback:
		c.withRoom(func(roomID int64) {
			var req RespondRequest
			if err := env.Decode(&req); err != nil {
				c.enqueue(NewEnvelope(TypeError, ErrorView{Code: "bad_payload", Message: "参数不正确"}))
				return
			}
			if _, err := c.hub.session.RespondTakeback(roomID, c.identity.UserID, req.Accept); err != nil {
				c.enqueue(NewEnvelope(TypeError, describeError(err)))
				return
			}
			if req.Accept {
				c.hub.sendToRoom(roomID, NewEnvelope(TypeTakebackApplied, map[string]any{"roomId": roomID}))
			} else {
				c.hub.sendToRoom(roomID, NewEnvelope(TypeTakebackDeclined, map[string]any{
					"roomId": roomID, "by": c.identity.Name,
				}))
			}
		})

	case TypeChat:
		c.withRoom(func(roomID int64) {
			var req ChatRequest
			if err := env.Decode(&req); err != nil {
				c.enqueue(NewEnvelope(TypeError, ErrorView{Code: "bad_payload", Message: "参数不正确"}))
				return
			}
			text := strings.TrimSpace(req.Text)
			if text == "" {
				return
			}
			if utf8.RuneCountInString(text) > 200 {
				text = string([]rune(text)[:200])
			}
			side := ""
			if s, ok := c.hub.session.SideOfUserIn(roomID, c.identity.UserID); ok {
				side = gamehall.SideName(s)
			}
			c.hub.sendToRoom(roomID, NewEnvelope(TypeChatMessage, ChatView{
				UserID: c.identity.UserID,
				Name:   c.identity.Name,
				Side:   side,
				Text:   text,
				At:     time.Now().UnixMilli(),
			}))
		})

	default:
		c.enqueue(NewEnvelope(TypeError, ErrorView{
			Code: "unknown_type", Message: "不认识的消息类型: " + env.Type,
		}))
	}
}

// withRoom looks up the caller's room and runs fn with it, reporting a wire
// error when the caller is not seated anywhere.
func (c *Client) withRoom(fn func(roomID int64)) {
	if c.identity.UserID == 0 {
		c.enqueue(NewEnvelope(TypeError, ErrorView{
			Code: "login_required", Message: "请先登录再开始对局",
		}))
		return
	}
	roomID, ok := c.hub.session.RoomIDForUser(c.identity.UserID)
	if !ok {
		c.enqueue(NewEnvelope(TypeError, ErrorView{
			Code: "no_room", Message: "你当前不在任何对局中",
		}))
		return
	}
	c.hub.attachRoom(c, roomID)
	fn(roomID)
}

// aiLevelOf maps the wire AI level onto a search level. Zero means a human
// opponent.
func aiLevelOf(v int) ai.Level {
	if v <= 0 {
		return 0
	}
	return ai.ParseLevel(v)
}

// handleJoin seats the caller.
func (c *Client) handleJoin(ctx context.Context, req JoinRequest) {
	if c.identity.UserID == 0 {
		// A signed-out visitor may still watch, but not play, because every
		// result is persisted against an account.
		c.enqueue(NewEnvelope(TypeError, ErrorView{
			Code: "login_required", Message: "请先登录再开始对局",
		}))
		return
	}

	level := aiLevelOf(req.AILevel)
	roomID, side, err := c.hub.session.Join(ctx, gamehall.JoinRequest{
		UserID:  c.identity.UserID,
		Name:    c.identity.Name,
		RoomID:  req.RoomID,
		AILevel: level,
		Side:    req.Side,
		Rated:   req.Rated,
	})
	if err != nil {
		c.enqueue(NewEnvelope(TypeError, describeError(err)))
		return
	}

	// Persist the game row once the room is actually in play. A room that is
	// still waiting for an opponent, or for 准备, must not leave a half-written
	// game behind when its match is cancelled.
	c.hub.persistIfPlaying(ctx, roomID)

	c.hub.attachRoom(c, roomID)
	view, ok := c.hub.session.View(roomID)
	if !ok {
		c.enqueue(NewEnvelope(TypeError, describeError(ErrNoSuchRoom)))
		return
	}
	view.YouAre = gamehall.SideName(side)
	c.enqueue(NewEnvelope(TypeRoom, view))
	c.hub.sendToRoom(roomID, NewEnvelope(TypeRoom, view))
	c.hub.sendPresence()

	// The computer may already be to move, for example when the human chose to
	// play Black.
	c.hub.requestAIMoveIfNeeded(ctx, roomID)
}

// handleLeave frees the caller's seat. The opponent, if there is one, is told
// why the game stopped; a room nobody is left in is dissolved, so cancelling a
// quick match does not leave an empty room in the lobby.
func (c *Client) handleLeave(ctx context.Context) {
	if c.identity.UserID == 0 {
		c.enqueue(NewEnvelope(TypeError, ErrorView{
			Code: "login_required", Message: "请先登录再开始对局",
		}))
		return
	}

	roomID, ok := c.hub.session.RoomIDForUser(c.identity.UserID)
	if !ok {
		// Nothing to give up, but the client still needs an answer so it can
		// move on, and the roster may have changed while it was watching.
		c.hub.detachAll(c)
		c.enqueue(NewEnvelope(TypeLeft, LeftView{}))
		c.hub.sendPresence()
		return
	}

	// Detach before leaving: the snapshot the departure publishes must not reach
	// the client that has just left the room.
	c.hub.detachRoom(c, roomID)
	out := c.hub.session.Leave(ctx, roomID, c.identity.UserID)
	c.enqueue(NewEnvelope(TypeLeft, LeftView{RoomID: roomID, Dissolved: out.Dissolved}))

	if out.Result != nil {
		// Somebody is still in the room: tell them the game is over and why.
		c.hub.announceResult(roomID, out.Result)
		c.hub.sendToRoom(roomID, NewEnvelope(TypeOpponentLeft, map[string]any{
			"roomId": roomID, "name": c.identity.Name, "left": true,
		}))
	}
	c.hub.sendPresence()
}

// persistIfPlaying creates the games row for a room that is in play.
func (h *Hub) persistIfPlaying(ctx context.Context, roomID int64) {
	view, ok := h.session.View(roomID)
	if !ok || view.Status != gamehall.StatusPlaying {
		return
	}
	h.ensurePersisted(ctx, roomID)
}

// handleMove applies a move and fans the result out.
func (c *Client) handleMove(ctx context.Context, roomID int64, req MoveRequest) {
	from := strings.ToLower(strings.TrimSpace(req.From))
	to := strings.ToLower(strings.TrimSpace(req.To))
	if _, err := game.ParseMove(from + to); err != nil {
		c.enqueue(NewEnvelope(TypeError, ErrorView{Code: "bad_move", Message: "走法格式不正确"}))
		return
	}

	out, err := c.hub.session.SubmitMove(ctx, roomID, c.identity.UserID, from, to)
	if err != nil {
		c.enqueue(NewEnvelope(TypeError, describeError(err)))
		return
	}
	c.hub.sendToRoom(roomID, NewEnvelope(TypeMoveMade, map[string]any{
		"roomId":   roomID,
		"from":     from,
		"to":       to,
		"notation": out.Record.Notation,
		"side":     out.Record.Side,
		"check":    out.Record.Check,
		"byAI":     false,
	}))
	if out.GameOver {
		c.hub.announceResult(roomID, out.Result)
		return
	}
	c.hub.requestAIMoveIfNeeded(ctx, roomID)
}

// detachClient handles a disconnect: it marks the seat offline and, for a game
// against the computer, ends the game so it is not left dangling.
func (h *Hub) detachClient(c *Client) {
	h.remove(c)
	if c.identity.UserID == 0 {
		h.sendPresence()
		return
	}
	roomID, ok := h.session.RoomIDForUser(c.identity.UserID)
	if !ok {
		h.sendPresence()
		return
	}
	h.session.Disconnect(roomID, c.identity.UserID)
	h.sendToRoom(roomID, NewEnvelope(TypeOpponentLeft, map[string]any{
		"roomId": roomID, "name": c.identity.Name,
	}))
	h.sendPresence()
}

// ensurePersisted inserts the games row for a room when it does not have one.
func (h *Hub) ensurePersisted(ctx context.Context, roomID int64) {
	room, ok := h.session.RoomByID(roomID)
	if !ok || room.PersistedID != 0 {
		return
	}
	if h.persister == nil {
		return
	}
	view := room.Snapshot()
	gameID, err := h.persister.CreateGame(ctx, view)
	if err != nil {
		h.logger.Error("cannot persist game", "room", roomID, "error", err)
		return
	}
	h.session.SetPersistedID(roomID, gameID)
}

// Persister creates the games row backing a room. It is implemented by the
// store package in the application wiring, which keeps this package free of a
// database import.
type Persister interface {
	CreateGame(ctx context.Context, view gamehall.RoomView) (int64, error)
}

// SetPersister installs the game-row creator.
func (h *Hub) SetPersister(p Persister) { h.persister = p }
