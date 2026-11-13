package gamehall

import (
	"context"
	"testing"
)

// TestClientCannotOptOutOfRating: a client that asks for an unrated room must
// still get a rated one, otherwise a loss can be dodged by sending rated=false.
func TestClientCannotOptOutOfRating(t *testing.T) {
	s, _ := newTestSession(t)
	unrated := false

	roomID, _, err := s.Join(context.Background(), JoinRequest{
		UserID: 1,
		Name:   "alice",
		Rated:  &unrated, // must be ignored
	})
	if err != nil {
		t.Fatalf("join: %v", err)
	}

	view, ok := s.View(roomID)
	if !ok {
		t.Fatal("room not found after join")
	}
	if !view.Rated {
		t.Fatal("客户端不该能把自己的对局标成不计分")
	}
}
