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
	ErrNothingPending = errors.New("gamehall: there is no pending offer")
	ErrSameSeat       = errors.New("gamehall: cannot play against yourself")
)

// Status values for a room.
const (
	StatusWaiting  = "waiting"
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

// BothSeated reports whether the room has two players.
func (r *Room) BothSeated() bool {
	return r.Red.UserID != 0 && r.Black.UserID != 0
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
func (s *Session) Join(ctx context.Context, req JoinRequest) (int64, game.Side, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// A player already seated somewhere is returned to that seat rather than
	// being given a second game.
	if existing, ok := s.byUser[req.UserID]; ok && req.UserID != 0 {
		r := s.rooms[existing]
		if r != nil && r.Status != StatusFinished {
			if side, ok := r.SideOf(req.UserID); ok {
				r.SeatFor(side).Connected = true
				s.notify([]int64{r.ID})
				return r.ID, side, nil
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

// joinSpecific seats a player in a named room.
func (s *Session) joinSpecific(req JoinRequest) (int64, game.Side, error) {
	r, ok := s.rooms[req.RoomID]
	if !ok {
		return 0, game.Red, ErrRoomNotFound
	}
	if r.Status == StatusFinished {
		return 0, game.Red, ErrGameFinished
	}
	// Rejoining your own seat.
	if side, ok := r.SideOf(req.UserID); ok {
		r.SeatFor(side).Connected = true
		s.notify([]int64{r.ID})
		return r.ID, side, nil
	}
	if r.BothSeated() {
		return 0, game.Red, ErrRoomFull
	}
	side := game.Red
	if r.Red.UserID != 0 {
		side = game.Black
	}
	s.seat(r, side, req.UserID, req.Name, false, 0)
	s.startLocked(r)
	s.notify([]int64{r.ID})
	return r.ID, side, nil
}

// joinMatchmaking finds a waiting room or opens a new one.
func (s *Session) joinMatchmaking(req JoinRequest) (int64, game.Side, error) {
	for len(s.waiting) > 0 {
		id := s.waiting[0]
		s.waiting = s.waiting[1:]
		r, ok := s.rooms[id]
		if !ok || r.Status != StatusWaiting || r.BothSeated() {
			continue
		}
		if r.Red.UserID == req.UserID {
			continue
		}
		s.seat(r, game.Black, req.UserID, req.Name, false, 0)
		s.startLocked(r)
		s.notify([]int64{r.ID})
		return r.ID, game.Black, nil
	}

	r := s.newRoomLocked(req.Rated)
	s.seat(r, game.Red, req.UserID, req.Name, false, 0)
	s.waiting = append(s.waiting, r.ID)
	s.notify([]int64{r.ID})
	return r.ID, game.Red, nil
}

// joinAI opens a room whose opponent is the computer.
func (s *Session) joinAI(req JoinRequest) (int64, game.Side, error) {
	r := s.newRoomLocked(req.Rated)

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
	s.startLocked(r)
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

// startLocked begins play. A room needs both seats filled, and it needs either
// two human players or a player facing the computer. A seat taken by the
// computer counts as filled, so a game against the computer starts the moment
// it is created; a room posted for matchmaking must keep waiting for an
// opponent. Caller holds the lock.
func (s *Session) startLocked(r *Room) {
	if r.Status != StatusWaiting {
		return
	}
	hasAI := r.Red.IsAI || r.Black.IsAI
	if hasAI {
		if r.Red.UserID == 0 && r.Black.UserID == 0 {
			return
		}
	} else if !r.BothSeated() {
		return
	}

	r.Status = StatusPlaying
	r.StartedAt = s.cfg.Now()
	s.armClockLocked(r)
	s.removeFromWaitingLocked(r.ID)
	s.startRecorderLocked(r)
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
	if r.Status != StatusPlaying {
		return MoveOutcome{}, ErrGameNotStarted
	}
	side := r.Game.Turn()
	if !r.SeatFor(side).IsAI {
		return MoveOutcome{}, ErrNotYourTurn
	}
	r.aiThinking = false
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
func (s *Session) finishLocked(ctx context.Context, r *Room) *Result {
	r.Status = StatusFinished
	r.Red.TurnDeadline = time.Time{}
	r.Black.TurnDeadline = time.Time{}
	s.byUserDelete(r)

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

// byUserDelete clears the user-to-room index for a finished room.
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
func (s *Session) OfferDraw(roomID, userID int64) (game.Side, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return game.Red, ErrRoomNotFound
	}
	if r.Status != StatusPlaying {
		return game.Red, ErrGameNotStarted
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return game.Red, ErrNotASeat
	}
	r.DrawOfferedBy = side
	r.HasDrawOffer = true
	s.notify([]int64{r.ID})
	return side, nil
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
func (s *Session) RequestTakeback(roomID, userID int64) (game.Side, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rooms[roomID]
	if !ok {
		return game.Red, ErrRoomNotFound
	}
	if r.Status != StatusPlaying {
		return game.Red, ErrGameNotStarted
	}
	side, ok := r.SideOf(userID)
	if !ok {
		return game.Red, ErrNotASeat
	}
	if len(r.Moves) == 0 {
		return game.Red, ErrNothingPending
	}
	r.TakebackRequestedBy = side
	r.HasTakebackRequest = true
	s.notify([]int64{r.ID})
	return side, nil
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
	if !accept {
		s.notify([]int64{r.ID})
		return r, nil
	}
	if _, ok := r.Game.Undo(); !ok {
		s.notify([]int64{r.ID})
		return r, nil
	}
	if len(r.Moves) > 0 {
		r.Moves = r.Moves[:len(r.Moves)-1]
	}
	for _, s := range []game.Side{game.Red, game.Black} {
		seat := r.SeatFor(s)
		if seat.MoveCount > 0 && s == side.Opponent() {
			seat.MoveCount--
		}
	}
	s.armClockLocked(r)
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

// ReapFinished drops old finished rooms so the process does not grow without
// bound. It returns how many were removed.
func (s *Session) ReapFinished(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := s.cfg.Now().Add(-maxAge)
	removed := 0
	for id, r := range s.rooms {
		if r.Status == StatusFinished && r.StartedAt.Before(cutoff) && r.CreatedAt.Before(cutoff) {
			delete(s.rooms, id)
			removed++
		}
	}
	return removed
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
