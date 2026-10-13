package game

import (
	"errors"
	"strings"
	"testing"
)

// Coordinates.
//
// A square is row*9+col. Row 0 is FEN rank 1 and holds Black's back rank, so
// Black occupies rows 0-4 with its palace on rows 0-2, and Red occupies rows
// 5-9 with its palace on rows 7-9. Red moves towards decreasing rows.
//
// Tests describe positions through boardOf, which takes (letter, square) pairs
// such as "R","a9" meaning a Red rook on square a9. That keeps every position
// readable while the engine speaks in rows and columns.

func sq(t *testing.T, name string) Square {
	t.Helper()
	s, err := ParseSquare(name)
	if err != nil {
		t.Fatalf("ParseSquare(%q): %v", name, err)
	}
	return s
}

// boardOf builds a position from (letter, square) pairs. It refuses positions
// that are not legal starting points for a test, which catches the most common
// way to write a misleading test: leaving the two kings facing each other down
// an open file.
func boardOf(t *testing.T, turn string, placements ...string) *Game {
	t.Helper()
	fen := fenFor(t, turn, placements...)
	b, err := ParseFEN(fen)
	if err != nil {
		t.Fatalf("generated FEN %q is invalid: %v", fen, err)
	}
	if b.kingsFacing() {
		t.Fatalf("position %q has the two kings facing on an open file, which is illegal", fen)
	}
	if _, ok := b.FindKing(Red); !ok {
		t.Fatalf("position %q has no Red king", fen)
	}
	if _, ok := b.FindKing(Black); !ok {
		t.Fatalf("position %q has no Black king", fen)
	}
	return &Game{board: b}
}

// fenFor renders a position as a FEN string.
func fenFor(t *testing.T, turn string, placements ...string) string {
	t.Helper()
	if len(placements)%2 != 0 {
		t.Fatalf("placements must be letter/square pairs, got %d values", len(placements))
	}
	grid := make([][]rune, Rows)
	for r := range grid {
		grid[r] = make([]rune, Cols)
		for c := range grid[r] {
			grid[r][c] = '1'
		}
	}
	for i := 0; i < len(placements); i += 2 {
		letter := rune(placements[i][0])
		if _, ok := pieceFor[letter]; !ok {
			t.Fatalf("unknown piece letter %q", placements[i])
		}
		s, err := ParseSquare(placements[i+1])
		if err != nil {
			t.Fatalf("bad square %q: %v", placements[i+1], err)
		}
		grid[s.Row()][s.Col()] = letter
	}
	ranks := make([]string, 0, Rows)
	for r := 0; r < Rows; r++ {
		var sb strings.Builder
		run := 0
		for c := 0; c < Cols; c++ {
			if grid[r][c] == '1' {
				run++
				continue
			}
			if run > 0 {
				sb.WriteByte(byte('0' + run))
				run = 0
			}
			sb.WriteRune(grid[r][c])
		}
		if run > 0 {
			sb.WriteByte(byte('0' + run))
		}
		ranks = append(ranks, sb.String())
	}
	return strings.Join(ranks, "/") + " " + turn + " - - 0 1"
}

// pieceFor lists the FEN letters accepted by boardOf.
var pieceFor = map[rune]string{
	'K': "king", 'A': "advisor", 'B': "elephant", 'N': "horse",
	'R': "rook", 'C': "cannon", 'P': "pawn", 'k': "king", 'a': "advisor",
	'b': "elephant", 'n': "horse", 'r': "rook", 'c': "cannon", 'p': "pawn",
}

func mustPlay(t *testing.T, g *Game, from, to string) MoveRecord {
	t.Helper()
	rec, err := g.Play(Move{From: sq(t, from), To: sq(t, to)})
	if err != nil {
		t.Fatalf("Play(%s%s): %v", from, to, err)
	}
	return rec
}

func dests(t *testing.T, g *Game, from string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, m := range g.LegalMovesFrom(sq(t, from)) {
		out[m.To.Name()] = true
	}
	return out
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func wantOnly(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("destination set = %v, want exactly %v", keys(got), want)
	}
	for _, w := range want {
		if !got[w] {
			t.Fatalf("destination set = %v, missing %s", keys(got), w)
		}
	}
}

// TestOpeningMoveCount pins the known xiangqi opening: 44 legal moves for Red.
func TestOpeningMoveCount(t *testing.T) {
	g := NewGame()
	if got := len(g.LegalMoves()); got != 44 {
		t.Fatalf("opening legal moves = %d, want 44", got)
	}
	if g.Turn() != Red {
		t.Fatalf("Red should move first, got %v", g.Turn())
	}
	if g.Over() {
		t.Fatalf("a fresh game must not be reported as over")
	}
	if g.Outcome() != OutcomeOngoing {
		t.Fatalf("fresh outcome = %q, want %q", g.Outcome(), OutcomeOngoing)
	}
}

// TestOpeningSymmetry confirms Black starts with the same number of legal moves.
func TestOpeningSymmetry(t *testing.T) {
	black, err := ParseFEN("rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR b - - 0 1")
	if err != nil {
		t.Fatalf("ParseFEN: %v", err)
	}
	g := &Game{board: black}
	if g.Turn() != Black {
		t.Fatalf("turn = %v, want black", g.Turn())
	}
	if got := len(g.LegalMoves()); got != 44 {
		t.Fatalf("black opening legal moves = %d, want 44", got)
	}
}

func TestStartFENRoundTrip(t *testing.T) {
	if got := NewBoard().FEN(); got != StartFEN {
		t.Fatalf("FEN round trip:\n got %s\nwant %s", got, StartFEN)
	}
	for _, bad := range []string{
		"",
		"9/9",
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR", // no side field
		"rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNX w - - 0 1",
	} {
		if _, err := ParseFEN(bad); err == nil {
			t.Fatalf("ParseFEN(%q) should fail", bad)
		}
	}
	for _, side := range []string{"w", "r", "red", "b", "black"} {
		if _, err := ParseFEN("4k4/9/9/9/9/9/9/9/9/4K4 " + side + " - - 0 1"); err != nil {
			t.Errorf("ParseFEN with side %q: %v", side, err)
		}
	}
}

// TestOpeningPiecesOnExpectedRanks pins the rank-to-row mapping independently
// of the rest of the suite.
func TestOpeningPiecesOnExpectedRanks(t *testing.T) {
	g := NewGame()
	for _, tc := range []struct {
		square string
		text   string
		side   Side
		kind   PieceType
	}{
		{"a9", "车", Red, Rook}, {"b9", "马", Red, Horse}, {"c9", "相", Red, Elephant},
		{"d9", "仕", Red, Advisor}, {"e9", "帅", Red, King}, {"b7", "炮", Red, Cannon},
		{"e6", "兵", Red, Pawn}, {"a0", "车", Black, Rook}, {"b0", "马", Black, Horse},
		{"c0", "象", Black, Elephant}, {"e0", "将", Black, King}, {"b2", "炮", Black, Cannon},
		{"e3", "卒", Black, Pawn},
	} {
		p := g.Board().At(sq(t, tc.square))
		if p.Side != tc.side || p.Type != tc.kind || p.Text() != tc.text {
			t.Errorf("%s holds %v/%v (%s), want %v/%v (%s)",
				tc.square, p.Side, p.Type, p.Text(), tc.side, tc.kind, tc.text)
		}
	}
}

// TestRookCannotJumpOwnPawn checks sliding is blocked by friendly pieces.
func TestRookCannotJumpOwnPawn(t *testing.T) {
	wantOnly(t, dests(t, NewGame(), "a9"), "a8", "a7")
}

// TestHorseLegBlock verifies the 蹩马腿 rule for the vertical leg.
func TestHorseLegBlock(t *testing.T) {
	// Red horse e6 with a friendly pawn on e7 blocking the two upward jumps.
	g := boardOf(t, "w", "N", "e6", "P", "e7", "K", "e9", "k", "d0")
	got := dests(t, g, "e6")
	if got["d8"] || got["f8"] {
		t.Errorf("horse reached %v although its vertical leg is blocked", keys(got))
	}
	for _, open := range []string{"c5", "c7", "d4", "f4"} {
		if !got[open] {
			t.Errorf("horse should still reach %s, got %v", open, keys(got))
		}
	}
}

// TestHorseLegBlockSideways checks the horizontal leg blocks the jumps whose
// leg is the occupied square.
func TestHorseLegBlockSideways(t *testing.T) {
	// A piece on d6 is the leg for the jumps towards c5 and c7.
	g := boardOf(t, "w", "N", "e6", "P", "d6", "K", "e9", "k", "d0")
	got := dests(t, g, "e6")
	if got["c5"] || got["c7"] {
		t.Errorf("horse reached %v although its leg on d6 is blocked", keys(got))
	}
	for _, open := range []string{"d4", "f4", "d8", "f8"} {
		if !got[open] {
			t.Errorf("horse should still reach %s, got %v", open, keys(got))
		}
	}
}

// TestElephantRules covers the river ban and the 塞象眼 block.
func TestElephantRules(t *testing.T) {
	// Opening: Red's elephant on c9 may only step to a7 and e7.
	wantOnly(t, dests(t, NewGame(), "c9"), "a7", "e7")

	// Blocked eye: a piece on d8 sits in the eye of the c9-e7 diagonal.
	g := boardOf(t, "w", "B", "c9", "P", "d8", "K", "e9", "k", "a0")
	if got := dests(t, g, "c9"); got["e7"] {
		t.Errorf("elephant jumped over the piece in its eye, got %v", keys(got))
	}

	// e7 -> a5/c5/g5 stay on Red's half and must be allowed.
	g2 := boardOf(t, "w", "B", "e7", "K", "e9", "k", "a0")
	for _, want := range []string{"c5", "g5"} {
		if got := dests(t, g2, "e7"); !got[want] {
			t.Errorf("elephant on e7 should reach %s, got %v", want, keys(got))
		}
	}

	// An elephant that has advanced to row 5 may not step across the river.
	g3 := boardOf(t, "w", "B", "e5", "K", "e9", "k", "a0")
	if got := dests(t, g3, "e5"); got["c3"] || got["g3"] {
		t.Errorf("elephant crossed the river, got %v", keys(got))
	}
}

// TestCannonNeedsScreen covers the 炮 capture and screen rules.
func TestCannonNeedsScreen(t *testing.T) {
	// Red cannon e9, a Black pawn on e6 as the screen and a Black rook on e5
	// behind it. The Red king on d9 is the only other piece on rank 9, and the
	// Black king sits on the far corner.
	g := boardOf(t, "w", "C", "e9", "p", "e6", "r", "e5", "K", "d9", "k", "i0")
	got := dests(t, g, "e9")
	if !got["e8"] || !got["e7"] {
		t.Errorf("cannon should slide to the empty e8 and e7, got %v", keys(got))
	}
	if got["e6"] {
		t.Errorf("cannon must not capture the screen on e6")
	}
	if !got["e5"] {
		t.Errorf("cannon should capture the rook behind the screen on e5, got %v", keys(got))
	}
	for _, blocked := range []string{"e4", "e3", "e2", "e1", "e0"} {
		if got[blocked] {
			t.Errorf("cannon reached %s past the screen, got %v", blocked, keys(got))
		}
	}
	// Sideways travel along rank 9 stops at the Red king on d9.
	for _, want := range []string{"f9", "g9", "h9", "i9"} {
		if !got[want] {
			t.Errorf("cannon should slide to %s, got %v", want, keys(got))
		}
	}
	if got["d9"] {
		t.Errorf("the cannon must not capture its own king")
	}
}

// TestCannonWithoutScreenCannotCapture checks a cannon needs a screen to take.
func TestCannonWithoutScreenCannotCapture(t *testing.T) {
	// Enemy rook on the same file with nothing between them. The Red king sits
	// on c9 so it is off both the e-file and the Black king's file.
	g := boardOf(t, "w", "C", "e9", "r", "e4", "K", "c9", "k", "i0")
	got := dests(t, g, "e9")
	if got["e4"] {
		t.Errorf("cannon captured without a screen")
	}
	if !got["e5"] || !got["e6"] || !got["e7"] || !got["e8"] {
		t.Errorf("cannon should slide along the empty file, got %v", keys(got))
	}
	if got["e3"] {
		t.Errorf("cannon jumped its first contact, got %v", keys(got))
	}
}

// TestPawnSidewaysOnlyAfterRiver covers the pawn rule change at the river.
func TestPawnSidewaysOnlyAfterRiver(t *testing.T) {
	// On Red's own half a Red pawn may only advance. The kings are on different
	// files so nothing else restricts the moves.
	wantOnly(t, dests(t, boardOf(t, "w", "P", "e7", "K", "d9", "k", "a0"), "e7"), "e6")
	wantOnly(t, dests(t, boardOf(t, "w", "P", "e5", "K", "d9", "k", "a0"), "e5"), "e4")

	// Across the river it gains the sideways steps but never moves backwards.
	wantOnly(t, dests(t, boardOf(t, "w", "P", "e3", "K", "d9", "k", "a0"), "e3"), "e2", "d3", "f3")

	// Black mirrors this: on its own half (rows 0-4) it only advances.
	wantOnly(t, dests(t, boardOf(t, "b", "p", "e3", "k", "d0", "K", "a9"), "e3"), "e4")
	// On e6 it has crossed the river, so it advances to e7 and steps sideways.
	wantOnly(t, dests(t, boardOf(t, "b", "p", "e6", "k", "d0", "K", "a9"), "e6"), "e7", "d6", "f6")

	// A Red pawn that has reached the last rank cannot advance off the board, so
	// it keeps only its sideways moves. The Black king is placed on the a-file
	// rather than on e0, so the pawn's forward square is empty.
	wantOnly(t, dests(t, boardOf(t, "w", "P", "e1", "K", "i9", "k", "a0"), "e1"), "d1", "f1", "e0")
}

// TestPalaceBounds checks kings and advisors never leave the palace. Every
// position below keeps the two kings off each other's file and leaves no king
// capturable, so the assertions describe ordinary movement only.
func TestPalaceBounds(t *testing.T) {
	// Red king in its palace centre. The Black king sits on the a-file. A king
	// steps orthogonally, so from e8 the four palace neighbours d8, f8, e7 and
	// e9 are reachable; d7 and f7 are diagonal and never one step away.
	wantOnly(t, dests(t, boardOf(t, "w", "K", "e8", "k", "a0"), "e8"), "d8", "f8", "e7", "e9")
	// With the Black king on the d-file, stepping to d8 would leave the kings
	// facing each other down file d, so that square must be excluded.
	if got := dests(t, boardOf(t, "w", "K", "e8", "k", "d0"), "e8"); got["d8"] {
		t.Errorf("king stepped onto the opposing king's file, got %v", keys(got))
	}
	// A king on the palace edge may not step sideways out of the palace: the
	// square left of d8 is c8, which is outside the palace, so only d9, d7 and
	// e8 remain.
	wantOnly(t, dests(t, boardOf(t, "w", "K", "d8", "k", "a0"), "d8"), "d7", "d9", "e8")
	// A Black king is confined to rows 0-2, so both e0 and e2 stay available.
	wantOnly(t, dests(t, boardOf(t, "b", "k", "e1", "K", "a9"), "e1"), "d1", "f1", "e0", "e2")
}

// TestAdvisorStaysInPalace checks advisors never leave the 3x3 palace. Each
// advisor is placed on a corner of the palace so it has at most one legal move,
// and the opposing king is far from that square so no king capture is on offer.
func TestAdvisorStaysInPalace(t *testing.T) {
	// The advisor on the bottom-left palace corner reaches only the centre.
	wantOnly(t, dests(t, boardOf(t, "w", "A", "d9", "K", "e9", "k", "i0"), "d9"), "e8")
	// The top-left palace corner has exactly one diagonal neighbour, the palace
	// centre e8, so it keeps that single legal move.
	wantOnly(t, dests(t, boardOf(t, "w", "A", "d7", "K", "e9", "k", "i0"), "d7"), "e8")
	// The same corner with the palace centre occupied by its own king has no
	// legal move at all: an advisor may never leave the palace.
	wantOnly(t, dests(t, boardOf(t, "w", "A", "d7", "K", "e8", "k", "i0"), "d7"))
	// The mirrored right-hand corners.
	wantOnly(t, dests(t, boardOf(t, "w", "A", "f9", "K", "e9", "k", "i0"), "f9"), "e8")
	// A Black advisor is confined to its own palace.
	wantOnly(t, dests(t, boardOf(t, "b", "a", "d2", "k", "e0", "K", "i9"), "d2"), "e1")
}

// TestFlyingGeneralForbidsExposedFile verifies a move that would leave the two
// kings facing each other down an open file is rejected.
func TestFlyingGeneralForbidsExposedFile(t *testing.T) {
	// Red king e9, Black king e0, Red rook e4 shielding the file.
	g := boardOf(t, "w", "R", "e4", "K", "e9", "k", "e0")
	if _, err := g.Play(Move{From: sq(t, "e4"), To: sq(t, "d4")}); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("moving the shielding rook off the file should be illegal, got %v", err)
	}
	// Moving the rook along the file keeps the shield and is legal.
	if _, err := g.Play(Move{From: sq(t, "e4"), To: sq(t, "e5")}); err != nil {
		t.Fatalf("moving the rook along the file should be legal, got %v", err)
	}
	// The king may step along the file as long as the rook still shields it.
	g3 := boardOf(t, "w", "R", "e4", "K", "e9", "k", "e0")
	if _, err := g3.Play(Move{From: sq(t, "e9"), To: sq(t, "e8")}); err != nil {
		t.Fatalf("stepping the king along the shielded file should be legal, got %v", err)
	}
	// The king may also step sideways while the shield holds, because d9 is not
	// the opposing king's file.
	if _, err := boardOf(t, "w", "R", "e4", "K", "e9", "k", "e0").Play(Move{From: sq(t, "e9"), To: sq(t, "d9")}); err != nil {
		t.Fatalf("stepping the king sideways while shielded should be legal, got %v", err)
	}
	// Stepping sideways onto the opposing king's open file would leave the two
	// kings facing down file d, which the flying-general rule forbids. The Red
	// rook on e4 keeps the starting position legal and file d is entirely open.
	g4 := boardOf(t, "w", "R", "e4", "K", "e9", "k", "d0")
	if _, err := g4.Play(Move{From: sq(t, "e9"), To: sq(t, "d9")}); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("stepping onto the opposing king's open file should be illegal, got %v", err)
	}

	// With nothing on the e-file the two kings already face each other. That
	// position is illegal, so boardOf refuses to build it and it is loaded
	// directly instead. A king move that keeps them facing is rejected...
	b, err := ParseFEN("4k4/9/9/9/9/9/9/9/9/4K4 w - - 0 1")
	if err != nil {
		t.Fatalf("ParseFEN: %v", err)
	}
	g2 := &Game{board: b}
	if _, err := g2.Play(Move{From: sq(t, "e9"), To: sq(t, "e8")}); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("a king move that leaves the kings facing should be illegal, got %v", err)
	}
	// ...while the generator still offers the flying-general capture, and
	// playing it captures the king and ends the game.
	found := false
	for _, m := range g2.PseudoLegalMovesFor(Red) {
		if m.From == sq(t, "e9") && m.To == sq(t, "e0") {
			found = true
		}
	}
	if !found {
		t.Errorf("the generator should offer the flying-general capture e9e0")
	}
	if _, err := g2.Play(Move{From: sq(t, "e9"), To: sq(t, "e0")}); err != nil {
		t.Fatalf("capturing the exposed king should be legal: %v", err)
	}
	if g2.Outcome() != OutcomeRedWin || g2.Reason() != ReasonKingCapture {
		t.Fatalf("outcome = %v/%v, want red win by king capture", g2.Outcome(), g2.Reason())
	}
}

// TestKingCannotMoveIntoCheck verifies the king may not step onto an attacked
// square.
func TestKingCannotMoveIntoCheck(t *testing.T) {
	// The Black rook on d0 controls the whole d-file. Red's rook on e4 shields
	// the e-file so the starting position is legal.
	g := boardOf(t, "w", "K", "e9", "R", "e4", "k", "e0", "r", "d0")
	if _, err := g.Play(Move{From: sq(t, "e9"), To: sq(t, "d9")}); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("king stepped onto an attacked file, got %v", err)
	}
	if _, err := g.Play(Move{From: sq(t, "e9"), To: sq(t, "e8")}); err != nil {
		t.Fatalf("king should be able to step to e8, got %v", err)
	}
}

// TestPinnedPieceCannotExposeKing verifies a shielded piece cannot move away
// when that would expose its own king.
func TestPinnedPieceCannotExposeKing(t *testing.T) {
	// Black rook e0, Red horse e4 pinned in front of the Red king on e9.
	g := boardOf(t, "w", "N", "e4", "K", "e9", "k", "a0", "r", "e0")
	got := dests(t, g, "e4")
	for _, off := range []string{"c3", "c5", "d2", "f2", "g3", "g5"} {
		if got[off] {
			t.Errorf("pinned horse moved off the pinning file to %s", off)
		}
	}
}

// TestTurnAndOriginValidation guards the error paths.
func TestTurnAndOriginValidation(t *testing.T) {
	g := NewGame()
	if _, err := g.Play(Move{From: sq(t, "a0"), To: sq(t, "a1")}); !errors.Is(err, ErrWrongSide) {
		t.Fatalf("moving a Black piece on Red's turn: got %v, want ErrWrongSide", err)
	}
	if _, err := g.Play(Move{From: sq(t, "e5"), To: sq(t, "e4")}); !errors.Is(err, ErrNoSuchPiece) {
		t.Fatalf("moving from an empty square: got %v, want ErrNoSuchPiece", err)
	}
	if _, err := g.Play(Move{From: sq(t, "a9"), To: sq(t, "e9")}); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("sliding a rook through its own pawn: got %v, want ErrIllegalMove", err)
	}
}

// TestCheckmateEndsGame plays a real mate and checks the recorded outcome.
func TestCheckmateEndsGame(t *testing.T) {
	// Black king e0. Red's rook on a0 bears along rank 0, so d0 is attacked
	// immediately and f0 is attacked the moment the king vacates e0. Red's rook
	// on c2 swings to e2 for check along the file: the king cannot take it (it
	// is two squares away), nothing can block, and its remaining palace squares
	// d0, f0 and e1 are all covered.
	g := boardOf(t, "w", "R", "a0", "R", "c2", "K", "d9", "k", "e0")
	if !g.HasLegalMove(Black) {
		t.Fatalf("the fixture must leave Black a reply before the mating move")
	}
	rec, err := g.Play(Move{From: sq(t, "c2"), To: sq(t, "e2")})
	if err != nil {
		t.Fatalf("mating move failed: %v", err)
	}
	if rec.Move.Captured != NoPiece {
		t.Fatalf("unexpected capture %v", rec.Move.Captured)
	}
	if g.HasLegalMove(Black) {
		t.Fatalf("the position is not mate: black could play %v", g.MarshalMoves(g.LegalMovesFor(Black)))
	}
	if !g.Over() {
		t.Fatalf("expected the game to be over after the mate")
	}
	if g.Outcome() != OutcomeRedWin || g.Reason() != ReasonCheckmate {
		t.Fatalf("outcome = %v/%v, want red win by checkmate", g.Outcome(), g.Reason())
	}
	if g.Status().Outcome != OutcomeRedWin {
		t.Fatalf("status outcome = %v", g.Status().Outcome)
	}
}

// TestCheckmateByRookPair checks the mate detector on a constructed position.
func TestCheckmateByRookPair(t *testing.T) {
	// Black is to move and already in check: the rook on e2 bears down the file
	// with e1 empty, while the rook on a0 covers d0 and, once the king leaves
	// e0, the square behind it on rank 0 as well.
	g := boardOf(t, "b", "R", "a0", "R", "e2", "K", "d9", "k", "e0")
	if !g.InCheck() {
		t.Fatalf("black should be in check from the rook on e2")
	}
	if g.HasLegalMove(Black) {
		t.Fatalf("black should have no legal reply, got %v", g.MarshalMoves(g.LegalMoves()))
	}
	if len(g.LegalMoves()) != 0 {
		t.Fatalf("legal move list should be empty in a mate position")
	}
}

// TestStalemateIsALoss confirms xiangqi scores stalemate as a loss.
func TestStalemateIsALoss(t *testing.T) {
	// Black king a0: it is not attacked, but a1 and b0 are both covered by the
	// rook on b8, and the board edge closes the rest.
	g := boardOf(t, "b", "k", "a0", "R", "b8", "R", "c9", "K", "e9")
	if g.InCheck() {
		t.Fatalf("black must not be in check in a stalemate position")
	}
	if len(g.LegalMoves()) != 0 {
		t.Fatalf("black should be stalemated, got %v", g.MarshalMoves(g.LegalMoves()))
	}
	// Drive the outcome through a real move: Red plays a waiting move and the
	// stalemate is then scored against Black.
	g2 := boardOf(t, "w", "k", "a0", "R", "b8", "R", "c9", "K", "e9")
	if _, err := g2.Play(Move{From: sq(t, "c9"), To: sq(t, "c8")}); err != nil {
		t.Fatalf("waiting move failed: %v", err)
	}
	if !g2.Over() {
		t.Fatalf("stalemate should end the game")
	}
	if g2.Outcome() != OutcomeRedWin || g2.Reason() != ReasonStalemate {
		t.Fatalf("stalemate outcome = %v/%v, want red win by stalemate", g2.Outcome(), g2.Reason())
	}
}

// TestKingCaptureEndsGame covers capturing the king as a terminal win.
func TestKingCaptureEndsGame(t *testing.T) {
	g := boardOf(t, "w", "R", "a9", "K", "e9", "k", "a0")
	if _, err := g.Play(Move{From: sq(t, "a9"), To: sq(t, "a0")}); err != nil {
		t.Fatalf("capturing the king should be accepted: %v", err)
	}
	if g.Outcome() != OutcomeRedWin || g.Reason() != ReasonKingCapture {
		t.Fatalf("outcome = %v/%v, want red win by king capture", g.Outcome(), g.Reason())
	}
}

func TestResignDrawAndTimeout(t *testing.T) {
	g := NewGame()
	if err := g.Resign(Red); err != nil {
		t.Fatalf("Resign: %v", err)
	}
	if g.Outcome() != OutcomeBlackWin || g.Reason() != ReasonResign {
		t.Fatalf("resign outcome = %v/%v", g.Outcome(), g.Reason())
	}
	if _, err := g.Play(Move{From: sq(t, "e6"), To: sq(t, "e5")}); !errors.Is(err, ErrGameOver) {
		t.Fatalf("playing after the game ended: got %v, want ErrGameOver", err)
	}
	if err := g.Resign(Black); !errors.Is(err, ErrGameOver) {
		t.Fatalf("second resign: got %v, want ErrGameOver", err)
	}

	d := NewGame()
	if err := d.Timeout(Black); err != nil {
		t.Fatalf("Timeout: %v", err)
	}
	if d.Outcome() != OutcomeRedWin || d.Reason() != ReasonTimeout {
		t.Fatalf("timeout outcome = %v/%v", d.Outcome(), d.Reason())
	}

	dr := NewGame()
	if err := dr.Draw(ReasonDrawAgreed); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if dr.Outcome() != OutcomeDraw || dr.Reason() != ReasonDrawAgreed {
		t.Fatalf("draw outcome = %v/%v", dr.Outcome(), dr.Reason())
	}
	if _, ok := dr.Winner(); ok {
		t.Fatalf("a draw must not report a winner")
	}
}

// TestUndoRestoresCapture checks a captured piece returns with its colour and
// that counters are restored. The c-file is used because Red's c-pawn can step
// to c5, Black's c-pawn answers on c4, and Red's pawn then captures it straight
// ahead. The capturing piece must be able to reach the occupied square in one
// move: a pawn (or a rook) walks the file, whereas an elephant moves two squares
// diagonally and can never travel c9-c4 along a file.
func TestUndoRestoresCapture(t *testing.T) {
	g := NewGame()
	mustPlay(t, g, "c6", "c5") // Red pawn forward
	mustPlay(t, g, "c3", "c4") // Black pawn forward, one square in front of it
	rec := mustPlay(t, g, "c5", "c4")
	if rec.Move.Captured != Pawn {
		t.Fatalf("captured = %v, want pawn", rec.Move.Captured)
	}
	fenAfter := g.FEN()

	if _, ok := g.Undo(); !ok {
		t.Fatalf("undo failed")
	}
	p := g.Board().At(sq(t, "c4"))
	if p.Type != Pawn || p.Side != Black {
		t.Fatalf("c4 after undo = %+v, want black pawn", p)
	}
	if r := g.Board().At(sq(t, "c5")); r.Type != Pawn || r.Side != Red {
		t.Fatalf("c5 after undo = %+v, want red pawn", r)
	}
	if g.FEN() == fenAfter {
		t.Fatalf("undo did not change the position")
	}
	if g.Over() {
		t.Fatalf("undo must clear the decided flag")
	}
	if g.Turn() != Red {
		t.Fatalf("turn after undo = %v, want red", g.Turn())
	}
	mustPlay(t, g, "c5", "c4")
	if g.FEN() != fenAfter {
		t.Fatalf("replay mismatch:\n got %s\nwant %s", g.FEN(), fenAfter)
	}
}

// TestUndoRestoresPosition compares an undone game against a reference game.
func TestUndoRestoresPosition(t *testing.T) {
	g := NewGame()
	mustPlay(t, g, "b7", "e7")
	mustPlay(t, g, "b2", "e2")
	mustPlay(t, g, "b9", "c7")
	if _, ok := g.Undo(); !ok {
		t.Fatalf("Undo reported failure")
	}

	ref := NewGame()
	mustPlay(t, ref, "b7", "e7")
	mustPlay(t, ref, "b2", "e2")
	if g.FEN() != ref.FEN() {
		t.Fatalf("after undo:\n got %s\nwant %s", g.FEN(), ref.FEN())
	}
	if len(g.History()) != 2 {
		t.Fatalf("history length after undo = %d, want 2", len(g.History()))
	}
}

// TestHistoryRecordsNotationAndFEN checks the move list the client renders.
func TestHistoryRecordsNotationAndFEN(t *testing.T) {
	g := NewGame()
	mustPlay(t, g, "h7", "e7")
	mustPlay(t, g, "h2", "h6")
	hist := g.History()
	if len(hist) != 2 {
		t.Fatalf("history length = %d, want 2", len(hist))
	}
	if hist[0].Notation != "炮二平五" {
		t.Errorf("ply 1 notation = %q, want 炮二平五", hist[0].Notation)
	}
	if hist[0].Side != Red || hist[1].Side != Black {
		t.Errorf("ply sides = %v/%v, want red/black", hist[0].Side, hist[1].Side)
	}
	if hist[1].FEN == "" || hist[1].Ply != 2 {
		t.Errorf("ply 2 record incomplete: %+v", hist[1])
	}
	if hist[0].Check {
		t.Errorf("the opening cannon move should not give check")
	}
	if got := len(g.Status().Notation); got != 2 {
		t.Errorf("status notation length = %d, want 2", got)
	}
	if g.Status().Turn != Red {
		t.Errorf("turn after two plies = %v, want red", g.Status().Turn)
	}
}

// TestIsAttackedBasics covers the detector used for check and pin logic.
func TestIsAttackedBasics(t *testing.T) {
	b := boardOf(t, "w", "R", "e4", "K", "i9", "k", "e0").Board()
	if !b.IsAttacked(sq(t, "e0"), Red) {
		t.Errorf("e0 should be attacked by the red rook on e4")
	}
	if b.IsAttacked(sq(t, "e4"), Red) {
		t.Errorf("a piece does not attack its own square")
	}
	if b.IsAttacked(sq(t, "d8"), Red) {
		t.Errorf("d8 should not be attacked by the red rook on e4")
	}

	// A horse on e4 attacks six squares but not the four orthogonally adjacent.
	hb := boardOf(t, "w", "N", "e4", "K", "i9", "k", "a0").Board()
	for _, hit := range []string{"d2", "f2", "c3", "c5", "g3", "g5"} {
		if !hb.IsAttacked(sq(t, hit), Red) {
			t.Errorf("horse on e4 should attack %s", hit)
		}
	}
	for _, miss := range []string{"d3", "e3", "e5", "d4", "f4"} {
		if hb.IsAttacked(sq(t, miss), Red) {
			t.Errorf("horse on e4 should not attack %s", miss)
		}
	}

	// A blocked leg removes the corresponding attacks.
	blocked := boardOf(t, "w", "N", "e4", "P", "e5", "K", "i9", "k", "a0").Board()
	if blocked.IsAttacked(sq(t, "d6"), Red) || blocked.IsAttacked(sq(t, "f6"), Red) {
		t.Errorf("a horse with a blocked leg must not attack d6/f6")
	}
	// One screen is enough for a cannon, and the screen itself is not attacked.
	// The Red king sits on d8 rather than d9 because a king defends the square
	// beside it, and from d9 it would make e9 "attacked" and hide what the
	// own-square assertion below is about.
	cb := boardOf(t, "w", "C", "e9", "p", "e6", "r", "e5", "K", "d8", "k", "i0").Board()
	if !cb.IsAttacked(sq(t, "e5"), Red) {
		t.Errorf("cannon on e9 should attack e5 through the screen on e6")
	}
	if cb.IsAttacked(sq(t, "e6"), Red) {
		t.Errorf("cannon must not attack its own screen")
	}
	if cb.IsAttacked(sq(t, "e9"), Red) {
		t.Errorf("a cannon does not attack its own square")
	}
	// The king really was the reason: a king does defend the square next to it,
	// so with the king back on d9 the probe square counts as attacked.
	king := boardOf(t, "w", "C", "e9", "p", "e6", "r", "e5", "K", "d9", "k", "i0").Board()
	if !king.IsAttacked(sq(t, "e9"), Red) {
		t.Errorf("the king on d9 defends the adjacent e9")
	}
	// A rook's attack is blocked by the first piece it meets, so the pawn on e6
	// stops it short of e5. e7 lies between the rook on e9 and that screen with
	// nothing in the way, so it is attacked.
	rb := boardOf(t, "w", "R", "e9", "p", "e6", "K", "d9", "k", "i0").Board()
	if !rb.IsAttacked(sq(t, "e7"), Red) {
		t.Errorf("the rook on e9 should attack e7, which is between it and the screen")
	}
	if rb.IsAttacked(sq(t, "e5"), Red) {
		t.Errorf("rook attack should be blocked by the pawn on e6")
	}
	// With the pawn on e6 as its only screen the cannon attacks every square
	// beyond that screen along the file, not only the one next to it: a cannon
	// jumps over exactly one piece and may stand any distance away.
	cb2 := boardOf(t, "w", "C", "e9", "p", "e6", "K", "d9", "k", "i0").Board()
	for _, hit := range []string{"e5", "e4"} {
		if !cb2.IsAttacked(sq(t, hit), Red) {
			t.Errorf("cannon on e9 should attack %s through the screen on e6", hit)
		}
	}
	// The screen itself and everything on the near side of it is not attacked:
	// no piece lies between the cannon and those squares.
	for _, miss := range []string{"e6", "e7", "e8"} {
		if cb2.IsAttacked(sq(t, miss), Red) {
			t.Errorf("cannon on e9 must not attack %s, which has no screen in front of it", miss)
		}
	}
}

// TestCannonCheckIsDetectedThroughADistantScreen is the regression test for a
// cannon check that the detector used to miss whenever the screen was not
// adjacent to the king. A missed check is what let the attacked side answer with
// a move that ignored it (送将), because the engine saw no check to resolve.
func TestCannonCheckIsDetectedThroughADistantScreen(t *testing.T) {
	// Black cannon e4, Red pawn e7 as the only screen, Red king e9.
	g := boardOf(t, "w", "c", "e4", "P", "e7", "K", "e9", "k", "d0")
	if !g.InCheck() {
		t.Fatalf("the Red king on e9 is in check from the cannon on e4 through the screen on e7")
	}
	legal := map[string]bool{}
	for _, m := range g.LegalMoves() {
		legal[m.String()] = true
	}
	if !legal["e9f9"] {
		t.Errorf("the king must be able to step off the attacked file, got %v", legal)
	}
	for mv := range legal {
		if mv != "e9f9" {
			t.Errorf("%s does not answer the check and must not be legal", mv)
		}
	}
	if _, err := g.Play(Move{From: sq(t, "e7"), To: sq(t, "e6")}); err == nil {
		t.Errorf("moving the screen sideways does not answer the check and must be rejected")
	}
}

// TestCannonWithNothingInBetweenDoesNotAttack guards the opposite error: a
// cannon whose line to the target holds no screen attacks nothing, so the target
// must not be reported as attacked. A false check refuses legal moves and can
// even announce a mate that does not exist.
func TestCannonWithNothingInBetweenDoesNotAttack(t *testing.T) {
	g := boardOf(t, "w", "c", "e7", "r", "e5", "K", "e9", "k", "d0")
	if g.InCheck() {
		t.Errorf("the Red king is not in check although a cannon and a rook share its file")
	}
	if g.Board().IsAttacked(sq(t, "e9"), Black) {
		t.Errorf("the Black cannon on e7 has no screen and cannot attack e9")
	}
}

// TestPawnAttackDetection checks the pawn attack pattern used for king safety,
// especially the sideways attack that only exists after crossing the river.
func TestPawnAttackDetection(t *testing.T) {
	// A Red pawn on e7 (own half) attacks e6 only. The Red king is off the file
	// so the probe is not blocked by it.
	p := boardOf(t, "w", "P", "e7", "K", "i9", "k", "a0").Board()
	if !p.IsAttacked(sq(t, "e6"), Red) {
		t.Errorf("pawn on e7 should attack e6")
	}
	if p.IsAttacked(sq(t, "d7"), Red) || p.IsAttacked(sq(t, "f7"), Red) {
		t.Errorf("a pawn that has not crossed the river must not attack sideways")
	}
	if p.IsAttacked(sq(t, "e8"), Red) {
		t.Errorf("a pawn never attacks backwards")
	}

	// Across the river on e3 it also attacks d3 and f3.
	q := boardOf(t, "w", "P", "e3", "K", "i9", "k", "a0").Board()
	if !q.IsAttacked(sq(t, "e2"), Red) {
		t.Errorf("pawn on e3 should attack e2")
	}
	if !q.IsAttacked(sq(t, "d3"), Red) || !q.IsAttacked(sq(t, "f3"), Red) {
		t.Errorf("a pawn across the river should attack sideways")
	}

	// A Black pawn mirrors this.
	r := boardOf(t, "b", "p", "e3", "k", "i0", "K", "a9").Board()
	if !r.IsAttacked(sq(t, "e4"), Black) {
		t.Errorf("black pawn on e3 should attack e4")
	}
	if r.IsAttacked(sq(t, "d3"), Black) || r.IsAttacked(sq(t, "f3"), Black) {
		t.Errorf("black pawn on its own half must not attack sideways")
	}
	s := boardOf(t, "b", "p", "e6", "k", "i0", "K", "a9").Board()
	if !s.IsAttacked(sq(t, "e7"), Black) {
		t.Errorf("black pawn on e6 should attack e7, its forward direction")
	}
	if !s.IsAttacked(sq(t, "d6"), Black) || !s.IsAttacked(sq(t, "f6"), Black) {
		t.Errorf("black pawn across the river should attack sideways")
	}
	if s.IsAttacked(sq(t, "e5"), Black) {
		t.Errorf("a black pawn never attacks backwards")
	}
}

func TestMoveParsing(t *testing.T) {
	for _, in := range []string{"h7e7", "h7-e7", " b7e7 "} {
		if _, err := ParseMove(in); err != nil {
			t.Errorf("ParseMove(%q): %v", in, err)
		}
	}
	for _, bad := range []string{"", "h2", "h2e", "z2e2", "j9e2", "h2e9x"} {
		if _, err := ParseMove(bad); err == nil {
			t.Errorf("ParseMove(%q) should fail", bad)
		}
	}
}

// TestPerftConsistency is a regression guard: the per-move node counts must add
// up to the total and stay in a plausible band.
func TestPerftConsistency(t *testing.T) {
	g := NewGame()
	div := g.PerftDivide(2)
	sum := 0
	for _, n := range div {
		sum += n
	}
	if sum != g.Perft(2) {
		t.Fatalf("perft divide sum %d != perft(2) %d", sum, g.Perft(2))
	}
	if len(div) != len(g.LegalMoves()) {
		t.Fatalf("divide covered %d moves, want %d", len(div), len(g.LegalMoves()))
	}
	for mv, n := range div {
		if n < 35 || n > 50 {
			t.Errorf("after %s black has %d replies, outside the plausible band", mv, n)
		}
	}
	if got := g.Perft(1); got != len(g.LegalMoves()) {
		t.Fatalf("perft(1) = %d, want %d", got, len(g.LegalMoves()))
	}
}
