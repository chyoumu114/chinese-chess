package game

// Move generation.
//
// PseudoLegalMoves returns moves that respect each piece's movement pattern but
// may leave the mover's own king in check (or expose it to the flying-general
// rule). LegalMoves filters those out. HasLegalMoves answers "is this side
// stalemated", which — unlike western chess — is a loss in xiangqi.

var (
	orthogonal = [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}}
	diagonal   = [4][2]int{{-1, -1}, {-1, 1}, {1, -1}, {1, 1}}
	// horseSteps pairs each horse destination offset with the "leg" square
	// that must be empty for the jump to be legal.
	horseSteps = [8]struct {
		dr, dc       int
		legDr, legDc int
	}{
		{-2, -1, -1, 0}, {-2, 1, -1, 0},
		{2, -1, 1, 0}, {2, 1, 1, 0},
		{-1, -2, 0, -1}, {1, -2, 0, -1},
		{-1, 2, 0, 1}, {1, 2, 0, 1},
	}
)

func (b *Board) onBoard(row, col int) bool {
	return row >= 0 && row < Rows && col >= 0 && col < Cols
}

func (b *Board) pieceAt(row, col int) Piece {
	if !b.onBoard(row, col) {
		return Piece{}
	}
	return b.squares[NewSquare(row, col)]
}

// PseudoLegalMoves returns every move the side to play could make ignoring
// king safety.
func (b *Board) PseudoLegalMoves() []Move {
	return b.movesFor(b.Turn)
}

// PseudoLegalMovesFor returns pseudo-legal moves for one side. It does not
// require that side to be to play, which the check detector relies on.
func (b *Board) PseudoLegalMovesFor(side Side) []Move { return b.movesFor(side) }

func (b *Board) movesFor(side Side) []Move {
	moves := make([]Move, 0, 48)
	for i := 0; i < Rows*Cols; i++ {
		sq := Square(i)
		p := b.squares[i]
		if p.Type == NoPiece || p.Side != side {
			continue
		}
		switch p.Type {
		case King:
			b.kingMoves(sq, p, &moves)
		case Advisor:
			b.stepMoves(sq, p, diagonal[:], &moves, func(to Square) bool { return inPalace(to, p.Side) })
		case Elephant:
			b.elephantMoves(sq, p, &moves)
		case Horse:
			b.horseMoves(sq, p, &moves)
		case Rook:
			b.rayMoves(sq, p, &moves, false)
		case Cannon:
			b.rayMoves(sq, p, &moves, true)
		case Pawn:
			b.pawnMoves(sq, p, &moves)
		}
	}
	return moves
}

// stepMoves emits single-square steps that satisfy the given predicate.
func (b *Board) stepMoves(sq Square, p Piece, dirs [][2]int, out *[]Move, allow func(Square) bool) {
	row, col := sq.Row(), sq.Col()
	for _, d := range dirs {
		nr, nc := row+d[0], col+d[1]
		if !b.onBoard(nr, nc) {
			continue
		}
		to := NewSquare(nr, nc)
		if allow != nil && !allow(to) {
			continue
		}
		if target := b.squares[to]; target.Type != NoPiece && target.Side == p.Side {
			continue
		}
		*out = append(*out, Move{From: sq, To: to})
	}
}

// kingMoves also implements the flying-general rule: a king may capture the
// opposing king along an open file. Play rejects that move as an immediate win,
// but generating it keeps the attack detector simple and correct.
func (b *Board) kingMoves(sq Square, p Piece, out *[]Move) {
	b.stepMoves(sq, p, orthogonal[:], out, func(to Square) bool { return inPalace(to, p.Side) })

	row, col := sq.Row(), sq.Col()
	step := -1
	if p.Side == Black {
		step = 1
	}
	for r := row + step; r >= 0 && r < Rows; r += step {
		target := b.squares[NewSquare(r, col)]
		if target.Type == NoPiece {
			continue
		}
		if target.Side != p.Side && target.Type == King {
			*out = append(*out, Move{From: sq, To: NewSquare(r, col)})
		}
		break
	}
}

// elephantMoves applies the 田 diagonal, the eye-block rule and the river ban.
func (b *Board) elephantMoves(sq Square, p Piece, out *[]Move) {
	row, col := sq.Row(), sq.Col()
	for _, d := range diagonal {
		nr, nc := row+2*d[0], col+2*d[1]
		if !b.onBoard(nr, nc) {
			continue
		}
		if b.pieceAt(row+d[0], col+d[1]).Type != NoPiece { // blocked eye
			continue
		}
		to := NewSquare(nr, nc)
		if !ownHalf(to, p.Side) { // elephants never cross the river
			continue
		}
		if target := b.squares[to]; target.Type != NoPiece && target.Side == p.Side {
			continue
		}
		*out = append(*out, Move{From: sq, To: to})
	}
}

// horseMoves applies the 马 leg-block rule.
func (b *Board) horseMoves(sq Square, p Piece, out *[]Move) {
	row, col := sq.Row(), sq.Col()
	for _, h := range horseSteps {
		nr, nc := row+h.dr, col+h.dc
		if !b.onBoard(nr, nc) {
			continue
		}
		if b.pieceAt(row+h.legDr, col+h.legDc).Type != NoPiece { // leg blocked
			continue
		}
		to := NewSquare(nr, nc)
		if target := b.squares[to]; target.Type != NoPiece && target.Side == p.Side {
			continue
		}
		*out = append(*out, Move{From: sq, To: to})
	}
}

// rayMoves slides along ranks and files. For a cannon the first piece met is a
// screen and only the next piece beyond it may be captured.
func (b *Board) rayMoves(sq Square, p Piece, out *[]Move, cannon bool) {
	row, col := sq.Row(), sq.Col()
	for _, d := range orthogonal {
		screen := false
		for nr, nc := row+d[0], col+d[1]; b.onBoard(nr, nc); nr, nc = nr+d[0], nc+d[1] {
			to := NewSquare(nr, nc)
			target := b.squares[to]
			if !cannon {
				if target.Type == NoPiece {
					*out = append(*out, Move{From: sq, To: to})
					continue
				}
				if target.Side != p.Side {
					*out = append(*out, Move{From: sq, To: to})
				}
				break
			}
			// Cannon.
			if !screen {
				if target.Type == NoPiece {
					*out = append(*out, Move{From: sq, To: to})
					continue
				}
				screen = true
				continue
			}
			if target.Type == NoPiece {
				continue
			}
			if target.Side != p.Side {
				*out = append(*out, Move{From: sq, To: to})
			}
			break
		}
	}
}

// pawnMoves: forward one always; sideways only after crossing the river.
// Pawns never move backwards.
func (b *Board) pawnMoves(sq Square, p Piece, out *[]Move) {
	forward := -1
	if p.Side == Black {
		forward = 1
	}
	dirs := [][2]int{{forward, 0}}
	if !ownHalf(sq, p.Side) { // crossed the river
		dirs = append(dirs, [2]int{0, -1}, [2]int{0, 1})
	}
	b.stepMoves(sq, p, dirs, out, nil)
}

// kingsFacing reports whether both kings share an open file, which is illegal.
func (b *Board) kingsFacing() bool {
	redSq, okRed := b.FindKing(Red)
	blackSq, okBlack := b.FindKing(Black)
	if !okRed || !okBlack || redSq.Col() != blackSq.Col() {
		return false
	}
	col := redSq.Col()
	lo, hi := blackSq.Row(), redSq.Row()
	if lo > hi {
		lo, hi = hi, lo
	}
	for row := lo + 1; row < hi; row++ {
		if b.squares[NewSquare(row, col)].Type != NoPiece {
			return false
		}
	}
	return true
}

// raysFrom walks outwards from a square in every orthogonal direction and
// reports whether attackingSide has a sliding piece bearing on it. Each
// direction is scanned independently and stops at the second piece it meets.
//
// Scanning one direction at a time matters: pieces on opposite sides of the
// target must never be combined into a single screen. Within one direction the
// first piece met can be a rook or king that attacks the square directly, or a
// screen behind which a cannon attacks; likewise a cannon of the attacking side
// standing first with a second attacking piece behind it attacks the square.
func (b *Board) raysFrom(row, col int, attacking Side) bool {
	for _, d := range orthogonal {
		var first, second Piece
		firstSq := Square(0)
		for nr, nc := row+d[0], col+d[1]; b.onBoard(nr, nc); nr, nc = nr+d[0], nc+d[1] {
			p := b.squares[NewSquare(nr, nc)]
			if p.Type == NoPiece {
				continue
			}
			if first.Type == NoPiece {
				first = p
				firstSq = NewSquare(nr, nc)
				continue
			}
			second = p
			break
		}

		if first.Side == attacking {
			switch first.Type {
			case Rook:
				return true
			case King:
				// The opposing king bears on an adjacent square; the
				// flying-general rule itself is handled separately.
				if abs(firstSq.Row()-row)+abs(firstSq.Col()-col) == 1 {
					return true
				}
			case Cannon:
				// A cannon attacks when exactly one piece separates it from the
				// target, and here the cannon itself is that first piece, so the
				// screen must be immediately next to the target with nothing in
				// between.
				if second.Type != NoPiece &&
					abs(firstSq.Row()-row)+abs(firstSq.Col()-col) == 2 {
					return true
				}
			case Pawn:
				if pawnAttacks(first, firstSq, NewSquare(row, col)) {
					return true
				}
			}
		}
		// A cannon of the attacking side standing immediately beyond a screen
		// also bears on the target: the screen is the first piece met and must
		// be adjacent to the target.
		if second.Side == attacking && second.Type == Cannon &&
			abs(firstSq.Row()-row)+abs(firstSq.Col()-col) == 1 {
			return true
		}
	}
	return false
}

// pawnAttacks reports whether a pawn standing on pawnSq attacks the square to.
func pawnAttacks(pawn Piece, pawnSq, to Square) bool {
	dr := to.Row() - pawnSq.Row()
	dc := to.Col() - pawnSq.Col()
	// Straight ahead: Red advances towards decreasing rows.
	forward := -1
	if pawn.Side == Black {
		forward = 1
	}
	if dc == 0 && dr == forward {
		return true
	}
	// Sideways attacks exist only after the river has been crossed, which the
	// pawn's own square decides.
	if dr == 0 && abs(dc) == 1 {
		return !ownHalf(pawnSq, pawn.Side)
	}
	return false
}

// IsAttacked reports whether any piece of bySide attacks the square.
func (b *Board) IsAttacked(sq Square, bySide Side) bool {
	row, col := sq.Row(), sq.Col()

	// Sliding attacks along ranks and files.
	if b.raysFrom(row, col, bySide) {
		return true
	}

	// Horse attacks. A horse stands at target-(dr,dc) to reach the target; the
	// leg that blocks that jump sits one step from the horse, and its offset is
	// half the jump, which is exactly (legDr, legDc).
	for _, h := range horseSteps {
		hr, hc := row-h.dr, col-h.dc // square the horse would stand on
		if !b.onBoard(hr, hc) {
			continue
		}
		p := b.squares[NewSquare(hr, hc)]
		if p.Side != bySide || p.Type != Horse {
			continue
		}
		legRow, legCol := hr+h.legDr, hc+h.legDc
		if b.pieceAt(legRow, legCol).Type == NoPiece {
			return true
		}
	}
	// Elephant and advisor attacks are irrelevant to king safety because a king
	// can never be on their reachable diagonal pattern in a legal position, but
	// they are included for completeness of the general "is this square
	// defended" question.
	for _, d := range diagonal {
		ar, ac := row+d[0], col+d[1]
		if b.onBoard(ar, ac) {
			if p := b.squares[NewSquare(ar, ac)]; p.Side == bySide && p.Type == Advisor {
				return true
			}
		}
		er, ec := row+2*d[0], col+2*d[1]
		if !b.onBoard(er, ec) {
			continue
		}
		if p := b.squares[NewSquare(er, ec)]; p.Side == bySide && p.Type == Elephant {
			if b.pieceAt(row+d[0], col+d[1]).Type == NoPiece {
				return true
			}
		}
	}
	return false
}

// InCheck reports whether the given side's king is currently attacked.
func (b *Board) InCheck(side Side) bool {
	sq, ok := b.FindKing(side)
	if !ok {
		return false
	}
	return b.IsAttacked(sq, side.Opponent())
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
