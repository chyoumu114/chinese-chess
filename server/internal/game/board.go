package game

import (
	"fmt"
	"strings"
)

// Board geometry. A square is encoded as row*Cols + col.
//
//	row 0  = Black's back rank (top of the board)
//	row 9  = Red's back rank   (bottom of the board)
//
// Red therefore moves towards decreasing row numbers.
const (
	Rows = 10
	Cols = 9
)

// Square is a zero-based board coordinate.
type Square uint8

// NewSquare builds a Square from row and column.
func NewSquare(row, col int) Square { return Square(row*Cols + col) }

// Row returns the zero-based row of the square.
func (s Square) Row() int { return int(s) / Cols }

// Col returns the zero-based column of the square.
func (s Square) Col() int { return int(s) % Cols }

// Valid reports whether the square lies on the board.
func (s Square) Valid() bool { return int(s) < Rows*Cols }

// Name renders the square in algebraic form, e.g. "e0".
func (s Square) Name() string {
	return string(rune('a'+s.Col())) + string(rune('0'+s.Row()))
}

// ParseSquare is the inverse of Square.Name.
func ParseSquare(name string) (Square, error) {
	if len(name) != 2 {
		return 0, fmt.Errorf("game: bad square %q", name)
	}
	col := int(name[0] - 'a')
	row := int(name[1] - '0')
	if col < 0 || col >= Cols || row < 0 || row >= Rows {
		return 0, fmt.Errorf("game: square %q off board", name)
	}
	return NewSquare(row, col), nil
}

// Move is a single ply. Captured is informational and filled in by Play.
type Move struct {
	From      Square    `json:"-"`
	To        Square    `json:"-"`
	Promotion PieceType `json:"-"`
	Captured  PieceType `json:"-"`

	// Notated is the Chinese move notation, filled in by Play.
	Notated string `json:"notation,omitempty"`
}

// FromName / ToName expose squares as strings for the HTTP/WebSocket layer.
func (m Move) FromName() string { return m.From.Name() }
func (m Move) ToName() string   { return m.To.Name() }

// String renders the move in UCI-like long algebraic form ("h2e2").
func (m Move) String() string { return m.From.Name() + m.To.Name() }

// Equal compares two moves by endpoints.
func (m Move) Equal(o Move) bool { return m.From == o.From && m.To == o.To }

// ParseMove accepts "h2e2" or "h2-e2".
func ParseMove(s string) (Move, error) {
	clean := strings.ReplaceAll(strings.TrimSpace(s), "-", "")
	if len(clean) != 4 {
		return Move{}, fmt.Errorf("game: bad move %q", s)
	}
	from, err := ParseSquare(clean[0:2])
	if err != nil {
		return Move{}, err
	}
	to, err := ParseSquare(clean[2:4])
	if err != nil {
		return Move{}, err
	}
	return Move{From: from, To: to}, nil
}

// StartFEN is the standard opening position.
const StartFEN = "rnbakabnr/9/1c5c1/p1p1p1p1p/9/9/P1P1P1P1P/1C5C1/9/RNBAKABNR w - - 0 1"

// Board is a full game state: position plus move counters and history.
type Board struct {
	squares [Rows * Cols]Piece
	Turn    Side
	// Halfmove counts plies since the last capture, used for the draw rule.
	Halfmove int
	// Fullmove is the 1-based move number.
	Fullmove int
}

// NewBoard returns a Board in the standard opening position.
func NewBoard() *Board {
	b, err := ParseFEN(StartFEN)
	if err != nil {
		panic("game: invalid StartFEN: " + err.Error())
	}
	return b
}

// ParseFEN loads a position from xiangqi FEN notation.
func ParseFEN(fen string) (*Board, error) {
	fields := strings.Fields(fen)
	if len(fields) < 2 {
		return nil, fmt.Errorf("game: FEN needs at least a placement and a side field")
	}
	b := &Board{Turn: Red, Fullmove: 1}

	ranks := strings.Split(fields[0], "/")
	if len(ranks) != Rows {
		return nil, fmt.Errorf("game: FEN has %d ranks, want %d", len(ranks), Rows)
	}
	for row, rank := range ranks {
		col := 0
		for _, r := range rank {
			if r >= '1' && r <= '9' {
				col += int(r - '0')
				continue
			}
			p, ok := PieceFromFEN(r)
			if !ok {
				return nil, fmt.Errorf("game: bad FEN piece %q", r)
			}
			if col >= Cols {
				return nil, fmt.Errorf("game: rank %d overflows", row)
			}
			b.squares[NewSquare(row, col)] = p
			col++
		}
		if col != Cols {
			return nil, fmt.Errorf("game: rank %d has %d columns, want %d", row, col, Cols)
		}
	}

	turn, err := ParseSide(fields[1])
	if err != nil {
		return nil, err
	}
	b.Turn = turn

	if len(fields) >= 4 && fields[3] != "-" {
		fmt.Sscanf(fields[3], "%d", &b.Halfmove)
	}
	if len(fields) >= 6 {
		fmt.Sscanf(fields[5], "%d", &b.Fullmove)
	}
	if b.Fullmove < 1 {
		b.Fullmove = 1
	}
	return b, nil
}

// FEN serialises the position back to xiangqi FEN notation.
func (b *Board) FEN() string {
	var sb strings.Builder
	for row := 0; row < Rows; row++ {
		if row > 0 {
			sb.WriteByte('/')
		}
		empty := 0
		for col := 0; col < Cols; col++ {
			p := b.squares[NewSquare(row, col)]
			if p.Type == NoPiece {
				empty++
				continue
			}
			if empty > 0 {
				sb.WriteByte(byte('0' + empty))
				empty = 0
			}
			sb.WriteRune(p.FEN())
		}
		if empty > 0 {
			sb.WriteByte(byte('0' + empty))
		}
	}
	turn := "w"
	if b.Turn == Black {
		turn = "b"
	}
	fmt.Fprintf(&sb, " %s - - %d %d", turn, b.Halfmove, b.Fullmove)
	return sb.String()
}

// Clone deep-copies the board.
func (b *Board) Clone() *Board {
	c := *b
	return &c
}

// At returns the piece on a square.
func (b *Board) At(sq Square) Piece { return b.squares[sq] }

// Set places a piece on a square (used by the FEN loader and tests).
func (b *Board) Set(sq Square, p Piece) { b.squares[sq] = p }

// Pieces returns every occupied square with its piece, in board reading order.
func (b *Board) Pieces() []struct {
	Square Square
	Piece  Piece
} {
	out := make([]struct {
		Square Square
		Piece  Piece
	}, 0, 32)
	for i := 0; i < Rows*Cols; i++ {
		if p := b.squares[i]; p.Type != NoPiece {
			out = append(out, struct {
				Square Square
				Piece  Piece
			}{Square(i), p})
		}
	}
	return out
}

// FindKing returns the square of the given side's king, or false when absent.
func (b *Board) FindKing(side Side) (Square, bool) {
	for i := 0; i < Rows*Cols; i++ {
		if p := b.squares[i]; p.Side == side && p.Type == King {
			return Square(i), true
		}
	}
	return 0, false
}

// inPalace reports whether a square is inside the given side's 3x3 palace.
func inPalace(sq Square, side Side) bool {
	col := sq.Col()
	if col < 3 || col > 5 {
		return false
	}
	row := sq.Row()
	if side == Red {
		return row >= 7 && row <= 9
	}
	return row >= 0 && row <= 2
}

// ownHalf reports whether a square is on the given side's own side of the river.
func ownHalf(sq Square, side Side) bool {
	if side == Red {
		return sq.Row() >= 5
	}
	return sq.Row() <= 4
}
