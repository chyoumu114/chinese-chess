package gamehall

import (
	"context"
	"errors"
	"testing"
	"time"

	"xiangqi/internal/ai"
	"xiangqi/internal/game"
)

// fakeClock lets a test move time forward deterministically.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }
func (c *fakeClock) Advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func newTestSession(t *testing.T) (*Session, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	rated := true
	s := NewSession(Config{
		TurnTimeout:  60 * time.Second,
		GameTimeout:  10 * time.Minute,
		AIBudget:     50 * time.Millisecond,
		RatedDefault: &rated,
		Now:          clock.Now,
	}, nil)
	return s, clock
}

// TestMatchmakingPairsTwoPlayers checks that a second join takes the open seat
// of the first player's room, and that the game starts only once both players
// have accepted it.
func TestMatchmakingPairsTwoPlayers(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	roomA, sideA, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("first join: %v", err)
	}
	if sideA != game.Red {
		t.Fatalf("first player side = %v, want red", sideA)
	}
	view, _ := s.View(roomA)
	if view.Status != StatusWaiting {
		t.Fatalf("room status = %q, want waiting", view.Status)
	}

	roomB, sideB, err := s.Join(ctx, JoinRequest{UserID: 2, Name: "bob"})
	if err != nil {
		t.Fatalf("second join: %v", err)
	}
	if roomB != roomA {
		t.Fatalf("second player landed in room %d, want %d", roomB, roomA)
	}
	if sideB != game.Black {
		t.Fatalf("second player side = %v, want black", sideB)
	}
	view, _ = s.View(roomA)
	if view.Status != StatusReady {
		t.Fatalf("room status after both players sat down = %q, want ready", view.Status)
	}
	if len(view.LegalFrom) != 0 {
		t.Errorf("a room that has not started must not publish legal moves")
	}

	// One player pressing 准备 is not enough.
	out, err := s.Ready(ctx, roomA, 1)
	if err != nil {
		t.Fatalf("red ready: %v", err)
	}
	if out.Started {
		t.Fatalf("the game started with only one player ready")
	}
	view, _ = s.View(roomA)
	if !view.Red.Ready || view.Black.Ready {
		t.Fatalf("ready flags = red %v / black %v, want red only", view.Red.Ready, view.Black.Ready)
	}
	if _, err := s.SubmitMove(ctx, roomA, 1, "h7", "e7"); !errors.Is(err, ErrGameNotStarted) {
		t.Fatalf("a move before both players were ready: got %v, want ErrGameNotStarted", err)
	}

	// The second press starts it.
	out, err = s.Ready(ctx, roomA, 2)
	if err != nil {
		t.Fatalf("black ready: %v", err)
	}
	if !out.Started || out.Restarted {
		t.Fatalf("second 准备 = %+v, want a first game starting", out)
	}
	view, _ = s.View(roomA)
	if view.Status != StatusPlaying {
		t.Fatalf("room status = %q, want playing", view.Status)
	}
	if view.Red.Ready || view.Black.Ready {
		t.Errorf("the ready flags must be cleared once the game starts")
	}
	if view.Turn != "red" {
		t.Fatalf("turn = %q, want red", view.Turn)
	}
	if len(view.LegalFrom) == 0 {
		t.Fatalf("a playing room should publish legal moves")
	}
}

// TestRejoinReturnsToTheSameSeat checks a player cannot be given two games.
func TestRejoinReturnsToTheSameSeat(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	roomA, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	roomB, side, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	if roomB != roomA || side != game.Red {
		t.Fatalf("rejoin returned room=%d side=%v, want room=%d side=red", roomB, side, roomA)
	}
	if len(s.RoomIDs()) != 1 {
		t.Fatalf("expected exactly one room, got %d", len(s.RoomIDs()))
	}
}

// TestJoinSpecificRoom covers taking a named seat and rejecting a full room.
func TestJoinSpecificRoom(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	roomID, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 0, Name: "guest", RoomID: roomID}); err != nil {
		// A guest has no user id, which the session treats as "not a seat
		// holder"; it must still be able to take the free seat.
		t.Logf("guest join returned %v", err)
	}

	if _, _, err := s.Join(ctx, JoinRequest{UserID: 2, Name: "bob", RoomID: roomID}); err != nil {
		t.Fatalf("second seat: %v", err)
	}
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 3, Name: "carol", RoomID: roomID}); !errors.Is(err, ErrRoomFull) {
		t.Fatalf("third join: got %v, want ErrRoomFull", err)
	}
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 3, Name: "carol", RoomID: 999}); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("unknown room: got %v, want ErrRoomNotFound", err)
	}
}

// TestMoveFlowAndNotation plays the central cannon opening and checks the
// recorded notation and the turn handover.
func TestMoveFlowAndNotation(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	out, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7")
	if err != nil {
		t.Fatalf("red move: %v", err)
	}
	if out.Record.Notation != "炮二平五" {
		t.Errorf("notation = %q, want 炮二平五", out.Record.Notation)
	}
	if out.Record.Side != "red" {
		t.Errorf("side = %q, want red", out.Record.Side)
	}
	if out.GameOver {
		t.Fatalf("the opening should not end the game")
	}

	view, _ := s.View(roomID)
	if view.Turn != "black" {
		t.Errorf("turn = %q, want black", view.Turn)
	}
	if len(view.Moves) != 1 {
		t.Fatalf("move list length = %d, want 1", len(view.Moves))
	}
	if view.Moves[0].From != "h7" || view.Moves[0].To != "e7" {
		t.Errorf("recorded move = %s%s, want h7e7", view.Moves[0].From, view.Moves[0].To)
	}

	// Black replies, then it is Red's turn again.
	if _, err := s.SubmitMove(ctx, roomID, 2, "h2", "h6"); err != nil {
		t.Fatalf("black move: %v", err)
	}
	view, _ = s.View(roomID)
	if view.Turn != "red" {
		t.Errorf("turn after two plies = %q, want red", view.Turn)
	}
}

// TestMoveRejections covers the error paths a client can trigger.
func TestMoveRejections(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	if _, err := s.SubmitMove(ctx, roomID, 2, "a0", "a1"); !errors.Is(err, ErrNotYourTurn) {
		t.Errorf("black moving first: got %v, want ErrNotYourTurn", err)
	}
	if _, err := s.SubmitMove(ctx, roomID, 9, "a9", "a8"); !errors.Is(err, ErrNotASeat) {
		t.Errorf("stranger moving: got %v, want ErrNotASeat", err)
	}
	// A rook cannot slide through its own pawn: a9-a4 would pass a6.
	if _, err := s.SubmitMove(ctx, roomID, 1, "a9", "a4"); !errors.Is(err, game.ErrIllegalMove) {
		t.Errorf("illegal move: got %v, want ErrIllegalMove", err)
	}
	// The horse on b9 cannot reach b7, which is not a 日 move.
	if _, err := s.SubmitMove(ctx, roomID, 1, "b9", "b7"); !errors.Is(err, game.ErrIllegalMove) {
		t.Errorf("illegal horse move: got %v, want ErrIllegalMove", err)
	}
	if _, err := s.SubmitMove(ctx, roomID, 1, "e5", "e4"); !errors.Is(err, game.ErrNoSuchPiece) {
		t.Errorf("moving from an empty square: got %v, want ErrNoSuchPiece", err)
	}
	// Moving the opponent's piece is reported as the wrong side.
	if _, err := s.SubmitMove(ctx, roomID, 1, "a0", "a1"); !errors.Is(err, game.ErrWrongSide) {
		t.Errorf("moving the opponent's piece: got %v, want ErrWrongSide", err)
	}
	if _, err := s.SubmitMove(ctx, 999, 1, "a9", "a8"); !errors.Is(err, ErrRoomNotFound) {
		t.Errorf("unknown room: got %v, want ErrRoomNotFound", err)
	}
	// The room must be untouched by every rejection above.
	view, _ := s.View(roomID)
	if view.Turn != "red" || len(view.Moves) != 0 {
		t.Fatalf("rejected moves changed the room: turn=%s moves=%d", view.Turn, len(view.Moves))
	}
}

// TestResignEndsTheGame checks the result and the room status.
func TestResignEndsTheGame(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	res, err := s.Resign(ctx, roomID, 1)
	if err != nil {
		t.Fatalf("resign: %v", err)
	}
	if res.Outcome != string(game.OutcomeBlackWin) {
		t.Errorf("outcome = %q, want %q", res.Outcome, game.OutcomeBlackWin)
	}
	if res.WinnerSide != "black" {
		t.Errorf("winner = %q, want black", res.WinnerSide)
	}
	if res.Reason != string(game.ReasonResign) {
		t.Errorf("reason = %q, want resign", res.Reason)
	}
	view, _ := s.View(roomID)
	if view.Status != StatusFinished {
		t.Errorf("status = %q, want finished", view.Status)
	}
	if _, err := s.SubmitMove(ctx, roomID, 2, "a3", "a4"); !errors.Is(err, ErrGameFinished) {
		t.Errorf("moving after the game ended: got %v, want ErrGameFinished", err)
	}
	if _, err := s.Resign(ctx, roomID, 2); !errors.Is(err, ErrGameFinished) {
		t.Errorf("second resign: got %v, want ErrGameFinished", err)
	}
}

// TestTurnClockExpires checks the per-move clock and the timeout result.
func TestTurnClockExpires(t *testing.T) {
	s, clock := newTestSession(t)
	ctx := context.Background()
	// Seating both players arms the clock for Red, who moves first.
	mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	if results := s.ExpireTimeouts(ctx); len(results) != 0 {
		t.Fatalf("nothing should expire yet, got %d", len(results))
	}

	clock.Advance(61 * time.Second)
	results := s.ExpireTimeouts(ctx)
	if len(results) != 1 {
		t.Fatalf("expected one timeout, got %d", len(results))
	}
	// Red was to play, so Black wins. The room id must be reported alongside the
	// result, because the result's GameID is the persisted game id, not the room.
	if results[0].RoomID == 0 {
		t.Errorf("the expired room id was not reported")
	}
	if results[0].Result == nil {
		t.Fatalf("the expired game has no result")
	}
	if results[0].Result.WinnerSide != "black" {
		t.Errorf("winner = %q, want black", results[0].Result.WinnerSide)
	}
	if results[0].Result.Reason != string(game.ReasonTimeout) {
		t.Errorf("reason = %q, want timeout", results[0].Result.Reason)
	}
	// The snapshot must also carry the outcome so a reconnecting client learns
	// the result even if it missed the one-off frame.
	view, ok := s.View(results[0].RoomID)
	if !ok {
		t.Fatalf("the expired room vanished")
	}
	if view.Result == nil {
		t.Fatalf("the finished room snapshot has no result")
	}
	if view.Result.WinnerSide != "black" || view.Result.Reason != string(game.ReasonTimeout) {
		t.Errorf("snapshot result = %+v, want a black win by timeout", view.Result)
	}
	// A second sweep must not double-report.
	if results := s.ExpireTimeouts(ctx); len(results) != 0 {
		t.Fatalf("timeout reported twice")
	}
}

// TestUnlimitedClocksNeverExpire pins the shipped default: with both clocks left
// at zero (unlimited), no deadline is armed, the snapshot reports msLeft 0, and
// no amount of elapsed time can end the game on time.
func TestUnlimitedClocksNeverExpire(t *testing.T) {
	clock := &fakeClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	rated := true
	// TurnTimeout and GameTimeout are deliberately absent: zero is the default
	// and must mean "unlimited", not "fall back to 60s / 20m".
	s := NewSession(Config{
		AIBudget:     50 * time.Millisecond,
		RatedDefault: &rated,
		Now:          clock.Now,
	}, nil)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	view, ok := s.View(roomID)
	if !ok {
		t.Fatalf("room %d vanished", roomID)
	}
	if view.Red.MSLeft != 0 || view.Black.MSLeft != 0 {
		t.Errorf("msLeft = red %d / black %d, want 0 (unlimited) for both",
			view.Red.MSLeft, view.Black.MSLeft)
	}
	if r := s.rooms[roomID]; !r.Red.TurnDeadline.IsZero() || !r.Black.TurnDeadline.IsZero() {
		t.Errorf("a move deadline was armed although the clock is unlimited")
	}

	// Even after an hour of sitting on the move, nothing expires and the game is
	// still playable.
	clock.Advance(time.Hour)
	if expired := s.ExpireTimeouts(ctx); len(expired) != 0 {
		t.Fatalf("a game with unlimited clocks expired: %+v", expired)
	}
	if _, err := s.SubmitMove(ctx, roomID, 1, "b7", "e7"); err != nil {
		t.Fatalf("a move with unlimited clocks was rejected: %v", err)
	}
}

// TestDrawOfferFlow covers offering, declining and accepting a draw.
func TestDrawOfferFlow(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	side, _, err := s.OfferDraw(ctx, roomID, 1)
	if err != nil {
		t.Fatalf("offer draw: %v", err)
	}
	if side != game.Red {
		t.Errorf("offer by = %v, want red", side)
	}

	// The offerer cannot accept their own offer.
	if _, err := s.RespondDraw(ctx, roomID, 1, true); err == nil {
		t.Errorf("accepting your own offer should fail")
	}
	// Declining clears it and leaves the game running.
	if _, err := s.RespondDraw(ctx, roomID, 2, false); err != nil {
		t.Fatalf("decline draw: %v", err)
	}
	view, _ := s.View(roomID)
	if view.DrawOfferedBy != "" || view.Status != StatusPlaying {
		t.Fatalf("after a decline: offeredBy=%q status=%q", view.DrawOfferedBy, view.Status)
	}
	if _, err := s.RespondDraw(ctx, roomID, 2, true); !errors.Is(err, ErrNothingPending) {
		t.Errorf("responding with no offer: got %v, want ErrNothingPending", err)
	}

	// Now accept for real.
	if _, _, err := s.OfferDraw(ctx, roomID, 2); err != nil {
		t.Fatalf("second offer: %v", err)
	}
	res, err := s.RespondDraw(ctx, roomID, 1, true)
	if err != nil {
		t.Fatalf("accept draw: %v", err)
	}
	if res.Outcome != string(game.OutcomeDraw) {
		t.Errorf("outcome = %q, want draw", res.Outcome)
	}
	view, _ = s.View(roomID)
	if view.Status != StatusFinished {
		t.Errorf("status = %q, want finished", view.Status)
	}
}

// TestTakeback rewinds a ply when the opponent agrees.
func TestTakeback(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("red move: %v", err)
	}
	if _, _, err := s.RequestTakeback(roomID, 2); err != nil {
		t.Fatalf("request takeback: %v", err)
	}
	view, _ := s.View(roomID)
	if view.TakebackRequestedBy != "black" {
		t.Fatalf("takebackRequestedBy = %q, want black", view.TakebackRequestedBy)
	}

	if _, err := s.RespondTakeback(roomID, 1, true); err != nil {
		t.Fatalf("accept takeback: %v", err)
	}
	view, _ = s.View(roomID)
	if len(view.Moves) != 0 {
		t.Fatalf("move list after takeback = %d, want 0", len(view.Moves))
	}
	if view.Turn != "red" {
		t.Fatalf("turn after takeback = %q, want red", view.Turn)
	}
	if view.FEN != game.StartFEN {
		t.Fatalf("position after takeback = %s, want the opening position", view.FEN)
	}
}

// TestPlayAgainstComputer checks the AI seat, the requested side and that the
// computer's move is applied through the same path.
func TestPlayAgainstComputer(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	roomID, side, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice", AILevel: ai.LevelEasy})
	if err != nil {
		t.Fatalf("join against the computer: %v", err)
	}
	if side != game.Red {
		t.Fatalf("default side against the computer = %v, want red", side)
	}
	view, _ := s.View(roomID)
	if !view.Black.IsAI {
		t.Fatalf("the black seat should be the computer: %+v", view.Black)
	}
	if !view.Black.Online {
		t.Errorf("the computer should always count as online")
	}
	if view.Status != StatusPlaying {
		t.Fatalf("status = %q, want playing", view.Status)
	}

	// The human moves, then the computer is to move.
	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("human move: %v", err)
	}
	level, ok := s.TakeAISeat(roomID)
	if !ok {
		t.Fatalf("the computer should be to move")
	}
	if level != ai.LevelEasy {
		t.Errorf("level = %v, want easy", level)
	}
	// A second claim must be refused while one search is in flight.
	if _, ok := s.TakeAISeat(roomID); ok {
		t.Errorf("a second AI claim should be refused")
	}

	// The transport would search here; the session only needs the move.
	g, err := game.NewGameFromFEN(mustFEN(t, s, roomID))
	if err != nil {
		t.Fatalf("rebuild position: %v", err)
	}
	move, found := ai.Search(g, level, 200*time.Millisecond)
	if !found {
		t.Fatalf("the computer found no move")
	}
	out, err := s.SubmitAIMove(ctx, roomID, move)
	if err != nil {
		t.Fatalf("submit computer move: %v", err)
	}
	if out.Record.Side != "black" {
		t.Errorf("computer move side = %q, want black", out.Record.Side)
	}
	view, _ = s.View(roomID)
	if view.Turn != "red" {
		t.Errorf("turn after the computer replied = %q, want red", view.Turn)
	}
	if len(view.Moves) != 2 {
		t.Errorf("move count = %d, want 2", len(view.Moves))
	}

	// Submitting the computer's move must release the claim, so the next
	// computer turn is claimable exactly once again.
	if _, err := s.SubmitMove(ctx, roomID, 1, "a9", "a8"); err != nil {
		t.Fatalf("second human move: %v", err)
	}
	if _, ok := s.TakeAISeat(roomID); !ok {
		t.Errorf("the next computer turn should be claimable again")
	}
	if _, ok := s.TakeAISeat(roomID); ok {
		t.Errorf("the claim must be granted only once per turn")
	}
}

// TestComputerCanPlayBlack checks the requested side is honoured.
func TestComputerCanPlayBlack(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	roomID, side, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice", AILevel: ai.LevelNormal, Side: "black"})
	if err != nil {
		t.Fatalf("join as black: %v", err)
	}
	if side != game.Black {
		t.Fatalf("side = %v, want black", side)
	}
	view, _ := s.View(roomID)
	if !view.Red.IsAI {
		t.Fatalf("the red seat should be the computer")
	}
	// Red, the computer, is to move first.
	if _, ok := s.TakeAISeat(roomID); !ok {
		t.Fatalf("the computer should be to move as Red")
	}
}

// TestReapFinishedDropsOldRooms checks finished rooms do not accumulate.
func TestReapFinishedDropsOldRooms(t *testing.T) {
	s, clock := newTestSession(t)
	ctx := context.Background()

	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	if _, err := s.Resign(ctx, roomID, 1); err != nil {
		t.Fatalf("resign: %v", err)
	}
	if n := s.ReapFinished(time.Hour); n != 0 {
		t.Fatalf("a fresh room should not be reaped, got %d", n)
	}
	clock.Advance(2 * time.Hour)
	if n := s.ReapFinished(time.Hour); n != 1 {
		t.Fatalf("reaped %d rooms, want 1", n)
	}
	if _, ok := s.View(roomID); ok {
		t.Fatalf("the room should be gone")
	}
}

// TestUpdateHookFiresOnChanges checks the transport is notified.
func TestUpdateHookFiresOnChanges(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	var seen []int64
	s.SetUpdateHook(func(ids []int64) { seen = append(seen, ids...) })

	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	if len(seen) == 0 {
		t.Fatalf("the update hook never fired while seating players")
	}
	seen = nil
	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if len(seen) == 0 {
		t.Fatalf("the update hook did not fire after a move")
	}
	for _, id := range seen {
		if id != roomID {
			t.Fatalf("hook reported room %d, want %d", id, roomID)
		}
	}
}

// TestDisconnectMarksSeatOffline checks presence tracking.
func TestDisconnectMarksSeatOffline(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	s.Disconnect(roomID, 2)
	view, _ := s.View(roomID)
	if view.Black.Online {
		t.Errorf("black should be offline after a disconnect")
	}
	if !view.Red.Online {
		t.Errorf("red should still be online")
	}
	// Reconnecting restores it.
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 2, Name: "bob"}); err != nil {
		t.Fatalf("rejoin: %v", err)
	}
	view, _ = s.View(roomID)
	if !view.Black.Online {
		t.Errorf("black should be online after reconnecting")
	}
}

// TestSnapshotReportsACannonCheckAndRefusesToIgnoreIt is the integration-level
// regression test for 送将: a cannon check whose screen stands far from the king
// must reach the client as `inCheck` (so the board warns the player) and must
// stop every move that does not answer it.
func TestSnapshotReportsACannonCheckAndRefusesToIgnoreIt(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	// Install the position directly: alice (red) is checked by the black cannon
	// on e4, with the red pawn on e7 as the only screen.
	g, err := game.NewGameFromFEN("3k5/9/9/9/4c4/9/9/4P4/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatalf("build position: %v", err)
	}
	s.mu.Lock()
	if r, ok := s.rooms[roomID]; ok {
		r.Game = g
		r.Moves = nil
	}
	s.mu.Unlock()

	view, _ := s.View(roomID)
	if !view.InCheck {
		t.Fatalf("the snapshot must report the cannon check on the red king")
	}
	if _, err := s.SubmitMove(ctx, roomID, 1, "e7", "e6"); !errors.Is(err, game.ErrIllegalMove) {
		t.Fatalf("a move that ignores the check: got %v, want ErrIllegalMove", err)
	}
	if _, err := s.SubmitMove(ctx, roomID, 1, "e9", "f9"); err != nil {
		t.Fatalf("the king stepping off the attacked file was refused: %v", err)
	}
	view, _ = s.View(roomID)
	if view.InCheck {
		t.Errorf("the check should be gone once the king stepped aside")
	}
}

// TestSnapshotPublishesLegalMoves checks the client can drive highlighting from
// the snapshot alone.
func TestSnapshotPublishesLegalMoves(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	view, _ := s.View(roomID)
	total := 0
	for _, dests := range view.LegalFrom {
		total += len(dests)
	}
	if total != 44 {
		t.Fatalf("legal move count in the snapshot = %d, want 44", total)
	}
	if dests := view.LegalFrom["a9"]; len(dests) != 2 {
		t.Errorf("rook a9 destinations = %v, want 2", dests)
	}
	if len(view.Moves) != 0 {
		t.Errorf("a fresh game should have no moves")
	}
	if view.Outcome != string(game.OutcomeOngoing) {
		t.Errorf("outcome = %q, want ongoing", view.Outcome)
	}
}

// TestRecorderReceivesGameEvents checks the persistence seam.
func TestRecorderReceivesGameEvents(t *testing.T) {
	rec := &fakeRecorder{}
	clock := &fakeClock{now: time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)}
	s := NewSession(Config{TurnTimeout: time.Minute, Now: clock.Now}, rec)
	ctx := context.Background()

	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	s.SetPersistedID(roomID, 77)

	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if len(rec.moves) != 1 {
		t.Fatalf("recorder saw %d moves, want 1", len(rec.moves))
	}
	if rec.moves[0].Move.Notation != "炮二平五" {
		t.Errorf("recorded notation = %q", rec.moves[0].Move.Notation)
	}
	if rec.moves[0].GameID != 77 {
		t.Errorf("recorded game id = %d, want 77", rec.moves[0].GameID)
	}

	if _, err := s.Resign(ctx, roomID, 1); err != nil {
		t.Fatalf("resign: %v", err)
	}
	if r, ok := s.RoomByID(roomID); ok {
		t.Logf("room after resign: rated=%v redIsAI=%v blackIsAI=%v redID=%d blackID=%d status=%s",
			r.Rated, r.Red.IsAI, r.Black.IsAI, r.Red.UserID, r.Black.UserID, r.Status)
	}
	t.Logf("recorder: moves=%d results=%d ratingCalls=%d", len(rec.moves), len(rec.results), len(rec.ratingCalls))
	if len(rec.results) != 1 {
		t.Fatalf("recorder saw %d results, want 1", len(rec.results))
	}
	if rec.results[0].WinnerSide != "black" {
		t.Errorf("recorded winner = %q, want black", rec.results[0].WinnerSide)
	}
	if rec.results[0].GameID != 77 {
		t.Errorf("result game id = %d, want 77", rec.results[0].GameID)
	}
	if len(rec.ratingCalls) != 1 {
		t.Fatalf("rating updates = %d, want 1", len(rec.ratingCalls))
	}
	// Black won, so red loses rating and black gains it.
	if rec.ratingCalls[0].redDelta >= 0 || rec.ratingCalls[0].blackDelta <= 0 {
		t.Errorf("rating deltas = %d/%d, want red negative and black positive",
			rec.ratingCalls[0].redDelta, rec.ratingCalls[0].blackDelta)
	}
}

// TestComputerGameDoesNotChangeRating checks rated games against the computer
// leave both accounts alone, while the finished game is still persisted.
func TestComputerGameDoesNotChangeRating(t *testing.T) {
	rec := &fakeRecorder{}
	s := NewSession(Config{TurnTimeout: time.Minute}, rec)
	ctx := context.Background()

	roomID, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice", AILevel: ai.LevelEasy})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	// A real room has a games row before it can record anything: the session
	// deliberately does not hand the recorder a result for game id zero.
	s.SetPersistedID(roomID, 42)

	res, err := s.Resign(ctx, roomID, 1)
	if err != nil {
		t.Fatalf("resign: %v", err)
	}
	if res == nil {
		t.Fatalf("resign returned no result")
	}
	if res.GameID != 42 {
		t.Errorf("result game id = %d, want 42", res.GameID)
	}
	if len(rec.ratingCalls) != 0 {
		t.Fatalf("a game against the computer must not change ratings, got %d updates", len(rec.ratingCalls))
	}
	if len(rec.results) != 1 {
		t.Fatalf("the result should still be recorded")
	}
	if rec.results[0].GameID != 42 {
		t.Errorf("recorded game id = %d, want 42", rec.results[0].GameID)
	}
	if rec.results[0].WinnerSide != "black" || rec.results[0].Reason != "resign" {
		t.Errorf("recorded result = %q/%q, want black/resign",
			rec.results[0].WinnerSide, rec.results[0].Reason)
	}
	// The room is rated, so ratings are suppressed by the computer seat rather
	// than by the game being casual.
	if !rec.results[0].Rated {
		t.Errorf("the room should be rated")
	}
}

// TestOpenRoomsListsWaitingGames checks the lobby view.
func TestOpenRoomsListsWaitingGames(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	if len(s.OpenRooms()) != 0 {
		t.Fatalf("a fresh session has no open rooms")
	}
	roomID, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	open := s.OpenRooms()
	if len(open) != 1 || open[0].ID != roomID {
		t.Fatalf("open rooms = %+v, want the one waiting room", open)
	}
	if open[0].HostName != "alice" || open[0].HostSide != "red" {
		t.Errorf("host = %q/%q, want alice/red", open[0].HostName, open[0].HostSide)
	}

	// Once an opponent arrives the room is no longer advertised.
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 2, Name: "bob"}); err != nil {
		t.Fatalf("second join: %v", err)
	}
	if len(s.OpenRooms()) != 0 {
		t.Fatalf("a full room must not be advertised: %+v", s.OpenRooms())
	}
}

// TestLeaveDissolvesAWaitingRoom covers cancelling a quick match: when the last
// seat leaves, the room goes with it, so no empty invitation stays in the lobby.
func TestLeaveDissolvesAWaitingRoom(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	roomID, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	out := s.Leave(ctx, roomID, 1)
	if !out.Dissolved {
		t.Fatalf("leaving the only seat must dissolve the room, got %+v", out)
	}
	if _, ok := s.View(roomID); ok {
		t.Fatalf("the dissolved room is still there")
	}
	if len(s.OpenRooms()) != 0 {
		t.Fatalf("a dissolved room is still advertised: %+v", s.OpenRooms())
	}
	// Leaving twice, or leaving a room that is gone, must be harmless.
	if out := s.Leave(ctx, roomID, 1); out.Dissolved {
		t.Errorf("leaving a vanished room reported a dissolve")
	}
	// The player is free to match again straight away.
	room2, side, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("rejoin after leaving: %v", err)
	}
	if room2 == roomID || side != game.Red {
		t.Fatalf("rejoin gave room %d as %v, want a new room as red", room2, side)
	}
}

// TestLeaveDuringAGameIsAnAbandonment checks the opponent is credited with the
// win, with a reason of its own, and that the seat is handed back.
func TestLeaveDuringAGameIsAnAbandonment(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("move: %v", err)
	}

	out := s.Leave(ctx, roomID, 1)
	if out.Result == nil {
		t.Fatalf("leaving a running game must produce a result")
	}
	if out.Result.WinnerSide != "black" || out.Result.Reason != string(game.ReasonAbandoned) {
		t.Fatalf("result = %+v, want a black win by abandonment", out.Result)
	}
	if out.Dissolved {
		t.Fatalf("the opponent is still in the room, which must not be dissolved")
	}
	view, ok := s.View(roomID)
	if !ok {
		t.Fatalf("the room vanished although the opponent is still there")
	}
	if view.Status != StatusFinished || view.Result == nil {
		t.Fatalf("status = %q result = %+v, want a finished room carrying the result", view.Status, view.Result)
	}
	if view.Red.UserID != 0 || view.Red.Name != "" {
		t.Errorf("the seat of the player who left was not freed: %+v", view.Red)
	}
	if view.Black.UserID != 2 || view.Black.Name != "bob" {
		t.Errorf("the opponent lost their seat: %+v", view.Black)
	}
	// The player who walked away can start over elsewhere.
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"}); err != nil {
		t.Fatalf("the player who left could not join again: %v", err)
	}
}

// TestLeaveBeforeTheFirstMoveFreesTheOpponent checks that a room whose game never
// started goes back to the lobby instead of pretending to be ready.
func TestLeaveBeforeTheFirstMoveFreesTheOpponent(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("alice join: %v", err)
	}
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 2, Name: "bob"}); err != nil {
		t.Fatalf("bob join: %v", err)
	}

	if out := s.Leave(ctx, roomID, 2); out.Dissolved {
		t.Fatalf("alice is still seated, so the room must survive")
	}
	view, _ := s.View(roomID)
	if view.Status != StatusWaiting {
		t.Fatalf("status = %q, want waiting", view.Status)
	}
	if view.Black.UserID != 0 || view.Black.Name != "" {
		t.Errorf("bob's seat was not freed: %+v", view.Black)
	}
	if len(s.OpenRooms()) != 1 {
		t.Fatalf("the room should be advertised for a new opponent: %+v", s.OpenRooms())
	}
}

// TestRematchKeepsTheRoomAndSwapsColours covers the end-of-game flow: both
// players stay in the room, both press 准备, and a fresh game starts in the same
// room with the colours swapped.
func TestRematchKeepsTheRoomAndSwapsColours(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := s.Resign(ctx, roomID, 2); err != nil {
		t.Fatalf("resign: %v", err)
	}
	s.SetPersistedID(roomID, 55)
	if id, ok := s.RoomIDForUser(1); !ok || id != roomID {
		t.Fatalf("a player lost the finished room: got %d/%v, want %d", id, ok, roomID)
	}

	// One 准备 is not enough, and the result stays on screen meanwhile.
	out, err := s.Ready(ctx, roomID, 1)
	if err != nil {
		t.Fatalf("red ready: %v", err)
	}
	if out.Started {
		t.Fatalf("one player cannot start a rematch")
	}
	view, _ := s.View(roomID)
	if view.Status != StatusFinished || view.Result == nil {
		t.Fatalf("status = %q result = %+v, want the finished game until the rematch starts", view.Status, view.Result)
	}
	if !view.Red.Ready {
		t.Errorf("red's readiness was not published")
	}

	out, err = s.Ready(ctx, roomID, 2)
	if err != nil {
		t.Fatalf("black ready: %v", err)
	}
	if !out.Started || !out.Restarted {
		t.Fatalf("outcome = %+v, want a rematch starting", out)
	}

	view, _ = s.View(roomID)
	if view.Status != StatusPlaying {
		t.Fatalf("status = %q, want playing", view.Status)
	}
	if view.Result != nil {
		t.Errorf("the rematch still carries the previous result")
	}
	if len(view.Moves) != 0 {
		t.Errorf("the rematch kept %d plies from the previous game", len(view.Moves))
	}
	if view.FEN != game.StartFEN {
		t.Errorf("the rematch did not reset the board: %s", view.FEN)
	}
	if view.Red.UserID != 2 || view.Black.UserID != 1 {
		t.Fatalf("colours were not swapped: red=%d black=%d", view.Red.UserID, view.Black.UserID)
	}
	if view.Red.Name != "bob" || view.Black.Name != "alice" {
		t.Fatalf("the names did not follow the seats: red=%q black=%q", view.Red.Name, view.Black.Name)
	}
	if view.Red.Ready || view.Black.Ready {
		t.Errorf("the ready flags must be cleared by the rematch")
	}
	if len(view.LegalFrom) == 0 {
		t.Errorf("a restarted room must publish legal moves")
	}
	// A rematch is a new game row, not a continuation of the old one: the room
	// must go back to "no persisted game" so the transport creates a fresh row.
	if r, ok := s.RoomByID(roomID); !ok || r.PersistedID != 0 {
		t.Errorf("the rematch kept the previous game row: %+v", r)
	}
	// Bob is red now and moves first; Alice, now black, may not.
	if _, err := s.SubmitMove(ctx, roomID, 1, "a0", "a1"); !errors.Is(err, ErrNotYourTurn) {
		t.Errorf("the old red player moved first after the swap: %v", err)
	}
	if _, err := s.SubmitMove(ctx, roomID, 2, "h7", "e7"); err != nil {
		t.Errorf("the new red player could not move: %v", err)
	}
}

// TestReadyAgainstTheComputerIsARematch checks a solo game follows the same rule:
// the computer is always ready, so one press starts the next game.
func TestReadyAgainstTheComputerIsARematch(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	roomID, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice", AILevel: ai.LevelEasy})
	if err != nil {
		t.Fatalf("join against the computer: %v", err)
	}
	if _, err := s.Resign(ctx, roomID, 1); err != nil {
		t.Fatalf("resign: %v", err)
	}

	out, err := s.Ready(ctx, roomID, 1)
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if !out.Started || !out.Restarted {
		t.Fatalf("outcome = %+v, want the computer game to restart", out)
	}
	view, _ := s.View(roomID)
	// The seats swap, so the computer takes Red and therefore opens the game.
	if !view.Red.IsAI || view.Red.UserID != 0 {
		t.Fatalf("the computer should hold red after the swap: %+v", view.Red)
	}
	if view.Black.IsAI || view.Black.UserID != 1 {
		t.Fatalf("the human should hold black after the swap: %+v", view.Black)
	}
	if view.Status != StatusPlaying || len(view.Moves) != 0 {
		t.Fatalf("status = %q moves = %d, want a fresh game", view.Status, len(view.Moves))
	}
	if !view.Red.Ready {
		t.Errorf("the computer's seat must always report ready")
	}
	// The computer is to move first, so the hub can claim its turn immediately.
	if _, ok := s.TakeAISeat(roomID); !ok {
		t.Errorf("the computer's turn in the rematch could not be claimed")
	}
	// Pressing 准备 while a game is running is refused.
	if _, err := s.Ready(ctx, roomID, 1); !errors.Is(err, ErrGameInProgress) {
		t.Errorf("pressing 准备 during a game: got %v, want ErrGameInProgress", err)
	}
}

// TestReadyAfterTheOpponentLeftReopensTheRoom checks a player is never stuck in
// a finished room alone: pressing 准备 advertises it for a new opponent.
func TestReadyAfterTheOpponentLeftReopensTheRoom(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	if _, err := s.Resign(ctx, roomID, 2); err != nil {
		t.Fatalf("resign: %v", err)
	}
	if out := s.Leave(ctx, roomID, 2); out.Dissolved {
		t.Fatalf("alice is still seated, so the room must survive")
	}

	out, err := s.Ready(ctx, roomID, 1)
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if !out.Reopened || out.Started {
		t.Fatalf("outcome = %+v, want a reopened room", out)
	}
	view, _ := s.View(roomID)
	if view.Status != StatusWaiting {
		t.Fatalf("status = %q, want waiting", view.Status)
	}
	if view.Result != nil {
		t.Errorf("the previous result should be gone while a new opponent is awaited")
	}
	open := s.OpenRooms()
	if len(open) != 1 || open[0].ID != roomID {
		t.Fatalf("open rooms = %+v, want the reopened room", open)
	}

	// A newcomer takes the free seat; both players must accept again, but the
	// player who had already accepted keeps that acceptance.
	if _, side, err := s.Join(ctx, JoinRequest{UserID: 3, Name: "carol"}); err != nil {
		t.Fatalf("carol join: %v", err)
	} else if side != game.Black {
		t.Fatalf("carol side = %v, want black", side)
	}
	view, _ = s.View(roomID)
	if view.Status != StatusReady {
		t.Fatalf("status = %q, want ready", view.Status)
	}
	if !view.Red.Ready || view.Black.Ready {
		t.Fatalf("ready flags = red %v / black %v, want alice still ready", view.Red.Ready, view.Black.Ready)
	}
	mustStart(t, s, ctx, roomID, 1, 3)
	view, _ = s.View(roomID)
	if view.Red.UserID != 1 || view.Black.UserID != 3 {
		t.Fatalf("seats after the new pairing: red=%d black=%d", view.Red.UserID, view.Black.UserID)
	}
	if len(view.LegalFrom) == 0 {
		t.Errorf("the reopened room must publish legal moves once it starts")
	}
}

// TestQuickMatchDoesNotResumeAFinishedRoom checks the lobby's 快速匹配 button:
// asking for an opponent while an old game is over must produce a new room.
func TestQuickMatchDoesNotResumeAFinishedRoom(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	if _, err := s.Resign(ctx, roomID, 2); err != nil {
		t.Fatalf("resign: %v", err)
	}

	room2, side, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("quick match after a finished game: %v", err)
	}
	if room2 == roomID {
		t.Fatalf("a quick match resumed the finished room %d", roomID)
	}
	if side != game.Red {
		t.Errorf("side = %v, want red in a fresh room", side)
	}
	if id, ok := s.RoomIDForUser(1); !ok || id != room2 {
		t.Fatalf("the player is still indexed in room %d, want %d", id, room2)
	}
	// The old room is still there for its remaining player, who may reopen it.
	if out, err := s.Ready(ctx, roomID, 2); err != nil {
		t.Fatalf("bob ready in the room he stayed in: %v", err)
	} else if out.Started {
		t.Errorf("bob is alone in the finished room and cannot start a game")
	} else if !out.Reopened {
		t.Errorf("bob's 准备 should reopen the room for a new opponent: %+v", out)
	}
}

// ---- helpers ----

// mustRoom seats two human players and has both of them press 准备, which is
// what it takes for the pieces to move. It deliberately goes through Ready
// rather than poking the status: the rooms the other tests exercise must be
// reached the way a client reaches them.
func mustRoom(t *testing.T, s *Session, ctx context.Context, redID int64, redName string, blackID int64, blackName string) int64 {
	t.Helper()
	roomID, _, err := s.Join(ctx, JoinRequest{UserID: redID, Name: redName})
	if err != nil {
		t.Fatalf("seat red: %v", err)
	}
	if _, _, err := s.Join(ctx, JoinRequest{UserID: blackID, Name: blackName}); err != nil {
		t.Fatalf("seat black: %v", err)
	}
	mustStart(t, s, ctx, roomID, redID, blackID)
	return roomID
}

// mustStart has both players accept the game and fails when it does not start.
func mustStart(t *testing.T, s *Session, ctx context.Context, roomID int64, redID, blackID int64) {
	t.Helper()
	if _, err := s.Ready(ctx, roomID, redID); err != nil {
		t.Fatalf("red ready: %v", err)
	}
	out, err := s.Ready(ctx, roomID, blackID)
	if err != nil {
		t.Fatalf("black ready: %v", err)
	}
	if !out.Started {
		t.Fatalf("both players were ready but the game did not start")
	}
}

func mustFEN(t *testing.T, s *Session, roomID int64) string {
	t.Helper()
	view, ok := s.View(roomID)
	if !ok {
		t.Fatalf("room %d vanished", roomID)
	}
	return view.FEN
}

// recordedMove pairs a ply with the game it belongs to.
type recordedMove struct {
	GameID int64
	Move   MoveRecord
}

// ratingCall captures one rating exchange.
type ratingCall struct {
	redID, blackID       int64
	redDelta, blackDelta int
}

// fakeRecorder records what the session reported, so the persistence seam can
// be asserted without a database.
type fakeRecorder struct {
	moves       []recordedMove
	results     []Result
	ratingCalls []ratingCall
	truncations []truncateCall
}

// truncateCall records one TruncateMoves call.
type truncateCall struct {
	GameID  int64
	KeepPly int
	FEN     string
}

func (f *fakeRecorder) StartGame(ctx context.Context, gameID int64) error { return nil }

func (f *fakeRecorder) AddMove(ctx context.Context, gameID int64, m MoveRecord) {
	f.moves = append(f.moves, recordedMove{GameID: gameID, Move: m})
}

func (f *fakeRecorder) TruncateMoves(ctx context.Context, gameID int64, keepPly int, fen string) {
	f.truncations = append(f.truncations, truncateCall{GameID: gameID, KeepPly: keepPly, FEN: fen})
}

func (f *fakeRecorder) FinishGame(ctx context.Context, gameID int64, r Result) {
	f.results = append(f.results, r)
}

func (f *fakeRecorder) Finish(ctx context.Context, redID, blackID int64, redDelta, blackDelta int) {
	f.ratingCalls = append(f.ratingCalls, ratingCall{
		redID: redID, blackID: blackID, redDelta: redDelta, blackDelta: blackDelta,
	})
}
