package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"xiangqi/internal/ai"
	"xiangqi/internal/game"
	"xiangqi/internal/gamehall"
)

// newTestHub returns a hub wired to a memory-only session. The logger is
// silenced so a recovered error does not spam the test output.
func newTestHub(t *testing.T, session *gamehall.Session) *Hub {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHub(session, logger, nil)
}

// fakeClient builds a Client with a queue but no socket, which is enough to
// observe what the hub would have sent.
func fakeClient(hub *Hub, userID int64, name string) *Client {
	return &Client{
		hub:      hub,
		identity: Identity{UserID: userID, Name: name},
		send:     make(chan Envelope, 128),
		rooms:    make(map[int64]struct{}),
		closed:   make(chan struct{}),
	}
}

// drain collects the message types queued on a client, decoding room snapshots.
func drain(t *testing.T, c *Client, wait time.Duration) (types []string, rooms []gamehall.RoomView) {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case env := <-c.send:
			types = append(types, env.Type)
			if env.Type == TypeRoom {
				var view gamehall.RoomView
				if err := json.Unmarshal(env.Data, &view); err != nil {
					t.Fatalf("room payload does not decode: %v", err)
				}
				rooms = append(rooms, view)
			}
		case <-deadline:
			return types, rooms
		}
	}
}

// TestSessionUpdateDoesNotDeadlock is the regression test for a lock-order
// inversion: the session fired its update hook while holding the session lock,
// the hook took the hub lock, and the presence sweep took them in the opposite
// order. The result was a hard deadlock on the second player joining, which no
// engine or session test could see because it only appears when the transport
// is attached.
func TestSessionUpdateDoesNotDeadlock(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.runBroadcasts(ctx)

	alice := fakeClient(hub, 1, "alice")
	bob := fakeClient(hub, 2, "bob")
	hub.add(alice)
	hub.add(bob)

	// Every mutating call below fires the session's update hook. If the hook
	// blocks on the hub lock, this join never returns.
	done := make(chan int64, 1)
	go func() {
		id, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 1, Name: "alice"})
		if err != nil {
			t.Errorf("alice join: %v", err)
			done <- 0
			return
		}
		hub.attachRoom(alice, id)
		done <- id
	}()

	var roomID int64
	select {
	case roomID = <-done:
		if roomID == 0 {
			t.Fatal("alice could not be seated")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("seating the first player deadlocked")
	}

	// The second player joining is what triggered the deadlock in practice.
	seated := make(chan struct{})
	go func() {
		defer close(seated)
		id, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 2, Name: "bob"})
		if err != nil {
			t.Errorf("bob join: %v", err)
			return
		}
		hub.attachRoom(bob, id)
	}()
	select {
	case <-seated:
	case <-time.After(5 * time.Second):
		t.Fatal("seating the second player deadlocked: the session update hook is blocking on the hub lock")
	}

	// The presence sweep takes the hub lock and used to call into the session
	// while holding it, which is the other half of the cycle.
	presenceDone := make(chan PresenceView, 1)
	go func() { presenceDone <- hub.presenceView() }()
	select {
	case view := <-presenceDone:
		if len(view.Online) != 2 {
			t.Errorf("presence lists %d players, want 2: %+v", len(view.Online), view.Online)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the presence sweep deadlocked")
	}

	// Attach both watchers before the next broadcast, exactly as handleJoin does
	// for the joining player (attach, then send a snapshot directly). Waiting
	// for the broadcast before attaching would race: the session notifies while
	// seating, which can happen before the client is registered as a watcher.
	hub.attachRoom(alice, roomID)
	hub.attachRoom(bob, roomID)
	bob.enqueueRoomSnapshot(roomID)

	// The worker must also deliver subsequent updates to both watchers.
	if _, err := session.SubmitMove(ctx, roomID, 1, "b7", "e7"); err != nil {
		t.Fatalf("move: %v", err)
	}

	_, aliceRooms := drain(t, alice, 2*time.Second)
	_, bobRooms := drain(t, bob, 2*time.Second)
	if len(aliceRooms) == 0 || len(bobRooms) == 0 {
		t.Fatalf("both watchers should have a snapshot (alice %d, bob %d)", len(aliceRooms), len(bobRooms))
	}
	last := bobRooms[len(bobRooms)-1]
	if last.ID != roomID {
		t.Errorf("bob's snapshot is for room %d, want %d", last.ID, roomID)
	}
	if last.Status != gamehall.StatusPlaying {
		t.Errorf("bob's snapshot status = %q, want playing", last.Status)
	}
	// The move must be visible, proving the update reached the client.
	if len(last.Moves) == 0 {
		t.Errorf("bob's latest snapshot has no moves; the move broadcast did not arrive")
	}
}

// TestBroadcastCarriesPerClientSide checks the snapshot is personalised: each
// watcher is told which side it plays.
func TestBroadcastCarriesPerClientSide(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.runBroadcasts(ctx)

	alice := fakeClient(hub, 1, "alice")
	bob := fakeClient(hub, 2, "bob")
	hub.add(alice)
	hub.add(bob)

	roomID, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("alice join: %v", err)
	}
	hub.attachRoom(alice, roomID)
	if _, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 2, Name: "bob"}); err != nil {
		t.Fatalf("bob join: %v", err)
	}
	hub.attachRoom(bob, roomID)

	_, aliceRooms := drain(t, alice, 2*time.Second)
	_, bobRooms := drain(t, bob, 2*time.Second)
	if len(aliceRooms) == 0 || len(bobRooms) == 0 {
		t.Fatalf("both watchers should receive snapshots (alice %d, bob %d)", len(aliceRooms), len(bobRooms))
	}
	if got := aliceRooms[len(aliceRooms)-1].YouAre; got != "red" {
		t.Errorf("alice was told she plays %q, want red", got)
	}
	if got := bobRooms[len(bobRooms)-1].YouAre; got != "black" {
		t.Errorf("bob was told he plays %q, want black", got)
	}
}

// TestAIMoveIsScheduledForTheComputerSeat checks the hub claims the computer's
// turn exactly once and plays a move into the room.
func TestAIMoveIsScheduledForTheComputerSeat(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{
		TurnTimeout: time.Minute,
		AIBudget:    200 * time.Millisecond,
	}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.runBroadcasts(ctx)

	human := fakeClient(hub, 1, "alice")
	hub.add(human)

	roomID, side, err := session.Join(ctx, gamehall.JoinRequest{
		UserID: 1, Name: "alice", AILevel: ai.LevelEasy,
	})
	if err != nil {
		t.Fatalf("join against the computer: %v", err)
	}
	if side != 0 {
		t.Fatalf("the human should be red, got %v", side)
	}
	hub.attachRoom(human, roomID)

	// The human moves, which hands the turn to the computer.
	if _, err := session.SubmitMove(ctx, roomID, 1, "b7", "e7"); err != nil {
		t.Fatalf("human move: %v", err)
	}
	// The hub must be able to claim the computer's turn, exactly once.
	if _, ok := session.TakeAISeat(roomID); !ok {
		t.Fatalf("the hub could not claim the computer's turn")
	}
	if _, ok := session.TakeAISeat(roomID); ok {
		t.Fatalf("the computer's turn was claimed twice")
	}

	// Play a real reply for the computer and check the turn comes back.
	view, ok := session.View(roomID)
	if !ok {
		t.Fatalf("room vanished")
	}
	g, err := game.NewGameFromFEN(view.FEN)
	if err != nil {
		t.Fatalf("rebuild position: %v", err)
	}
	move, found := ai.Search(g, ai.LevelEasy, 300*time.Millisecond)
	if !found {
		t.Fatalf("the computer found no move")
	}
	out, err := session.SubmitAIMove(ctx, roomID, move)
	if err != nil {
		t.Fatalf("submit the computer's move (%s): %v", move.String(), err)
	}
	if out.Record.Side != "black" {
		t.Errorf("the computer's move was recorded for %q, want black", out.Record.Side)
	}
	view, _ = session.View(roomID)
	if view.Turn != "red" {
		t.Errorf("turn after the computer replied = %q, want red", view.Turn)
	}
	if len(view.Moves) != 2 {
		t.Errorf("move count = %d, want 2", len(view.Moves))
	}
	// After the computer's move the flag is released, so the next computer turn
	// can be claimed again once the human moves. Pick a move from the current
	// position rather than assuming one.
	view, _ = session.View(roomID)
	g, err = game.NewGameFromFEN(view.FEN)
	if err != nil {
		t.Fatalf("rebuild position: %v", err)
	}
	humanMoves := g.LegalMoves()
	if len(humanMoves) == 0 {
		t.Fatalf("the human has no legal move")
	}
	if _, err := session.SubmitMove(ctx, roomID, 1, humanMoves[0].FromName(), humanMoves[0].ToName()); err != nil {
		t.Fatalf("second human move (%s): %v", humanMoves[0].String(), err)
	}
	if _, ok := session.TakeAISeat(roomID); !ok {
		t.Errorf("the next computer turn could not be claimed")
	}
}
