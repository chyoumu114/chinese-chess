package game

import "testing"

// TestPlayRejectsMovesThePieceCannotMake guards a bug where a move was checked
// only for king safety, so an arbitrary square pair reaching Play through the
// API could teleport a piece past a blocker. Play must agree with the generator.
func TestPlayRejectsMovesThePieceCannotMake(t *testing.T) {
	cases := []struct {
		name     string
		from, to string
	}{
		{"rook through its own pawn", "a9", "a4"},
		{"rook through its own pawn, far side", "i9", "i4"},
		{"horse making a straight move", "b9", "b7"},
		{"horse jumping its leg", "h9", "h7"},
		{"elephant across the river", "c9", "c5"},
		{"elephant off its diagonal", "c9", "d8"},
		{"advisor out of the diagonal", "d9", "d8"},
		{"king out of the palace", "e9", "e6"},
		{"pawn moving sideways before the river", "e6", "d6"},
		{"pawn moving backwards", "e6", "e7"},
		{"pawn two ranks", "e6", "e4"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := NewGame()
			_, err := g.Play(Move{From: sq(t, c.from), To: sq(t, c.to)})
			if err == nil {
				t.Fatalf("Play(%s%s) was accepted but the piece cannot make that move", c.from, c.to)
			}
			// The position must be untouched by a rejected move.
			if g.FEN() != StartFEN {
				t.Fatalf("a rejected move changed the position to %s", g.FEN())
			}
		})
	}
}

// TestCheckedSideMustAnswerTheCheck pins the 应将 rule: while a king is attacked
// only the moves that break the attack are legal. Any other move would leave the
// king in check (送将) and must be rejected, whichever piece makes it — including
// the piece that squares are traded for.
func TestCheckedSideMustAnswerTheCheck(t *testing.T) {
	cases := []struct {
		name string
		// fen is a position in which Red is to move and in check.
		fen string
		// ignore does not answer the check and must be rejected.
		ignore string
		// answer breaks the check and must be accepted.
		answer string
	}{
		{
			name:   "rook check down the file",
			fen:    "3k5/9/9/4r4/9/9/9/9/9/R3K4 w - - 0 1",
			ignore: "a9a3",
			answer: "e9f9",
		},
		{
			name:   "cannon check through a screen far from the king",
			fen:    "3k5/9/9/9/4c4/9/9/4P4/9/4K4 w - - 0 1",
			ignore: "e7e6",
			answer: "e9f9",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, err := NewGameFromFEN(c.fen)
			if err != nil {
				t.Fatalf("position %q is invalid: %v", c.fen, err)
			}
			if !g.InCheck() {
				t.Fatalf("Red should be in check in %s", c.fen)
			}
			if _, err := g.Play(Move{From: sq(t, c.ignore[:2]), To: sq(t, c.ignore[2:])}); err == nil {
				t.Errorf("%s leaves the king in check and must be rejected", c.ignore)
			}
			if _, err := g.Play(Move{From: sq(t, c.answer[:2]), To: sq(t, c.answer[2:])}); err != nil {
				t.Errorf("%s answers the check and must be accepted: %v", c.answer, err)
			}
		})
	}
}

// TestPlayAgreesWithLegalMoves checks every move the generator offers is
// accepted, and no other move is. This is the strongest statement of the
// invariant the bug above violated.
func TestPlayAgreesWithLegalMoves(t *testing.T) {
	g := NewGame()
	legal := g.LegalMoves()

	// Every generated move must be playable on a fresh game.
	for _, m := range legal {
		fresh := NewGame()
		if _, err := fresh.Play(m); err != nil {
			t.Errorf("generator offered %s but Play rejected it: %v", m, err)
		}
	}

	// Exhaustively try every square pair for Red and confirm Play accepts
	// exactly the generated set.
	accepted := map[string]bool{}
	for from := 0; from < Rows*Cols; from++ {
		for to := 0; to < Rows*Cols; to++ {
			if Square(from) == Square(to) {
				continue
			}
			fresh := NewGame()
			mv := Move{From: Square(from), To: Square(to)}
			if _, err := fresh.Play(mv); err == nil {
				accepted[mv.String()] = true
			}
		}
	}
	if len(accepted) != len(legal) {
		t.Fatalf("Play accepted %d moves but the generator offers %d", len(accepted), len(legal))
	}
	for _, m := range legal {
		if !accepted[m.String()] {
			t.Errorf("generator offers %s but Play rejected it", m)
		}
	}
}
