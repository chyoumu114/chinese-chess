package ws

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
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

	// pending records when a signed-in player's last connection went away, so the
	// periodic sweep can forfeit a running game if they never come back.
	pending map[int64]time.Time
	// grace is how long a player may stay offline mid-game before losing. It is
	// a field rather than a constant so tests can shorten it.
	grace time.Duration

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
		pending: make(map[int64]time.Time),
		grace:   defaultDisconnectGrace,
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
//
// A wake-up first drains the whole burst of queued notifications and only then
// takes one snapshot per room. The order matters: a notification is queued by the
// session *after* the change it announces is already applied, so a snapshot taken
// after the drain covers every change in the burst. Broadcasting eagerly and
// skipping the rest of the burst would drop the last change of a room — for
// example the "both players are ready, the game started" update — and leave both
// clients looking at the previous state until something else happens in the room.
func (h *Hub) runBroadcasts(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-h.updates:
			rooms := map[int64]struct{}{id: {}}
			// Drain anything already queued without blocking, so the snapshots
			// below are taken once per room instead of once per change.
			for {
				select {
				case next := <-h.updates:
					rooms[next] = struct{}{}
				default:
					goto broadcast
				}
			}
		broadcast:
			for roomID := range rooms {
				h.broadcastRoom(roomID)
			}
		}
	}
}

// checkOrigin enforces the WebSocket origin policy.
//
// An empty allowlist no longer means "anyone": it falls back to same-origin,
// which is what a deployment behind our own reverse proxy needs. Development
// additionally accepts a localhost dev server, because `npm run dev` serves the
// SPA from another port. Accepting arbitrary origins would let a hostile page
// open a socket carrying the victim's session.
func (h *Hub) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// Non-browser clients (the end-to-end suite, tools) send no Origin.
		return true
	}
	for _, allowed := range h.origins {
		if strings.EqualFold(strings.TrimSuffix(allowed, "/"), strings.TrimSuffix(origin, "/")) {
			return true
		}
	}
	if len(h.origins) > 0 {
		return false
	}
	return sameOrigin(origin, r) || isLocalDevOrigin(origin)
}

// sameOrigin reports whether origin matches this request's own scheme and host.
func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = strings.ToLower(proto)
	}
	return strings.EqualFold(u.Scheme, scheme) && strings.EqualFold(u.Host, r.Host)
}

// isLocalDevOrigin reports whether origin is a localhost dev server.
//
// Duplicated in the api package on purpose: ws must not import api, and a shared
// helper for ten lines would be a worse dependency than the copy.
func isLocalDevOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" {
		return false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return true
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
	// 新连接必须立刻广播一次在线名单：否则刷新页面时新连接没人知道，自己会被
	// 上一份快照记成「不在线」，要等下一个人加入或离开才纠正过来。
	h.sendPresence()

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
		h.detachRoomLocked(c, roomID)
	}
	// Start the abandonment clock when a signed-in player goes fully offline.
	// Closing the tab mid-game must not be a free escape from a loss.
	if userID := c.identity.UserID; userID != 0 && !h.onlineLocked(userID) {
		h.pending[userID] = time.Now()
	}
	h.mu.Unlock()
}

// hasClientFor reports whether an account still has a live connection. It is
// called after the client in question has already been removed, so it only sees
// the others.
func (h *Hub) hasClientFor(userID int64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.onlineLocked(userID)
}

// onlineLocked reports whether the account still has a live connection. Caller
// holds the hub lock.
func (h *Hub) onlineLocked(userID int64) bool {
	for other := range h.clients {
		if other.identity.UserID == userID {
			return true
		}
	}
	return false
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

// detachRoom stops delivering a room's updates to a client. It is called before
// the session is told about a departure, so the snapshot the departure publishes
// can never reach the client that has just walked away.
func (h *Hub) detachRoom(c *Client, roomID int64) {
	if roomID == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.detachRoomLocked(c, roomID)
}

// detachAll stops delivering updates for every room a client watches.
func (h *Hub) detachAll(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for roomID := range c.rooms {
		h.detachRoomLocked(c, roomID)
	}
}

// detachRoomLocked drops one watch entry. Caller holds the hub lock.
func (h *Hub) detachRoomLocked(c *Client, roomID int64) {
	delete(c.rooms, roomID)
	if set, ok := h.byRoom[roomID]; ok {
		delete(set, c)
		if len(set) == 0 {
			delete(h.byRoom, roomID)
		}
	}
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

// presenceView renders the lobby roster.
//
// The status must come from the session, never from counting hub connections:
// counting made two players in the same room disagree — whoever the map loop
// reached first was reported as "waiting" while the other was "playing" — and a
// room that is merely ready (both seats taken, nobody has pressed 准备) is
// "waiting" for both of them, not "playing".
//
// Locks are taken one at a time: snapshot the hub, release it, then ask the
// session. The broadcast worker goes the other way (session hook → hub lock),
// so holding both here would be a deadlock waiting to happen.
func (h *Hub) presenceView() PresenceView {
	type connInfo struct {
		userID int64
		name   string
		rooms  []int64
	}

	h.mu.RLock()
	conns := make([]connInfo, 0, len(h.clients))
	for c := range h.clients {
		info := connInfo{userID: c.identity.UserID, name: c.identity.Name}
		for id := range c.rooms {
			info.rooms = append(info.rooms, id)
		}
		conns = append(conns, info)
	}
	h.mu.RUnlock()

	view := PresenceView{Online: make([]PresenceEntry, 0, len(conns))}
	for _, info := range conns {
		entry := PresenceEntry{UserID: info.userID, Name: info.name, Status: StatusOnline}
		for _, id := range info.rooms {
			switch h.presenceStatus(id, info.userID) {
			case StatusPlaying:
				entry.RoomID, entry.Status = id, StatusPlaying
			case StatusWaiting:
				if entry.Status != StatusPlaying {
					entry.RoomID, entry.Status = id, StatusWaiting
				}
			}
		}
		view.Online = append(view.Online, entry)
	}

	h.mu.RLock()
	view.Rooms = len(h.byRoom)
	h.mu.RUnlock()
	return view
}

// presenceStatus maps a room's real state onto one person's lobby status. A
// spectator holds no seat, so watching does not change that person's status.
func (h *Hub) presenceStatus(roomID, userID int64) string {
	r, ok := h.session.RoomByID(roomID)
	if !ok {
		return StatusOnline
	}
	if _, seated := r.SideOf(userID); !seated {
		return StatusOnline
	}
	switch r.Status {
	case gamehall.StatusPlaying:
		return StatusPlaying
	case gamehall.StatusWaiting, gamehall.StatusReady:
		return StatusWaiting
	}
	return StatusOnline
}

// SpectatorCount reports how many connections watch a room without holding a
// seat in it.
func (h *Hub) SpectatorCount(roomID int64) int {
	h.mu.RLock()
	watchers := len(h.byRoom[roomID])
	h.mu.RUnlock()

	seated := 0
	if r, ok := h.session.RoomByID(roomID); ok {
		if r.Red.UserID != 0 {
			seated++
		}
		if r.Black.UserID != 0 {
			seated++
		}
	}
	if watchers <= seated {
		return 0
	}
	return watchers - seated
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

// defaultDisconnectGrace is how long a seated player may stay offline before
// the game is forfeited in the opponent's favour.
const defaultDisconnectGrace = 60 * time.Second

// forfeitAbandoned finishes games whose seated player stayed offline past the
// grace period.
//
// Without this the room would sit in playing forever when the game clock is
// disabled, which it is by default. Reconnecting inside the grace period simply
// drops the entry, so a flaky network does not cost a game.
func (h *Hub) forfeitAbandoned(ctx context.Context) {
	now := time.Now()

	h.mu.Lock()
	due := make([]int64, 0, 4)
	for userID, since := range h.pending {
		if h.onlineLocked(userID) {
			delete(h.pending, userID)
			continue
		}
		if now.Sub(since) >= h.grace {
			due = append(due, userID)
		}
	}
	h.mu.Unlock()

	for _, userID := range due {
		// The sweep runs outside the hub lock: it calls into the session, and
		// the lock order is hub -> session everywhere else too.
		h.mu.Lock()
		delete(h.pending, userID)
		h.mu.Unlock()

		roomID, seated := h.session.RoomIDForUser(userID)
		if !seated {
			continue
		}
		// Leaving a running room abandons the game and records the loss, which is
		// exactly the outcome an abandoned game should have.
		out := h.session.Leave(ctx, roomID, userID)
		if out.Result != nil {
			h.announceResult(roomID, out.Result)
		}
		if !out.Dissolved {
			h.broadcastRoom(roomID)
		}
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
			h.forfeitAbandoned(ctx)
		case <-presence.C:
			h.sendPresence()
		case <-reap.C:
			if n := h.session.ReapWaiting(2 * time.Hour); n > 0 {
				h.logger.Info("reaped abandoned waiting rooms", "count", n)
			}
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
	case errors.Is(err, gamehall.ErrGameInProgress):
		return ErrorView{Code: "game_in_progress", Message: "对局已开始，先下完这一局"}
	case errors.Is(err, gamehall.ErrNotYourTurn):
		return ErrorView{Code: "not_your_turn", Message: "还没轮到你走棋"}
	case errors.Is(err, gamehall.ErrGameFinished):
		return ErrorView{Code: "finished", Message: "对局已结束"}
	case errors.Is(err, gamehall.ErrGameNotStarted):
		return ErrorView{Code: "not_started", Message: "双方准备后对局才开始"}
	case errors.Is(err, gamehall.ErrNothingPending):
		return ErrorView{Code: "nothing_pending", Message: "没有待处理的请求"}
	case errors.Is(err, game.ErrGameOver):
		return ErrorView{Code: "finished", Message: "对局已结束"}
	case errors.Is(err, game.ErrIllegalMove):
		return ErrorView{Code: "illegal_move", Message: "不能这样走，被将军时必须应将"}
	case errors.Is(err, game.ErrNoSuchPiece):
		return ErrorView{Code: "no_piece", Message: "该位置没有棋子"}
	case errors.Is(err, game.ErrWrongSide):
		return ErrorView{Code: "wrong_side", Message: "不能移动对方的棋子"}
	default:
		if err == nil {
			return ErrorView{Code: "unknown", Message: "未知错误"}
		}
		return ErrorView{Code: "internal", Message: err.Error()}
	}
}
