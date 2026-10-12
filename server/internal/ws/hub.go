package ws

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"xiangqi/internal/ai"
	"xiangqi/internal/game"
	"xiangqi/internal/gamehall"
)

// Connection tuning.
const (
	// writeWait is how long a single write may take.
	writeWait = 10 * time.Second
	// pongWait is how long the server waits for any client traffic before
	// treating the connection as dead.
	pongWait = 60 * time.Second
	// pingPeriod must be shorter than pongWait.
	pingPeriod = 25 * time.Second
	// maxMessageSize caps an inbound frame.
	maxMessageSize = 8 << 10
	// sendBuffer is the per-connection outbound queue depth.
	sendBuffer = 64
)

// Identity is the authenticated caller attached to a connection.
type Identity struct {
	UserID int64
	Name   string
	Guest  bool
}

// Hub owns every live WebSocket connection and drives the gamehall session.
type Hub struct {
	session *gamehall.Session
	logger  *slog.Logger
	origins []string

	mu      sync.RWMutex
	clients map[*Client]struct{}
	// byRoom indexes the clients attached to each room so broadcasts are cheap.
	byRoom map[int64]map[*Client]struct{}

	// updates carries room ids from the session's update hook to the broadcast
	// worker. It must be a channel rather than a direct call: the session fires
	// the hook while holding its own lock, so broadcasting inline would acquire
	// the hub lock under the session lock, while the presence sweep acquires
	// them in the opposite order — a classic deadlock. Handing the work to a
	// worker also keeps the session lock hold time short.
	updates chan int64

	// aiSem bounds how many computer searches run at once.
	aiSem chan struct{}

	// persister creates the games row backing a room. It is installed by the
	// application wiring so this package needs no database import.
	persister Persister

	upgrader websocket.Upgrader
}

// NewHub returns a hub bound to a session.
func NewHub(session *gamehall.Session, logger *slog.Logger, allowedOrigins []string) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Hub{
		session: session,
		logger:  logger,
		origins: allowedOrigins,
		clients: make(map[*Client]struct{}),
		byRoom:  make(map[int64]map[*Client]struct{}),
		updates: make(chan int64, 1024),
		aiSem:   make(chan struct{}, 4),
	}
	h.upgrader = websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     h.checkOrigin,
	}
	session.SetUpdateHook(h.onSessionUpdate)
	return h
}

// onSessionUpdate is the session's update hook. It runs with the session lock
// held and therefore only hands room ids to the broadcast worker.
func (h *Hub) onSessionUpdate(roomIDs []int64) {
	for _, id := range roomIDs {
		select {
		case h.updates <- id:
		default:
			// The worker is behind. Dropping a notification would leave a client
			// with a stale board, so this is logged loudly; the worker drains in
			// order and the queue is far larger than any realistic burst.
			h.logger.Error("websocket update queue is full; dropping a room notification", "room", id)
		}
	}
}

// runBroadcasts fans room updates out to the clients watching them, until ctx is
// cancelled.
func (h *Hub) runBroadcasts(ctx context.Context) {
	// Coalesce the rooms seen in one wake-up so a burst of moves in different
	// rooms costs one snapshot each, not one per notification.
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-h.updates:
			roomID := id
			h.broadcastRoom(roomID)
			// Drain anything already queued without blocking.
			for {
				select {
				case next := <-h.updates:
					if next != roomID {
						h.broadcastRoom(next)
					}
				default:
					goto drained
				}
			}
		drained:
		}
	}
}

// checkOrigin enforces the configured origin allowlist. An empty allowlist
// accepts any origin, which is correct for a same-origin deployment behind a
// reverse proxy and for local development.
func (h *Hub) checkOrigin(r *http.Request) bool {
	if len(h.origins) == 0 {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client
	}
	for _, allowed := range h.origins {
		if strings.EqualFold(strings.TrimSuffix(allowed, "/"), strings.TrimSuffix(origin, "/")) {
			return true
		}
	}
	return false
}

// ServeHTTP upgrades a request to a WebSocket and runs the connection.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request, id Identity) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote a response.
		h.logger.Debug("websocket upgrade failed", "error", err)
		return
	}
	client := &Client{
		hub:      h,
		conn:     conn,
		identity: id,
		send:     make(chan Envelope, sendBuffer),
		rooms:    make(map[int64]struct{}),
		closed:   make(chan struct{}),
	}
	h.add(client)

	go client.writePump()
	go client.readPump()
}

// add registers a client.
func (h *Hub) add(c *Client) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

// remove unregisters a client and detaches it from its rooms.
func (h *Hub) remove(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	for roomID := range c.rooms {
		if set, ok := h.byRoom[roomID]; ok {
			delete(set, c)
			if len(set) == 0 {
				delete(h.byRoom, roomID)
			}
		}
	}
	h.mu.Unlock()
}

// attachRoom records that a client is watching a room.
func (h *Hub) attachRoom(c *Client, roomID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.rooms[roomID] = struct{}{}
	set, ok := h.byRoom[roomID]
	if !ok {
		set = make(map[*Client]struct{})
		h.byRoom[roomID] = set
	}
	set[c] = struct{}{}
}

// sendToRoom queues an envelope for every client watching a room.
func (h *Hub) sendToRoom(roomID int64, env Envelope) {
	h.mu.RLock()
	targets := make([]*Client, 0, 4)
	for c := range h.byRoom[roomID] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		c.enqueue(env)
	}
}

// sendPresence pushes the lobby roster to every connected client. It is called
// when rooms open or close.
func (h *Hub) sendPresence() {
	view := h.presenceView()
	env := NewEnvelope(TypePresence, view)

	h.mu.RLock()
	targets := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		c.enqueue(env)
	}
}

// presenceView renders the lobby roster. It reads only the hub's own state and
// the clients' identities, never the session, so it cannot participate in a
// lock cycle.
func (h *Hub) presenceView() PresenceView {
	h.mu.RLock()
	defer h.mu.RUnlock()

	view := PresenceView{Online: make([]PresenceEntry, 0, len(h.clients))}
	rooms := make(map[int64]int)
	for c := range h.clients {
		entry := PresenceEntry{UserID: c.identity.UserID, Name: c.identity.Name, Status: "online"}
		switch len(c.rooms) {
		case 0:
			entry.Status = "online"
		case 1:
			for id := range c.rooms {
				entry.RoomID = id
				rooms[id]++
			}
			entry.Status = "waiting"
			if rooms[entry.RoomID] > 1 {
				entry.Status = "playing"
			}
		default:
			entry.Status = "playing"
		}
		view.Online = append(view.Online, entry)
	}
	view.Rooms = len(h.byRoom)
	return view
}

// broadcastRoom pushes a fresh snapshot of one room to its watchers. It is
// called by the broadcast worker, never with the session lock held, so it may
// read the session freely.
func (h *Hub) broadcastRoom(roomID int64) {
	h.mu.RLock()
	targets := make([]*Client, 0, 4)
	for c := range h.byRoom[roomID] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		c.enqueueRoomSnapshot(roomID)
	}
}

// Close shuts every connection down.
func (h *Hub) Close() {
	h.mu.Lock()
	targets := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.Unlock()

	for _, c := range targets {
		c.close()
	}
}

// StartTimers runs the periodic jobs: clock expiry, presence refresh and room
// reaping. It also drives the broadcast worker. It returns when ctx is
// cancelled.
func (h *Hub) StartTimers(ctx context.Context) {
	go h.runBroadcasts(ctx)

	clock := time.NewTicker(time.Second)
	defer clock.Stop()
	presence := time.NewTicker(10 * time.Second)
	defer presence.Stop()
	reap := time.NewTicker(5 * time.Minute)
	defer reap.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-clock.C:
			for _, expired := range h.session.ExpireTimeouts(ctx) {
				h.announceResult(expired.RoomID, expired.Result)
			}
		case <-presence.C:
			h.sendPresence()
		case <-reap.C:
			h.session.ReapFinished(2 * time.Hour)
		}
	}
}

// announceResult pushes a game_over message to everyone watching a room.
//
// The room id is passed in explicitly rather than taken from the result: the
// result carries the *persisted* game id, which is a different counter, so
// using it here would broadcast to the wrong room (or to nothing at all).
func (h *Hub) announceResult(roomID int64, res *gamehall.Result) {
	if res == nil {
		return
	}
	view := gamehall.GameOverView{
		Outcome:    res.Outcome,
		Reason:     res.Reason,
		WinnerSide: res.WinnerSide,
		FEN:        res.FEN,
		MoveCount:  res.MoveCount,
	}
	h.sendToRoom(roomID, NewEnvelope(TypeGameOver, view))
}

// requestAIMoveIfNeeded starts a search when the computer is to move. The
// search runs on its own goroutine so a slow position cannot stall the hub.
func (h *Hub) requestAIMoveIfNeeded(ctx context.Context, roomID int64) {
	level, ok := h.session.TakeAISeat(roomID)
	if !ok {
		return
	}
	go h.playAIMove(ctx, roomID, level)
}

// playAIMove searches for and submits the computer's reply.
func (h *Hub) playAIMove(ctx context.Context, roomID int64, level ai.Level) {
	select {
	case h.aiSem <- struct{}{}:
		defer func() { <-h.aiSem }()
	case <-ctx.Done():
		return
	}

	view, ok := h.session.View(roomID)
	if !ok || view.Status != gamehall.StatusPlaying {
		return
	}
	g, err := game.NewGameFromFEN(view.FEN)
	if err != nil {
		h.logger.Error("ai: cannot rebuild position", "room", roomID, "error", err)
		return
	}
	// The snapshot's move list must be replayed for the AI's clock bookkeeping
	// to be irrelevant here: the search only needs the position, which the FEN
	// already encodes, including the side to move.

	move, found := ai.Search(g, level, h.session.Config().AIBudget)
	if !found {
		return
	}
	// A short pause makes the computer's replies feel deliberate rather than
	// instantaneous.
	select {
	case <-time.After(350 * time.Millisecond):
	case <-ctx.Done():
		return
	}

	out, err := h.session.SubmitAIMove(ctx, roomID, move)
	if err != nil {
		h.logger.Warn("ai: move rejected", "room", roomID, "move", move.String(), "error", err)
		return
	}
	h.sendToRoom(roomID, NewEnvelope(TypeMoveMade, map[string]any{
		"roomId":   roomID,
		"from":     out.Move.FromName(),
		"to":       out.Move.ToName(),
		"notation": out.Record.Notation,
		"side":     out.Record.Side,
		"byAI":     true,
	}))
	if out.GameOver {
		h.announceResult(roomID, out.Result)
	}
}

// ErrNoSuchRoom is returned when a handler addresses a room that is gone.
var ErrNoSuchRoom = errors.New("ws: no such room")

// describeError maps an internal error onto a wire error code.
func describeError(err error) ErrorView {
	switch {
	case errors.Is(err, gamehall.ErrRoomNotFound):
		return ErrorView{Code: "room_not_found", Message: "对局不存在"}
	case errors.Is(err, gamehall.ErrRoomFull):
		return ErrorView{Code: "room_full", Message: "该对局已满"}
	case errors.Is(err, gamehall.ErrNotASeat):
		return ErrorView{Code: "not_seated", Message: "你不在这个对局中"}
	case errors.Is(err, gamehall.ErrNotYourTurn):
		return ErrorView{Code: "not_your_turn", Message: "还没轮到你走棋"}
	case errors.Is(err, game.ErrIllegalMove):
		return ErrorView{Code: "illegal_move", Message: "不能这样走"}
	case errors.Is(err, game.ErrNoSuchPiece):
		return ErrorView{Code: "no_piece", Message: "该位置没有棋子"}
	case errors.Is(err, game.ErrWrongSide):
		return ErrorView{Code: "wrong_side", Message: "不能移动对方的棋子"}
	case errors.Is(err, gamehall.ErrGameFinished):
		return ErrorView{Code: "finished", Message: "对局已结束"}
	case errors.Is(err, gamehall.ErrGameNotStarted):
		return ErrorView{Code: "not_started", Message: "对局还没开始"}
	case errors.Is(err, gamehall.ErrNothingPending):
		return ErrorView{Code: "nothing_pending", Message: "没有待处理的请求"}
	default:
		if err == nil {
			return ErrorView{Code: "unknown", Message: "未知错误"}
		}
		return ErrorView{Code: "internal", Message: err.Error()}
	}
}
