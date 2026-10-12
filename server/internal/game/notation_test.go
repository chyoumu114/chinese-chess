package game

import "testing"

// File numbering used in the expected notation below: both sides count files
// from their own right, and the board columns already run left-to-right from
// each side's own viewpoint, so column a is file 一 and column i is file 九.
//
// The canonical check is the central-cannon opening: the cannon that starts on
// column h is Red's 炮二 and swinging it to column e is 炮二平五.

// TestChineseNotation pins the long algebraic Chinese notation for the most
// common opening moves. A rook or pawn advancing along a file names the
// distance travelled; horse, elephant, advisor and any sideways move name the
// destination file.
func TestChineseNotation(t *testing.T) {
	cases := []struct {
		from, to string
		want     string
	}{
		{"e6", "e5", "兵五进一"}, // central pawn advances one rank
		{"a6", "a5", "兵九进一"}, // edge pawn advances; column a is file 九
		{"i6", "i5", "兵一进一"}, // the far edge pawn is file 一
		{"b7", "e7", "炮八平五"}, // left cannon swings to the central file
		{"h7", "e7", "炮二平五"}, // right cannon reaches the same file
		{"h7", "h3", "炮二进四"}, // cannon advances four ranks
		{"b7", "b6", "炮八进一"}, // cannon advances one rank
		{"a9", "a8", "车九进一"}, // left rook advances one rank
		{"a9", "a7", "车九进二"}, // left rook advances two ranks
		{"i9", "i7", "车一进二"}, // right rook advances two ranks
		{"c9", "e7", "相七进五"}, // left elephant moves inward (names the file)
		{"g9", "e7", "相三进五"}, // right elephant moves inward
		{"b9", "c7", "马八进七"}, // left horse (names the destination file)
		{"h9", "g7", "马二进三"}, // right horse
		{"d9", "e8", "仕六进五"}, // left advisor to the palace centre
		{"f9", "e8", "仕四进五"}, // right advisor to the palace centre
		{"e9", "e8", "帅五进一"}, // king advances; the count is the distance
	}
	for _, c := range cases {
		g := NewGame()
		got, err := g.Notation(Move{From: sq(t, c.from), To: sq(t, c.to)})
		if err != nil {
			t.Errorf("Notation(%s%s): %v", c.from, c.to, err)
			continue
		}
		if got != c.want {
			t.Errorf("Notation(%s%s) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

// TestNotationMatchesKnownOpening checks a short opening sequence end to end,
// which is the strongest single guard on the file-numbering convention.
func TestNotationMatchesKnownOpening(t *testing.T) {
	g := NewGame()
	// 炮二平五 — the right-hand cannon swings to the central file.
	if rec := mustPlay(t, g, "h7", "e7"); rec.Notation != "炮二平五" {
		t.Errorf("h7e7 notation = %q, want 炮二平五", rec.Notation)
	}
	// 炮8平5 — Black mirrors it. For Black the columns run the other way, so
	// the mirrored move is the cannon that starts on column b.
	if rec := mustPlay(t, g, "b2", "e2"); rec.Notation != "炮2平5" && rec.Notation != "炮八平五" {
		t.Errorf("b2e2 notation = %q, want the mirrored central cannon", rec.Notation)
	}
	// 马八进七 — the left horse advances towards the seven file.
	if rec := mustPlay(t, g, "b9", "c7"); rec.Notation != "马八进七" {
		t.Errorf("b9c7 notation = %q, want 马八进七", rec.Notation)
	}
}

// TestNotationRetreat checks the 退 direction word for both sides, and that the
// two camps produce mirrored notation for mirrored moves.
func TestNotationRetreat(t *testing.T) {
	// Red rook retreats down the board.
	g := boardOf(t, "w", "R", "a6", "K", "i9", "k", "d0")
	got, err := g.Notation(Move{From: sq(t, "a6"), To: sq(t, "a7")})
	if err != nil {
		t.Fatalf("Notation: %v", err)
	}
	if got != "车九退一" {
		t.Errorf("red rook retreat = %q, want 车九退一", got)
	}

	// Black rook moves down the board. For Black, decreasing row is away from
	// its own side, so the direction word is 退, not 进.
	gb := boardOf(t, "b", "r", "a2", "k", "i0", "K", "d9")
	gotB, err := gb.Notation(Move{From: sq(t, "a2"), To: sq(t, "a1")})
	if err != nil {
		t.Fatalf("Notation: %v", err)
	}
	if gotB != "车九退一" {
		t.Errorf("black rook moving down the board = %q, want 车九退一", gotB)
	}
	// Advancing towards Red, which for Black means increasing row, is 进.
	gotB2, err := gb.Notation(Move{From: sq(t, "a2"), To: sq(t, "a3")})
	if err != nil {
		t.Fatalf("Notation: %v", err)
	}
	if gotB2 != "车九进一" {
		t.Errorf("black rook advancing = %q, want 车九进一", gotB2)
	}
}

// TestNotationDisambiguation covers the 前/后 qualifier when two identical
// pieces share a file. The qualifier replaces the origin file, so a rear rook
// moving sideways is written 后车平八 and never 车九平八.
func TestNotationDisambiguation(t *testing.T) {
	// Two Red rooks on the a-file (file 九): a7 is the front piece because it
	// is closer to Black. The Red king sits on i9 so the kings are not facing.
	g := boardOf(t, "w", "R", "a7", "R", "a9", "K", "i9", "k", "d0")
	got, err := g.Notation(Move{From: sq(t, "a9"), To: sq(t, "b9")})
	if err != nil {
		t.Fatalf("Notation: %v", err)
	}
	if got != "后车平八" {
		t.Errorf("rear rook = %q, want 后车平八", got)
	}
	got2, err := g.Notation(Move{From: sq(t, "a7"), To: sq(t, "b7")})
	if err != nil {
		t.Fatalf("Notation: %v", err)
	}
	if got2 != "前车平八" {
		t.Errorf("front rook = %q, want 前车平八", got2)
	}
}

// TestNotationForBlackDisambiguation checks the qualifier is reversed for Black,
// whose front piece is the one with the larger row number.
func TestNotationForBlackDisambiguation(t *testing.T) {
	// Two Black rooks on the a-file: a4 is the front piece from Black's view.
	g := boardOf(t, "b", "r", "a2", "r", "a4", "k", "i0", "K", "d9")
	got, err := g.Notation(Move{From: sq(t, "a4"), To: sq(t, "b4")})
	if err != nil {
		t.Fatalf("Notation: %v", err)
	}
	if got != "前车平八" {
		t.Errorf("black front rook = %q, want 前车平八", got)
	}
	got2, err := g.Notation(Move{From: sq(t, "a2"), To: sq(t, "b2")})
	if err != nil {
		t.Fatalf("Notation: %v", err)
	}
	if got2 != "后车平八" {
		t.Errorf("black rear rook = %q, want 后车平八", got2)
	}
}

// TestFileLabels pins the file numbering directly. Column i is file 一 and
// column a is file 九, for both camps.
func TestFileLabels(t *testing.T) {
	for col, want := range map[int]string{0: "九", 1: "八", 4: "五", 7: "二", 8: "一"} {
		if got := fileLabel(col, Red); got != want {
			t.Errorf("fileLabel(%d, Red) = %q, want %q", col, got, want)
		}
		if got := fileLabel(col, Black); got != want {
			t.Errorf("fileLabel(%d, Black) = %q, want %q", col, got, want)
		}
	}
}

// TestNotationForLegalMovesNeverPanics walks the whole legal move list and
// checks every move produces a non-empty notation.
func TestNotationForLegalMovesNeverPanics(t *testing.T) {
	g := NewGame()
	for _, m := range g.LegalMoves() {
		n, err := g.Notation(m)
		if err != nil {
			t.Fatalf("Notation(%s): %v", m, err)
		}
		if n == "" {
			t.Fatalf("empty notation for %s", m)
		}
	}
}
