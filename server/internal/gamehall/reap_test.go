package gamehall

import (
	"context"
	"testing"
	"time"
)

// TestReapWaitingDropsAbandonedRooms: someone opens a room and disappears. The
// sweep must collect it instead of leaving the lobby cluttered forever, and a
// room that is only a few minutes old must survive.
func TestReapWaitingDropsAbandonedRooms(t *testing.T) {
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	session := NewSession(Config{
		TurnTimeout: time.Minute,
		Now:         func() time.Time { return clock },
	}, nil)

	roomID, _, err := session.Join(context.Background(), JoinRequest{UserID: 1, Name: "red"})
	if err != nil {
		t.Fatalf("join: %v", err)
	}

	if n := session.ReapWaiting(2 * time.Hour); n != 0 {
		t.Fatalf("a fresh room must be kept, reaped %d", n)
	}

	clock = clock.Add(3 * time.Hour)
	if n := session.ReapWaiting(2 * time.Hour); n != 1 {
		t.Fatalf("the abandoned room should be reaped, got %d", n)
	}
	if _, ok := session.RoomByID(roomID); ok {
		t.Fatal("reaped room is still present")
	}
	if _, seated := session.RoomIDForUser(1); seated {
		t.Fatal("the user index must be cleared with the room")
	}
}

// TestReapWaitingLeavesRunningGamesAlone: the sweep only targets rooms that never
// started. A game in progress is owned by the clock and the abandon sweep.
func TestReapWaitingLeavesRunningGamesAlone(t *testing.T) {
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	session := NewSession(Config{
		TurnTimeout: time.Minute,
		Now:         func() time.Time { return clock },
	}, nil)

	ctx := context.Background()
	roomID, _, err := session.Join(ctx, JoinRequest{UserID: 1, Name: "red"})
	if err != nil {
		t.Fatalf("join red: %v", err)
	}
	if _, _, err := session.Join(ctx, JoinRequest{UserID: 2, Name: "black", RoomID: roomID}); err != nil {
		t.Fatalf("join black: %v", err)
	}
	if _, err := session.Ready(ctx, roomID, 1); err != nil {
		t.Fatalf("ready red: %v", err)
	}
	if _, err := session.Ready(ctx, roomID, 2); err != nil {
		t.Fatalf("ready black: %v", err)
	}

	clock = clock.Add(3 * time.Hour)
	if n := session.ReapWaiting(2 * time.Hour); n != 0 {
		t.Fatalf("a room in play must not be reaped, got %d", n)
	}
}
