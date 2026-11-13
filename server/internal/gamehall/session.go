// Package gamehall holds the live-game state machine. It is transport
// agnostic: the WebSocket layer and the HTTP layer both drive it, and it is
// fully testable without a network or a database.
package gamehall

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"xiangqi/internal/ai"
	"xiangqi/internal/game"
)

// Errors returned to callers. They are translated into wire error codes by the
// transport layer.
var (
	ErrRoomNotFound   = errors.New("gamehall: room not found")
	ErrRoomFull       = errors.New("gamehall: room is full")
	ErrNotASeat       = errors.New("gamehall: you are not seated in this room")
	ErrNotYourTurn    = errors.New("gamehall: it is not your turn")
	ErrGameFinished   = errors.New("gamehall: the game has finished")
	ErrGameNotStarted = errors.New("gamehall: the game has not started")
	ErrGameInProgress = errors.New("gamehall: the game is already under way")
	ErrNothingPending = errors.New("gamehall: there is no pending offer")
	ErrSameSeat       = errors.New("gamehall: cannot play against yourself")
)

// Status values for a room.
//
// A room walks through them in one direction, with a single step backwards:
// waiting (one seat free) → ready (both seats taken, waiting for 准备) → playing
// → finished. A finished room returns to ready when both players accept a
// rematch, so the same room hosts game after game until somebody leaves.
const (
	StatusWaiting  = "waiting"
	StatusReady    = "ready"
	StatusPlaying  = "playing"
	StatusFinished = "finished"
)

// Config tunes a Session.
type Config struct {
	// TurnTimeout is the time each side gets per move. It counts down only while
	// that side is to play, so the clock does not depend on wall-clock alignment
	// between the two players. Zero means unlimited time per move.
	TurnTimeout time.Duration
	// GameTimeout is the total time each side may spend over the whole game.
	// Zero means unlimited, i.e. no total clock.
	GameTimeout time.Duration
	// AIDepth caps the search budget the computer is allowed per move.
	AIBudget time.Duration
	// RatedDefault marks new games as rated unless the caller says otherwise.
	// It is a pointer because a zero bool cannot express "unset", and unset must
	// mean rated: a plain bool would silently make every game casual.
	RatedDefault *bool
	// Now is the clock, injected so tests can control time.
	Now func() time.Time
}

// DefaultConfig returns sensible production defaults.
//
// The clocks are OFF by default: a zero TurnTimeout / GameTimeout means
// "unlimited", and the code below treats zero as a real setting rather than as
// "unset", so a game is never decided by a clock unless an operator configures
// one through XIANGQI_TURN_TIMEOUT / XIANGQI_GAME_TIMEOUT.
func DefaultConfig() Config {
	rated := true
	return Config{
		// Explicit zeros: the move clock and the total clock are unlimited.
		TurnTimeout:  0,
		GameTimeout:  0,
		AIBudget:     900 * time.Millisecond,
		RatedDefault: &rated,
		Now:          time.Now,
	}
}

// withDefaults fills in unset fields.
//
// TurnTimeout and GameTimeout are deliberately NOT defaulted here: zero is the
// documented "unlimited" value for both, so a caller that passes zero must get
// no clock rather than being silently handed one.
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.AIBudget == 0 {
		c.AIBudget = d.AIBudget
	}
	if c.RatedDefault == nil {
		c.RatedDefault = d.RatedDefault
	}
	if c.Now == nil {
		c.Now = d.Now
	}
	return c
}

// Recorder persists games. Every method may be a no-op; a nil Recorder is
// valid and used by tests.
type Recorder interface {
	// StartGame marks the persisted game as playing and returns nothing useful.
	StartGame(ctx context.Context, gameID int64) error
	// AddMove appends a ply.
	AddMove(ctx context.Context, gameID int64, m MoveRecord)
	// TruncateMoves drops every ply after keepPly and restores the game's
	// denormalised position. A takeback rewinds the board; without this the store
	// still holds the old plies, and the next move that reuses a ply number hits
	// uk_moves_game_ply and is silently lost.
	TruncateMoves(ctx context.Context, gameID int64, keepPly int, fen string)
	// FinishGame records the result.
	FinishGame(ctx context.Context, gameID int64, r Result)
	// Finish updates both players' ratings and counters. The deltas are added to
	// the current ratings.
	Finish(ctx context.Context, redID, blackID int64, redDelta, blackDelta int)
}

// MoveRecord is one ply as handed to the Recorder.
type MoveRecord struct {
	Ply      int
	Side     string
	UCI      string
	Notation string
	FEN      string
	Check    bool
	MSSpent  int64
}

// Result is a finished game as handed to the Recorder.
type Result struct {
	GameID      int64
	Outcome     string
	Reason      string
	WinnerSide  string
	FEN         string
	MoveCount   int
	RedMSLeft   int64
	BlackMSLeft int64
	Rated       bool
	RedUserID   int64
	BlackUserID int64
}

// Seat is one player in a room.
type Seat struct {
	UserID  int64
	Name    string
	IsAI    bool
	AILevel ai.Level
	// Connected is true while a transport is attached for this seat. The
	// computer's seat is always considered connected.
	Connected bool
	// JoinedAt is when the seat was taken.
	JoinedAt time.Time
	// MSLeft is the total clock remaining for this seat.
	MSLeft int64
	// TurnDeadline is when the current move must be played, zero when it is not
	// this seat's turn or the clock is disabled.
	TurnDeadline time.Time
	// MoveCount counts the plies this seat has played.
	MoveCount int
	// Ready records that this player accepted the next game by pressing 准备.
	// It is cleared when the game starts. A seat held by the computer is always
	// considered ready.
	Ready bool
}

// SideName renders a side as the wire string.
func SideName(s game.Side) string {
	if s == game.Black {
		return "black"
	}
	return "red"
}

// Room is a single game with its two seats.
type Room struct {
	ID int64
	// PersistedID is the games table row for this room, or zero when the room
	// has not been persisted.
	PersistedID int64
	Game        *game.Game
	Red         Seat
	Black       Seat

	Status string
	Rated  bool
	// CreatedAt and StartedAt bound the game for reporting.
	CreatedAt time.Time
	StartedAt time.Time

	Moves []MoveRecord

	DrawOfferedBy       game.Side
	HasDrawOffer        bool
	TakebackRequestedBy game.Side
	HasTakebackRequest  bool

	// aiThinking is true from the moment a search for this room is claimed by
	// TakeAISeat until the search concludes in SubmitAIMove. It keeps a second
	// concurrent search from being started for the same turn; a later turn is
	// claimable again once the computer's move has been submitted.
	aiThinking bool

	// Result holds the terminal summary once the game is decided. It is kept on
	// the room so every snapshot carries the outcome, which makes the final
	// state authoritative rather than dependent on a one-off frame arriving.
	Result *RoomResult
}

// Turn returns the side to play.
func (r *Room) Turn() game.Side { return r.Game.Turn() }

// SeatFor returns the seat belonging to a side.
func (r *Room) SeatFor(side game.Side) *Seat {
	if side == game.Red {
		return &r.Red
	}
	return &r.Black
}

// SideOf returns the side a user occupies, and whether they occupy one.
func (r *Room) SideOf(userID int64) (game.Side, bool) {
	switch {
	case userID != 0 && r.Red.UserID == userID:
		return game.Red, true
	case userID != 0 && r.Black.UserID == userID:
		return game.Black, true
	}
	return game.Red, false
}

// Filled reports whether both seats are occupied, by a player or by the
// computer. A seat taken by the computer is taken, so this — and not "are there
// two human UserIDs" — is what answers "is there room for another player".
func (r *Room) Filled() bool {
	return (r.Red.UserID != 0 || r.Red.IsAI) && (r.Black.UserID != 0 || r.Black.IsAI)
}

// Occupied reports whether anybody at all is still in the room. It is what
// decides whether a room survives a departure.
func (r *Room) Occupied() bool {
	return r.Red.UserID != 0 || r.Black.UserID != 0 || r.Red.IsAI || r.Black.IsAI
}

// sideReady reports whether a side has accepted the next game. The computer
// always has.
func (r *Room) sideReady(side game.Side) bool {
	seat := r.SeatFor(side)
	return seat.Ready || seat.IsAI
}

// BothReady reports whether both sides accepted the next game.
func (r *Room) BothReady() bool {
	return r.sideReady(game.Red) && r.sideReady(game.Black)
}

// Session owns every live room. All exported methods are safe for concurrent
// use; each takes the session lock for the duration of the mutation.
type Session struct {
	cfg Config

	mu    sync.Mutex
	rooms map[int64]*Room
	// byUser maps a user id onto the room they are currently seated in.
	byUser map[int64]int64
	// nextID hands out room identifiers.
	nextID int64
	// waiting lists room ids that have one free seat, for quick matching.
	waiting []int64

	recorder Recorder
	// onUpdate is called after any mutation with the affected room ids, so the
	// transport can push fresh snapshots. It runs with the session lock held and
	// therefore must not call back into the Session.
	onUpdate func(roomIDs []int64)
}

// NewSession returns an empty session. recorder may be nil.
func NewSession(cfg Config, recorder Recorder) *Session {
	return &Session{
		cfg:      cfg.withDefaults(),
		rooms:    make(map[int64]*Room),
		byUser:   make(map[int64]int64),
		recorder: recorder,
	}
}

// SetUpdateHook installs the callback invoked after every change.
func (s *Session) SetUpdateHook(fn func(roomIDs []int64)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onUpdate = fn
}

// SetClock replaces the clock, which tests use to drive the timeouts.
func (s *Session) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Now = now
}

// Config returns the effective configuration.
func (s *Session) Config() Config { return s.cfg }

// RoomByID returns a snapshot pointer to a room. The pointer must only be read
// while holding the lock or from a copy produced by View.
func (s *Session) RoomByID(id int64) (*Room, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[id]
	return r, ok
}

// RoomIDForUser returns the room a user is seated in.
func (s *Session) RoomIDForUser(userID int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byUser[userID]
	return id, ok
}

// RoomIDs lists every live room.
func (s *Session) RoomIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, 0, len(s.rooms))
	for id := range s.rooms {
		out = append(out, id)
	}
	return out
}

// JoinRequest asks to be seated.
type JoinRequest struct {
	UserID int64
	Name   string
	// RoomID targets a specific room; zero means "find or create one".
	RoomID int64
	// AILevel requests a game against the computer. Zero means a human opponent.
	AILevel ai.Level
	// Side is the requested side against the computer; empty means the server
	// picks. It is ignored for human games, where the first player is Red.
	Side string
	// Rated overrides the default rating flag.
	Rated *bool
}

// Join seats a player, creating a room when necessary, and returns the room id
// and the side they were given.
//
// A player who is already seated keeps that seat, unless the request asks for a
// different room or for a game their current room cannot offer. Asking for
// another seat therefore implicitly leaves the old one.
func (s *Session) Join(ctx context.Context, req JoinRequest) (int64, game.Side, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.UserID != 0 {
		if existing, ok := s.byUser[req.UserID]; ok {
			if r := s.rooms[existing]; r != nil {
				side, seated := r.SideOf(req.UserID)
				if seated && resumable(r, req) {
					r.SeatFor(side).Connected = true
					s.notify([]int64{r.ID})
					return r.ID, side, nil
				}
				// Free the old seat first: a player holds exactly one seat, and
				// walking away from a running game is a loss for them.
				s.leaveLocked(ctx, existing, req.UserID)
			}
		}
	}

	if req.RoomID != 0 {
		return s.joinSpecific(req)
	}
	if req.AILevel != 0 {
		return s.joinAI(req)
	}
	return s.joinMatchmaking(req)
}

// resumable reports whether an existing seat satisfies a join request, so a
// reconnect or a duplicate join returns the player to the game they are in.
//
// A quick match deliberately does not resume a finished room: the player asked
// for an opponent, not to stare at the board of a game that is over.
func resumable(r *Room, req JoinRequest) bool {
	switch {
	case req.RoomID != 0:
		return r.ID == req.RoomID
	case req.AILevel != 0:
		return r.Status != StatusFinished && (r.Red.IsAI || r.Black.IsAI)
	default:
		return r.Status != StatusFinished
	}
}

// joinSpecific seats a player in a named room.
func (s *Session) joinSpecific(req JoinRequest) (int64, game.Side, error) {
	r, ok := s.rooms[req.RoomID]
	if !ok {
		return 0, game.Red, ErrRoomNotFound
	}
	// A finished room only turns people away when it is full. After somebody
	// walked out mid-game the room keeps a free seat, and a spectator is
	// allowed to take it: the room then waits for both sides to press 准备,
	// exactly like any other rematch.
	if r.Status == StatusFinished && r.Filled() {
		return 0, game.Red, ErrGameFinished
	}
	// Rejoining your own seat.
	if side, ok := r.SideOf(req.UserID); ok {
		r.SeatFor(side).Connected = true
		s.notify([]int64{r.ID})
		return r.ID, side, nil
	}
	if r.Filled() {
		return 0, game.Red, ErrRoomFull
	}
	side := game.Red
	if r.Red.UserID != 0 {
		side = game.Black
	}
	s.seat(r, side, req.UserID, req.Name, false, 0)
	s.advanceLocked(r)
	s.notify([]int64{r.ID})
	return r.ID, side, nil
}

// joinMatchmaking finds a waiting room or opens a new one.
func (s *Session) joinMatchmaking(req JoinRequest) (int64, game.Side, error) {
	for len(s.waiting) > 0 {
		id := s.waiting[0]
		s.waiting = s.waiting[1:]
		r, ok := s.rooms[id]
		if !ok || r.Status != StatusWaiting || r.Filled() {
			continue
		}
		if r.Red.UserID != 0 && r.Red.UserID == req.UserID {
			continue
		}
		side := game.Red
		if r.Red.UserID != 0 {
			side = game.Black
		}
		s.seat(r, side, req.UserID, req.Name, false, 0)
		s.advanceLocked(r)
		s.notify([]int64{r.ID})
		return r.ID, side, nil
	}

	// The client does not get to decide whether a game counts for rating: a loss
	// could otherwise be dodged by asking for an unrated room. The server's
	// RatedDefault is authoritative.
	r := s.newRoomLocked(nil)
	s.seat(r, game.Red, req.UserID, req.Name, false, 0)
	s.enqueueWaitingLocked(r)
	s.notify([]int64{r.ID})
	return r.ID, game.Red, nil
}

// joinAI opens a room whose opponent is the computer. The computer is always
// ready, so such a game starts as soon as it is created.
func (s *Session) joinAI(req JoinRequest) (int64, game.Side, error) {
	// The client does not get to decide whether a game counts for rating: a loss
	// could otherwise be dodged by asking for an unrated room. The server's
	// RatedDefault is authoritative.
	r := s.newRoomLocked(nil)

	humanSide := game.Red
	switch strings.ToLower(strings.TrimSpace(req.Side)) {
	case "black", "b":
		humanSide = game.Black
	case "red", "r", "":
		// Red is the traditional first player and the default for a new player.
		humanSide = game.Red
	}
	aiSide := humanSide.Opponent()

	s.seat(r, humanSide, req.UserID, req.Name, false, 0)
	s.seat(r, aiSide, 0, fmt.Sprintf("电脑（%s）", req.AILevel), true, req.AILevel)
	s.beginLocked(r, false)
	s.notify([]int64{r.ID})
	return r.ID, humanSide, nil
}

// newRoomLocked creates an empty room. The caller must hold the lock.
func (s *Session) newRoomLocked(rated *bool) *Room {
	s.nextID++
	now := s.cfg.Now()
	isRated := true
	if s.cfg.RatedDefault != nil {
		isRated = *s.cfg.RatedDefault
	}
	if rated != nil {
		isRated = *rated
	}
	r := &Room{
		ID:        s.nextID,
		Game:      game.NewGame(),
		Status:    StatusWaiting,
		Rated:     isRated,
		CreatedAt: now,
		Red:       Seat{MSLeft: s.cfg.GameTimeout.Milliseconds()},
		Black:     Seat{MSLeft: s.cfg.GameTimeout.Milliseconds()},
	}
	s.rooms[r.ID] = r
	return r
}

// seat installs a player in a seat. The caller must hold the lock.
func (s *Session) seat(r *Room, side game.Side, userID int64, name string, isAI bool, level ai.Level) {
	seat := r.SeatFor(side)
	seat.UserID = userID
	seat.Name = name
	seat.IsAI = isAI
	seat.AILevel = level
	seat.Connected = true
	seat.JoinedAt = s.cfg.Now()
	if userID != 0 {
		s.byUser[userID] = r.ID
	}
}

// advanceLocked recomputes the state of a room that is not in play, from who is
// seated and who has pressed 准备. It reports whether a game started and whether
// that game is a rematch of the previous one. Caller holds the lock.
//
// Two human players both have to accept before the pieces move: joining a room
// only fills a seat, it never starts a game on its own.
func (s *Session) advanceLocked(r *Room) (started, restarted bool) {
	if r.Status == StatusPlaying {
		return false, false
	}
	if !r.Filled() {
		if r.Status != StatusFinished {
			r.Status = StatusWaiting
		}
		return false, false
	}
	// Somebody is in both seats from here on, so the room stops being an open
	// invitation in the lobby.
	s.removeFromWaitingLocked(r.ID)
	if !r.BothReady() {
		if r.Status != StatusFinished {
			r.Status = StatusReady
		}
		return false, false
	}
	restarted = r.Status == StatusFinished
	s.beginLocked(r, restarted)
	return true, restarted
}

// beginLocked puts a room into play. restart asks for a fresh game in the same
// room, with the two seats trading colours. Caller holds the lock.
func (s *Session) beginLocked(r *Room, restart bool) {
	if restart {
		s.resetLocked(r, true)
	}
	r.Status = StatusPlaying
	r.StartedAt = s.cfg.Now()
	r.Red.Ready = false
	r.Black.Ready = false
	s.armClockLocked(r)
	s.removeFromWaitingLocked(r.ID)
	s.startRecorderLocked(r)
}

// resetLocked prepares a room for its next game. When swap is true the two seats
// trade colours, which is what "再来一局换边" means. Caller holds the lock.
//
// The persisted game id is cleared as well: the next game is a new row in the
// games table, because one room can host many games in a row.
func (s *Session) resetLocked(r *Room, swap bool) {
	if swap {
		r.Red, r.Black = r.Black, r.Red
	}
	now := s.cfg.Now()
	r.Game = game.NewGame()
	r.Moves = nil
	r.Result = nil
	r.HasDrawOffer = false
	r.DrawOfferedBy = game.Red
	r.HasTakebackRequest = false
	r.TakebackRequestedBy = game.Red
	r.aiThinking = false
	r.PersistedID = 0
	r.StartedAt = time.Time{}
	r.CreatedAt = now
	for _, side := range []game.Side{game.Red, game.Black} {
		seat := r.SeatFor(side)
		seat.Ready = false
		seat.MoveCount = 0
		seat.MSLeft = s.cfg.GameTimeout.Milliseconds()
		seat.TurnDeadline = time.Time{}
		seat.JoinedAt = now
	}
}

// ReadyOutcome reports what pressing 准备 did.
type ReadyOutcome struct {
	// Started is true when this press started the game.
	Started bool
	// Restarted is true when the game that started is a rematch in a room whose
	// previous game had already finished; the seats have swapped colours.
	Restarted bool
	// Reopened is true when the opponent had already left, so instead of
	// starting, the room went back to the lobby waiting for a new opponent.
	Reopened bool
}

// Ready records that a seated player accepts the next game. As soon as both
// sides have accepted the room starts — or restarts, after a finished game, in
// which case the two players swap colours and stay in the same room.
//
// Pressing 准备 in a finished room whose opponent has left reopens the room for
// matchmaking, so a player is never stuck in front of an empty board.
func (s *Session) Ready(ctx context.Context, roomID, userID int64) (ReadyOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return ReadyOutcome{}, ErrRoomNotFound
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return ReadyOutcome{}, ErrNotASeat
	}
	if r.Status == StatusPlaying {
		return ReadyOutcome{}, ErrGameInProgress
	}

	r.SeatFor(side).Ready = true
	out := ReadyOutcome{}
	out.Started, out.Restarted = s.advanceLocked(r)
	if !out.Started && r.Status == StatusFinished && !r.Filled() {
		// Alone in a room whose game is over: reopen it instead of leaving the
		// player in front of an empty board. The acceptance just given outlives
		// the reset, so a newcomer only has to accept in turn.
		s.resetLocked(r, false)
		r.SeatFor(side).Ready = true
		r.Status = StatusWaiting
		s.enqueueWaitingLocked(r)
		out.Reopened = true
	}
	s.notify([]int64{r.ID})
	return out, nil
}

// LeaveOutcome reports what leaving a room did.
type LeaveOutcome struct {
	RoomID int64
	// Dissolved is true when the room was dropped because nobody was left in it.
	Dissolved bool
	// Result is set when leaving ended a game that was still running.
	Result *Result
}

// Leave frees the caller's seat. It is idempotent: leaving a room that is gone,
// or that the caller is not seated in, does nothing.
//
// Leaving a room whose opponent is still there ends a running game as abandoned
// (the player who walked away loses) and keeps whatever the game had reached.
// Leaving the last seat dissolves the room, which is how a cancelled quick match
// stops showing up in the lobby.
func (s *Session) Leave(ctx context.Context, roomID, userID int64) LeaveOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.leaveLocked(ctx, roomID, userID)
}

// leaveLocked is Leave with the lock already held, so Join can hand a seat back
// before taking another one. Caller holds the lock.
func (s *Session) leaveLocked(ctx context.Context, roomID, userID int64) LeaveOutcome {
	out := LeaveOutcome{RoomID: roomID}
	r, ok := s.rooms[roomID]
	if !ok {
		return out
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return out
	}

	// A game that is still running is abandoned: the opponent wins and the
	// result is recorded before the seat is handed back, because the rating
	// exchange needs both user ids.
	if r.Status == StatusPlaying {
		_ = r.Game.Abandon(side)
		out.Result = s.finishLocked(ctx, r)
	}

	s.vacateLocked(r, side)
	s.removeFromWaitingLocked(r.ID)

	if !r.Occupied() {
		delete(s.rooms, r.ID)
		out.Dissolved = true
	} else if r.Status != StatusFinished {
		// The opponent is still here but the game never started, so the room
		// goes back to the lobby instead of pretending to be ready.
		r.Status = StatusWaiting
		s.enqueueWaitingLocked(r)
	}
	s.notify([]int64{roomID})
	return out
}

// vacateLocked empties a seat, keeping the other seat intact, and clears the
// player index so the user is free to take another seat. Caller holds the lock.
func (s *Session) vacateLocked(r *Room, side game.Side) {
	seat := r.SeatFor(side)
	if seat.UserID != 0 && s.byUser[seat.UserID] == r.ID {
		delete(s.byUser, seat.UserID)
	}
	*seat = Seat{MSLeft: s.cfg.GameTimeout.Milliseconds()}
}

// enqueueWaitingLocked advertises a room as open for an opponent, without
// listing it twice. Caller holds the lock.
func (s *Session) enqueueWaitingLocked(r *Room) {
	for _, id := range s.waiting {
		if id == r.ID {
			return
		}
	}
	s.waiting = append(s.waiting, r.ID)
}

// startRecorderLocked tells the recorder the game is under way.
func (s *Session) startRecorderLocked(r *Room) {
	if s.recorder == nil || r.PersistedID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.recorder.StartGame(ctx, r.PersistedID)
}

// removeFromWaitingLocked drops a room id from the matchmaking queue.
func (s *Session) removeFromWaitingLocked(id int64) {
	for i, candidate := range s.waiting {
		if candidate == id {
			s.waiting = append(s.waiting[:i], s.waiting[i+1:]...)
			return
		}
	}
}

// armClockLocked sets the move deadline for the side to play.
func (s *Session) armClockLocked(r *Room) {
	if r.Status != StatusPlaying {
		return
	}
	side := r.Game.Turn()
	seat := r.SeatFor(side)
	if s.cfg.TurnTimeout > 0 {
		seat.TurnDeadline = s.cfg.Now().Add(s.cfg.TurnTimeout)
	} else {
		seat.TurnDeadline = time.Time{}
	}
	other := r.SeatFor(side.Opponent())
	other.TurnDeadline = time.Time{}
}

// SetPersistedID records the database id of a room's game row.
func (s *Session) SetPersistedID(roomID, gameID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rooms[roomID]; ok {
		r.PersistedID = gameID
	}
}

// SetRecorder swaps the persistence sink.
func (s *Session) SetRecorder(rec Recorder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recorder = rec
}

// Disconnect marks a seat as no longer connected.
func (s *Session) Disconnect(roomID, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[roomID]
	if !ok {
		return
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return
	}
	r.SeatFor(side).Connected = false
	s.notify([]int64{r.ID})
}

// MoveOutcome describes what happened when a move was submitted.
type MoveOutcome struct {
	Room     *Room
	Move     game.Move
	Record   MoveRecord
	GameOver bool
	Result   *Result
	// AITurn is true when the computer is now to move in this room.
	AITurn bool
	// AISide is the side the computer plays in this room.
	AISide game.Side
}

// SubmitMove validates and applies a human move. On success the room snapshot
// reflects the new position and Result is non-nil when the game just ended.
func (s *Session) SubmitMove(ctx context.Context, roomID, userID int64, from, to string) (MoveOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return MoveOutcome{}, ErrRoomNotFound
	}
	if r.Status == StatusFinished {
		return MoveOutcome{}, ErrGameFinished
	}
	if r.Status != StatusPlaying {
		return MoveOutcome{}, ErrGameNotStarted
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return MoveOutcome{}, ErrNotASeat
	}
	return s.applyMoveLocked(ctx, r, side, from, to)
}

// SubmitAIMove plays the computer's move in a room. It is called by the
// transport after a search completes.
func (s *Session) SubmitAIMove(ctx context.Context, roomID int64, mv game.Move) (MoveOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return MoveOutcome{}, ErrRoomNotFound
	}
	// 搜索已经结束，无论这一步最终是否被采纳，都要把认领标志放掉。否则一次过期的
	// 着法（例如玩家在电脑思考期间悔了棋）会让认领永远挂在那里，电脑此后再也轮不到
	// 走棋，棋盘就卡死了。
	r.aiThinking = false
	if r.Status != StatusPlaying {
		return MoveOutcome{}, ErrGameNotStarted
	}
	side := r.Game.Turn()
	if !r.SeatFor(side).IsAI {
		return MoveOutcome{}, ErrNotYourTurn
	}
	return s.applyMoveLocked(ctx, r, side, mv.FromName(), mv.ToName())
}

// applyMoveLocked performs the shared move path. Caller holds the lock.
func (s *Session) applyMoveLocked(ctx context.Context, r *Room, side game.Side, from, to string) (MoveOutcome, error) {
	if r.Game.Turn() != side {
		return MoveOutcome{}, ErrNotYourTurn
	}
	mv, err := game.ParseMove(from + to)
	if err != nil {
		return MoveOutcome{}, fmt.Errorf("%w: %v", game.ErrIllegalMove, err)
	}

	seat := r.SeatFor(side)
	now := s.cfg.Now()
	spent := int64(0)
	if !seat.TurnDeadline.IsZero() {
		spent = s.cfg.TurnTimeout.Milliseconds() - seat.TurnDeadline.Sub(now).Milliseconds()
		if spent < 0 {
			spent = s.cfg.TurnTimeout.Milliseconds()
		}
		if spent > s.cfg.TurnTimeout.Milliseconds() {
			spent = s.cfg.TurnTimeout.Milliseconds()
		}
		seat.MSLeft -= spent
		if seat.MSLeft < 0 {
			seat.MSLeft = 0
		}
	}

	record, err := r.Game.Play(mv)
	if err != nil {
		return MoveOutcome{}, err
	}
	seat.MoveCount++

	rec := MoveRecord{
		Ply:      record.Ply,
		Side:     SideName(side),
		UCI:      mv.String(),
		Notation: record.Notation,
		FEN:      record.FEN,
		Check:    record.Check,
		MSSpent:  spent,
	}
	r.Moves = append(r.Moves, rec)
	// 局面一变，挂起的悔棋请求就失去意义了：对手这时再点「同意」，撤掉的是刚走的
	// 这一步，而不是当初请求的那一步。所以任何一步棋落地都让它作废。
	if r.HasTakebackRequest {
		r.HasTakebackRequest = false
		r.TakebackRequestedBy = game.Red
	}
	if s.recorder != nil && r.PersistedID != 0 {
		s.recorder.AddMove(ctx, r.PersistedID, rec)
	}

	out := MoveOutcome{Room: r, Move: mv, Record: rec}
	if r.Game.Over() {
		out.GameOver = true
		out.Result = s.finishLocked(ctx, r)
		s.notify([]int64{r.ID})
		return out, nil
	}

	s.armClockLocked(r)
	next := r.Game.Turn()
	if r.SeatFor(next).IsAI {
		// Only report the turn. aiThinking belongs to the search claim, which
		// TakeAISeat takes and SubmitAIMove releases; setting it here would make
		// the transport's TakeAISeat call refuse the very search it must start.
		out.AITurn = true
		out.AISide = next
	}
	s.notify([]int64{r.ID})
	return out, nil
}

// finishLocked records the result of a finished room and returns it. The result
// is always returned, even when no recorder is installed, so callers can report
// the outcome and the final position. Caller holds the lock.
//
// The two players deliberately keep their seats: a finished room is where a
// rematch happens, so both users stay bound to it until they leave.
func (s *Session) finishLocked(ctx context.Context, r *Room) *Result {
	r.Status = StatusFinished
	r.Red.TurnDeadline = time.Time{}
	r.Black.TurnDeadline = time.Time{}
	// A pending offer dies with the game: leaving it set would make the client
	// keep asking for an answer to a request that no longer means anything.
	r.HasDrawOffer = false
	r.DrawOfferedBy = game.Red
	r.HasTakebackRequest = false
	r.TakebackRequestedBy = game.Red

	winnerSide := ""
	if side, ok := r.Game.Winner(); ok {
		winnerSide = SideName(side)
	}
	res := &Result{
		GameID:      r.PersistedID,
		Outcome:     string(r.Game.Outcome()),
		Reason:      string(r.Game.Reason()),
		WinnerSide:  winnerSide,
		FEN:         r.Game.FEN(),
		MoveCount:   len(r.Moves),
		RedMSLeft:   maxInt64(r.Red.MSLeft, 0),
		BlackMSLeft: maxInt64(r.Black.MSLeft, 0),
		Rated:       r.Rated,
		RedUserID:   r.Red.UserID,
		BlackUserID: r.Black.UserID,
	}
	// Publish the terminal summary on the room so every subsequent snapshot
	// carries it.
	r.Result = &RoomResult{
		Outcome:    res.Outcome,
		Reason:     res.Reason,
		WinnerSide: res.WinnerSide,
		FEN:        res.FEN,
		MoveCount:  res.MoveCount,
	}
	if s.recorder == nil {
		return res
	}
	if res.GameID != 0 {
		s.recorder.FinishGame(ctx, res.GameID, *res)
	}

	// Ratings only move for two human players in a rated game.
	if r.Rated && !r.Red.IsAI && !r.Black.IsAI && r.Red.UserID != 0 && r.Black.UserID != 0 {
		redDelta, blackDelta := ratingDeltas(winnerSide)
		s.recorder.Finish(ctx, r.Red.UserID, r.Black.UserID, redDelta, blackDelta)
	}
	return res
}

// byUserDelete clears the user-to-room index for a room that is going away.
func (s *Session) byUserDelete(r *Room) {
	for _, id := range []int64{r.Red.UserID, r.Black.UserID} {
		if id != 0 && s.byUser[id] == r.ID {
			delete(s.byUser, id)
		}
	}
}

// ratingDeltas returns a simple Elo-style exchange: the winner gains the same
// amount the loser drops.
func ratingDeltas(winnerSide string) (red, black int) {
	const k = 16
	switch winnerSide {
	case "red":
		return k, -k
	case "black":
		return -k, k
	default:
		return 0, 0
	}
}

// Resign concedes the game for a seated player.
func (s *Session) Resign(ctx context.Context, roomID, userID int64) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return nil, ErrRoomNotFound
	}
	if r.Status == StatusFinished {
		return nil, ErrGameFinished
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return nil, ErrNotASeat
	}
	if err := r.Game.Resign(side); err != nil {
		return nil, err
	}
	res := s.finishLocked(ctx, r)
	s.notify([]int64{r.ID})
	return res, nil
}

// Timeout ends the game because the side to play ran out of time. It reports
// whether anything happened.
func (s *Session) Timeout(ctx context.Context, roomID int64) (*Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok || r.Status != StatusPlaying {
		return nil, false
	}
	side := r.Game.Turn()
	seat := r.SeatFor(side)
	if seat.TurnDeadline.IsZero() || s.cfg.Now().Before(seat.TurnDeadline) {
		return nil, false
	}
	if err := r.Game.Timeout(side); err != nil {
		return nil, false
	}
	res := s.finishLocked(ctx, r)
	s.notify([]int64{r.ID})
	return res, true
}

// ExpiredGame pairs a room with the result of its clock running out. The room
// id is reported explicitly because a Result carries the persisted game id,
// which is a different counter and must not be used to address live rooms.
type ExpiredGame struct {
	RoomID int64
	Result *Result
}

// ExpireTimeouts checks every playing room and ends the ones whose clock ran
// out. It returns the rooms that changed, with their results.
func (s *Session) ExpireTimeouts(ctx context.Context) []ExpiredGame {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.cfg.Now()
	var expired []ExpiredGame
	var changed []int64
	for _, r := range s.rooms {
		if r.Status != StatusPlaying {
			continue
		}
		side := r.Game.Turn()
		seat := r.SeatFor(side)
		if seat.TurnDeadline.IsZero() || now.Before(seat.TurnDeadline) {
			continue
		}
		if err := r.Game.Timeout(side); err != nil {
			continue
		}
		res := s.finishLocked(ctx, r)
		expired = append(expired, ExpiredGame{RoomID: r.ID, Result: res})
		changed = append(changed, r.ID)
	}
	if len(changed) > 0 {
		s.notify(changed)
	}
	return expired
}

// OfferDraw records a draw offer from a seated player.
//
// Against the computer the offer is accepted on the spot: a human offering a
// draw to an engine is asking for the game to stop, and leaving them to wait for
// a reply that never comes is just a dead end. The returned *Result is non-nil
// exactly when the computer accepted, i.e. when the game is already over.
func (s *Session) OfferDraw(ctx context.Context, roomID, userID int64) (game.Side, *Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return game.Red, nil, ErrRoomNotFound
	}
	if r.Status != StatusPlaying {
		return game.Red, nil, ErrGameNotStarted
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return game.Red, nil, ErrNotASeat
	}
	if r.SeatFor(side.Opponent()).IsAI {
		if err := r.Game.Draw(game.ReasonDrawAgreed); err != nil {
			return side, nil, err
		}
		res := s.finishLocked(ctx, r)
		s.notify([]int64{r.ID})
		return side, res, nil
	}
	r.DrawOfferedBy = side
	r.HasDrawOffer = true
	s.notify([]int64{r.ID})
	return side, nil, nil
}

// RespondDraw accepts or declines an outstanding draw offer. Accepting ends the
// game as a draw.
func (s *Session) RespondDraw(ctx context.Context, roomID, userID int64, accept bool) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return nil, ErrRoomNotFound
	}
	if !r.HasDrawOffer {
		return nil, ErrNothingPending
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return nil, ErrNotASeat
	}
	if side == r.DrawOfferedBy {
		return nil, fmt.Errorf("gamehall: you made the draw offer")
	}
	r.HasDrawOffer = false
	if !accept {
		s.notify([]int64{r.ID})
		return nil, nil
	}
	if err := r.Game.Draw(game.ReasonDrawAgreed); err != nil {
		return nil, err
	}
	res := s.finishLocked(ctx, r)
	s.notify([]int64{r.ID})
	return res, nil
}

// RequestTakeback records a takeback request.
//
// Against the computer the request is granted on the spot. The rewind then goes
// back to before the player's own last move: the engine has already answered, so
// undoing a single ply would only invite it to play the same move again. The
// returned *Room is non-nil exactly when the computer accepted.
func (s *Session) RequestTakeback(roomID, userID int64) (game.Side, *Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return game.Red, nil, ErrRoomNotFound
	}
	if r.Status != StatusPlaying {
		return game.Red, nil, ErrGameNotStarted
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return game.Red, nil, ErrNotASeat
	}
	if len(r.Moves) == 0 {
		return game.Red, nil, ErrNothingPending
	}
	if r.SeatFor(side.Opponent()).IsAI {
		if last := r.Moves[len(r.Moves)-1]; last.Side == SideName(side.Opponent()) {
			s.acceptTakebackLocked(r, side)
		}
		s.acceptTakebackLocked(r, side)
		s.notify([]int64{r.ID})
		return side, r, nil
	}
	r.TakebackRequestedBy = side
	r.HasTakebackRequest = true
	s.notify([]int64{r.ID})
	return side, nil, nil
}

// acceptTakebackLocked rewinds one accepted ply. side is the player who asked,
// so the move counter goes back on their opponent's account. Caller holds the
// lock.
func (s *Session) acceptTakebackLocked(r *Room, side game.Side) {
	if _, ok := r.Game.Undo(); !ok {
		return
	}
	if len(r.Moves) > 0 {
		undone := r.Moves[len(r.Moves)-1]
		r.Moves = r.Moves[:len(r.Moves)-1]
		// MoveCount counts plies per player, so it must go down on the side that
		// made the rewinded move rather than always on the requester's opponent.
		// The two differ when the AI answers twice: each accept rewinds one more
		// ply, and they belong to different players.
		moved := game.Red
		if undone.Side == SideName(game.Black) {
			moved = game.Black
		}
		if seat := r.SeatFor(moved); seat.MoveCount > 0 {
			seat.MoveCount--
		}
	}
	// Keep the stored game in step with the board. The recorder is synchronous, so
	// this lands before any new move that reuses the same ply numbers.
	if s.recorder != nil {
		s.recorder.TruncateMoves(context.Background(), r.PersistedID, len(r.Moves), r.Game.FEN())
	}
	s.armClockLocked(r)
}

// RespondTakeback accepts or declines an outstanding takeback request. A single
// accepted request rewinds one ply, which is the opponent's last move.
func (s *Session) RespondTakeback(roomID, userID int64, accept bool) (*Room, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return nil, ErrRoomNotFound
	}
	if !r.HasTakebackRequest {
		return nil, ErrNothingPending
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return nil, ErrNotASeat
	}
	if side == r.TakebackRequestedBy {
		return nil, fmt.Errorf("gamehall: you made the takeback request")
	}
	r.HasTakebackRequest = false
	if accept {
		s.acceptTakebackLocked(r, side)
	}
	s.notify([]int64{r.ID})
	return r, nil
}

// FinishAIGame ends a game abandoned by the human player against the computer.
func (s *Session) FinishAIGame(ctx context.Context, roomID int64, humanSide game.Side) *Result {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok || r.Status != StatusPlaying {
		return nil
	}
	if !r.Red.IsAI && !r.Black.IsAI {
		return nil
	}
	_ = r.Game.Resign(humanSide)
	res := s.finishLocked(ctx, r)
	s.notify([]int64{r.ID})
	return res
}

// notify calls the update hook. Caller holds the lock.
func (s *Session) notify(ids []int64) {
	if s.onUpdate != nil && len(ids) > 0 {
		s.onUpdate(ids)
	}
}

// TakeAISeat claims the computer's turn in a room. It reports the level when the
// side to play is the computer and no search has been claimed for that turn yet,
// and it marks the search as in flight so a second caller is refused. SubmitAIMove
// plays the move and releases the claim, which makes the next computer turn
// claimable exactly once.
func (s *Session) TakeAISeat(roomID int64) (ai.Level, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok || r.Status != StatusPlaying {
		return 0, false
	}
	side := r.Game.Turn()
	seat := r.SeatFor(side)
	if !seat.IsAI || r.aiThinking {
		return 0, false
	}
	r.aiThinking = true
	return seat.AILevel, true
}

// Snapshot renders a room for the wire. it copies everything so the caller can
// serialise it without holding the lock.
func (r *Room) Snapshot() RoomView {
	view := RoomView{
		ID:                  r.ID,
		Status:              r.Status,
		FEN:                 r.Game.FEN(),
		Turn:                SideName(r.Game.Turn()),
		InCheck:             r.Game.InCheck(),
		Outcome:             string(r.Game.Outcome()),
		Reason:              string(r.Game.Reason()),
		Rated:               r.Rated,
		Red:                 seatView(r.Red, "red"),
		Black:               seatView(r.Black, "black"),
		Moves:               make([]MoveView, 0, len(r.Moves)),
		LegalFrom:           map[string][]string{},
		DrawOfferedBy:       "",
		TakebackRequestedBy: "",
	}
	if side, ok := r.Game.Winner(); ok {
		view.WinnerSide = SideName(side)
	}
	for _, m := range r.Moves {
		mv, err := game.ParseMove(m.UCI)
		if err != nil {
			continue
		}
		view.Moves = append(view.Moves, MoveView{
			Ply: m.Ply, Side: m.Side, From: mv.FromName(), To: mv.ToName(),
			Notation: m.Notation, Check: m.Check, MSSpent: m.MSSpent,
		})
	}
	if r.HasDrawOffer {
		view.DrawOfferedBy = SideName(r.DrawOfferedBy)
	}
	if r.HasTakebackRequest {
		view.TakebackRequestedBy = SideName(r.TakebackRequestedBy)
	}
	if r.Result != nil {
		view.Result = &GameOverView{
			Outcome:    r.Result.Outcome,
			Reason:     r.Result.Reason,
			WinnerSide: r.Result.WinnerSide,
			FEN:        r.Result.FEN,
			MoveCount:  r.Result.MoveCount,
		}
	}
	if r.Status == StatusPlaying {
		for _, m := range r.Game.LegalMoves() {
			view.LegalFrom[m.FromName()] = append(view.LegalFrom[m.FromName()], m.ToName())
		}
	}
	return view
}

func seatView(s Seat, side string) PlayerView {
	return PlayerView{
		UserID:    s.UserID,
		Name:      s.Name,
		Side:      side,
		Online:    s.IsAI || s.Connected,
		IsAI:      s.IsAI,
		AILevel:   int(s.AILevel),
		MSLeft:    maxInt64(s.MSLeft, 0),
		MoveCount: s.MoveCount,
		// A seat played by the computer is always ready for the next game, so
		// the client can render readiness without special-casing the engine.
		Ready: s.Ready || s.IsAI,
	}
}

// View returns a fresh snapshot of a room by id.
func (s *Session) View(roomID int64) (RoomView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[roomID]
	if !ok {
		return RoomView{}, false
	}
	return r.Snapshot(), true
}

// SideOfUserIn reports which side a user holds in a room.
func (s *Session) SideOfUserIn(roomID, userID int64) (game.Side, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rooms[roomID]
	if !ok {
		return game.Red, false
	}
	return r.SideOf(userID)
}

// Presence lists the rooms that are open for an opponent.
type OpenRoom struct {
	ID        int64
	HostName  string
	HostSide  string
	Rated     bool
	CreatedAt time.Time
}

// OpenRooms lists waiting rooms, newest first.
func (s *Session) OpenRooms() []OpenRoom {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]OpenRoom, 0, len(s.waiting))
	for _, id := range s.waiting {
		r, ok := s.rooms[id]
		if !ok || r.Status != StatusWaiting {
			continue
		}
		host := r.Red
		side := "red"
		if r.Red.UserID == 0 {
			host = r.Black
			side = "black"
		}
		out = append(out, OpenRoom{ID: r.ID, HostName: host.Name, HostSide: side, Rated: r.Rated, CreatedAt: r.CreatedAt})
	}
	return out
}

// PlayingRoom is one row of the lobby's "对战中" list. It is deliberately much
// thinner than RoomView: the lobby polls this every few seconds and has no use
// for the board or the move list.
type PlayingRoom struct {
	ID        int64
	RedName   string
	BlackName string
	MoveCount int
	Rated     bool
	CreatedAt time.Time
}

// PlayingRooms lists the rooms whose game is running, newest first. Anybody may
// watch one, and watching never takes a seat.
func (s *Session) PlayingRooms() []PlayingRoom {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]PlayingRoom, 0, len(s.rooms))
	for _, r := range s.rooms {
		if r.Status != StatusPlaying {
			continue
		}
		out = append(out, PlayingRoom{
			ID:        r.ID,
			RedName:   r.Red.Name,
			BlackName: r.Black.Name,
			MoveCount: len(r.Moves),
			Rated:     r.Rated,
			CreatedAt: r.CreatedAt,
		})
	}
	// Newest first. Insertion sort: the list is tiny and this avoids pulling in
	// another import just for one sort.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID > out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ReapFinished drops old finished rooms so the process does not grow without
// bound. It returns how many were removed. Because a finished room keeps its
// players seated until they leave, their user index entries go with it.
func (s *Session) ReapFinished(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.cfg.Now().Add(-maxAge)
	removed := 0
	for id, r := range s.rooms {
		if r.Status == StatusFinished && r.StartedAt.Before(cutoff) && r.CreatedAt.Before(cutoff) {
			delete(s.rooms, id)
			s.byUserDelete(r)
			s.removeFromWaitingLocked(id)
			removed++
		}
	}
	return removed
}

// ReapWaiting drops rooms that have been waiting for an opponent since before
// the cutoff. Without it the lobby can be filled with abandoned tables: someone
// opens a room, closes the tab, and nothing ever collects it.
//
// Two hours is deliberately generous: a real player waiting that long is already
// unusual, and they can simply open a new room.
func (s *Session) ReapWaiting(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.cfg.Now().Add(-maxAge)
	removed := 0
	for id, r := range s.rooms {
		if r.Status == StatusPlaying || r.Status == StatusFinished {
			continue
		}
		if !r.CreatedAt.Before(cutoff) {
			continue
		}
		delete(s.rooms, id)
		s.byUserDelete(r)
		s.removeFromWaitingLocked(id)
		removed++
	}
	return removed
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
