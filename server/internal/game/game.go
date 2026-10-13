package game

import (
	"errors"
	"strings"
)

// Game-level errors returned to the API layer.
var (
	ErrGameOver    = errors.New("game: game is already over")
	ErrNotYourTurn = errors.New("game: not your turn")
	ErrIllegalMove = errors.New("game: illegal move")
	ErrNoSuchPiece = errors.New("game: no piece on the origin square")
	ErrWrongSide   = errors.New("game: that piece belongs to the other side")
)

// Outcome describes how a game ended.
type Outcome string

const (
	OutcomeOngoing  Outcome = "ongoing"
	OutcomeRedWin   Outcome = "red_win"
	OutcomeBlackWin Outcome = "black_win"
	OutcomeDraw     Outcome = "draw"
)

// Reason records why a game ended, for display in the game history.
type Reason string

const (
	ReasonCheckmate   Reason = "checkmate"
	ReasonStalemate   Reason = "stalemate"
	ReasonKingCapture Reason = "king_capture"
	ReasonResign      Reason = "resign"
	ReasonTimeout     Reason = "timeout"
	ReasonDrawAgreed  Reason = "draw_agreed"
	ReasonSixtyMove   Reason = "sixty_move"
	ReasonAbandoned   Reason = "abandoned"
)

// 三种由局面重复触发的终局原因：单方长将、单方长捉，以及不变作和。
const (
	ReasonPerpetualCheck Reason = "perpetual_check"
	ReasonPerpetualChase Reason = "perpetual_chase"
	ReasonRepetition     Reason = "repetition"
)

// MoveRecord is one played ply as stored in history and sent to clients.
type MoveRecord struct {
	Ply      int    `json:"ply"`
	Side     Side   `json:"side"`
	Move     Move   `json:"move"`
	FEN      string `json:"fen"`
	Check    bool   `json:"check"`
	Notation string `json:"notation"`
}

// Record is the private undo record for one ply.
type Record struct {
	move     Move
	piece    Piece
	captured Piece
	halfmove int
	fullmove int
	turn     Side
	notation string
}

// Game wraps a Board with legality enforcement, history and result tracking.
type Game struct {
	board   *Board
	history []Record
	records []MoveRecord
	outcome Outcome
	reason  Reason
	// decided is set once the outcome stops being "ongoing". A separate flag is
	// needed because the zero value of Outcome is the empty string, not
	// OutcomeOngoing, so comparing against the constant alone would report
	// every fresh game as finished.
	decided bool

	// marks 按步序记录每一步落地后的局面、走子方与是否将军，seen 统计每个局面
	// 出现过几次。两者一起支撑「三次重复 → 长将/长捉/和棋」的裁决，
	// 见 repetition.go。
	marks []plyMark
	seen  map[positionKey]int
}

// NewGame returns a fresh game in the opening position.
func NewGame() *Game {
	g := &Game{board: NewBoard()}
	g.initTracking()
	return g
}

// NewGameFromFEN restores a game from a stored FEN string.
func NewGameFromFEN(fen string) (*Game, error) {
	b, err := ParseFEN(fen)
	if err != nil {
		return nil, err
	}
	g := &Game{board: b}
	g.initTracking()
	return g, nil
}

// Board exposes the current position.
func (g *Game) Board() *Board { return g.board }

// Turn returns the side to play.
func (g *Game) Turn() Side { return g.board.Turn }

// FEN returns the current position.
func (g *Game) FEN() string { return g.board.FEN() }

// Outcome returns the result; OutcomeOngoing while the game is live.
func (g *Game) Outcome() Outcome {
	if !g.decided {
		return OutcomeOngoing
	}
	return g.outcome
}

// Reason returns why the game ended, empty while ongoing.
func (g *Game) Reason() Reason { return g.reason }

// Over reports whether the game has finished.
func (g *Game) Over() bool { return g.decided }

// PlyCount returns the number of plies played.
func (g *Game) PlyCount() int { return len(g.history) }

// History returns the notated move list.
func (g *Game) History() []MoveRecord {
	out := make([]MoveRecord, len(g.records))
	copy(out, g.records)
	return out
}

// InCheck reports whether the side to play is in check.
func (g *Game) InCheck() bool { return g.board.InCheck(g.board.Turn) }

// Clone returns an independent copy of the game, used for move counting and for
// evaluating hypothetical lines without disturbing the live game.
func (g *Game) Clone() *Game {
	c := &Game{
		board:   g.board.Clone(),
		outcome: g.outcome,
		reason:  g.reason,
		decided: g.decided,
	}
	c.history = append([]Record(nil), g.history...)
	c.records = append([]MoveRecord(nil), g.records...)
	c.marks = append([]plyMark(nil), g.marks...)
	if g.seen != nil {
		c.seen = make(map[positionKey]int, len(g.seen))
		for k, v := range g.seen {
			c.seen[k] = v
		}
	}
	return c
}

// ApplyUnchecked plays a move without validating it. Callers must have
// established legality; this is used by the perft harness and by the AI.
func (g *Game) ApplyUnchecked(m Move) {
	captured := g.board.At(m.To)
	g.history = append(g.history, Record{
		move: m, piece: g.board.At(m.From), captured: captured,
		halfmove: g.board.Halfmove, fullmove: g.board.Fullmove, turn: g.board.Turn,
	})
	g.board.apply(m)
	g.recordPly(m)
	g.decided = false
	g.reason = ""
}

// LegalMoves returns every legal move for the side to play.
func (g *Game) LegalMoves() []Move { return g.legalMoves(g.board.Turn) }

// LegalMovesFor returns the legal moves for one side regardless of whose turn
// it is. It is used by the AI and by diagnostics.
func (g *Game) LegalMovesFor(side Side) []Move { return g.legalMoves(side) }

// LegalMovesFrom returns the legal moves originating on a square.
func (g *Game) LegalMovesFrom(from Square) []Move {
	out := make([]Move, 0, 8)
	for _, m := range g.LegalMoves() {
		if m.From == from {
			out = append(out, m)
		}
	}
	return out
}

// PseudoLegalMovesFor exposes unchecked move generation for diagnostics.
func (g *Game) PseudoLegalMovesFor(side Side) []Move {
	return g.board.PseudoLegalMovesFor(side)
}

func (g *Game) legalMoves(side Side) []Move {
	pseudo := g.board.PseudoLegalMovesFor(side)
	legal := make([]Move, 0, len(pseudo))
	for _, m := range pseudo {
		if g.legalAfter(m, side) {
			legal = append(legal, m)
		}
	}
	return legal
}

// legalAfter reports whether a move is fully legal for side: the piece must
// actually be able to make that move (its movement pattern, blocking pieces and
// board bounds), and it must leave the mover's own king safe.
//
// The movement check matters because Play is reachable from the API with an
// arbitrary square pair. Without it a rook could be teleported past a blocking
// pawn, since king safety alone cannot detect that.
func (g *Game) legalAfter(m Move, side Side) bool {
	scratch := g.board.Clone()
	piece := scratch.At(m.From)
	if piece.Type == NoPiece || piece.Side != side {
		return false
	}
	if !g.isPseudoLegal(m) {
		return false
	}
	scratch.apply(m)
	if _, ok := scratch.FindKing(side); !ok {
		return false // own king was captured by the flying general
	}
	if scratch.kingsFacing() {
		return false
	}
	return !scratch.InCheck(side)
}

// isPseudoLegal reports whether the move is one the piece could make ignoring
// king safety, by looking for it in the generator's output for its origin.
func (g *Game) isPseudoLegal(m Move) bool {
	for _, candidate := range g.board.PseudoLegalMovesFor(g.board.At(m.From).Side) {
		if candidate.Equal(m) {
			return true
		}
	}
	return false
}

// Play validates and applies a move for the side to play, appending it to the
// history and updating the outcome.
func (g *Game) Play(m Move) (MoveRecord, error) {
	if g.Over() {
		return MoveRecord{}, ErrGameOver
	}
	side := g.board.Turn
	piece := g.board.At(m.From)
	if piece.Type == NoPiece {
		return MoveRecord{}, ErrNoSuchPiece
	}
	if piece.Side != side {
		return MoveRecord{}, ErrWrongSide
	}
	if !g.legalAfter(m, side) {
		return MoveRecord{}, ErrIllegalMove
	}

	notation := g.describe(m)
	captured := g.board.At(m.To)

	g.history = append(g.history, Record{
		move: m, piece: piece, captured: captured,
		halfmove: g.board.Halfmove, fullmove: g.board.Fullmove,
		turn: side, notation: notation,
	})
	g.board.apply(m)
	g.recordPly(m)

	entry := MoveRecord{
		Ply:      len(g.history),
		Side:     side,
		Move:     Move{From: m.From, To: m.To, Captured: captured.Type, Notated: notation},
		FEN:      g.board.FEN(),
		Check:    g.board.InCheck(g.board.Turn),
		Notation: notation,
	}
	g.records = append(g.records, entry)

	g.updateOutcome(captured)
	if !g.decided {
		if v := g.Repetition(); v.Decided {
			g.outcome = v.Outcome
			g.reason = v.Reason
			g.decided = true
		}
	}
	return entry, nil
}

// apply mutates the board with a move, handling counters and side to move.
func (b *Board) apply(m Move) {
	captured := b.At(m.To)
	b.squares[m.To] = b.squares[m.From]
	b.squares[m.From] = Piece{}

	if captured.Type != NoPiece || b.squares[m.To].Type == Pawn {
		b.Halfmove = 0
	} else {
		b.Halfmove++
	}
	if b.Turn == Black {
		b.Fullmove++
	}
	b.Turn = b.Turn.Opponent()
}

// updateOutcome decides whether the game just ended. captured carries the piece
// that stood on the destination square before the move.
func (g *Game) updateOutcome(captured Piece) {
	if captured.Type == King {
		// The side that just moved won by capturing the king.
		g.setWin(g.board.Turn.Opponent(), ReasonKingCapture)
		return
	}
	next := g.board.Turn
	if !g.hasLegalMove(next) {
		if g.board.InCheck(next) {
			g.setWin(next.Opponent(), ReasonCheckmate)
		} else {
			// Stalemate is a loss in xiangqi, not a draw.
			g.setWin(next.Opponent(), ReasonStalemate)
		}
		return
	}
	// Halfmove is counted in plies; 120 plies is the conventional 60-move rule.
	if g.board.Halfmove >= 120 {
		g.outcome = OutcomeDraw
		g.reason = ReasonSixtyMove
		g.decided = true
	}
}

func (g *Game) setWin(winner Side, reason Reason) {
	if winner == Red {
		g.outcome = OutcomeRedWin
	} else {
		g.outcome = OutcomeBlackWin
	}
	g.reason = reason
	g.decided = true
}

func (g *Game) hasLegalMove(side Side) bool {
	for _, m := range g.board.PseudoLegalMovesFor(side) {
		if g.legalAfter(m, side) {
			return true
		}
	}
	return false
}

// HasLegalMove reports whether a side has any legal move in this position.
func (g *Game) HasLegalMove(side Side) bool { return g.hasLegalMove(side) }

// Resign ends the game in favour of the opponent of the resigning side.
func (g *Game) Resign(side Side) error {
	if g.Over() {
		return ErrGameOver
	}
	g.setWin(side.Opponent(), ReasonResign)
	return nil
}

// Timeout ends the game because a side ran out of time.
func (g *Game) Timeout(side Side) error {
	if g.Over() {
		return ErrGameOver
	}
	g.setWin(side.Opponent(), ReasonTimeout)
	return nil
}

// Abandon ends the game because a side left the room. Walking out of a running
// game counts as a loss, and it is recorded with its own reason so the opponent
// is told why the game stopped rather than reading it as a resignation.
func (g *Game) Abandon(side Side) error {
	if g.Over() {
		return ErrGameOver
	}
	g.setWin(side.Opponent(), ReasonAbandoned)
	return nil
}

// Draw ends the game as a draw, recording the agreed reason.
func (g *Game) Draw(reason Reason) error {
	if g.Over() {
		return ErrGameOver
	}
	if reason == "" {
		reason = ReasonDrawAgreed
	}
	g.outcome = OutcomeDraw
	g.reason = reason
	g.decided = true
	return nil
}

// Undo rolls back the last ply. It is used by takeback requests.
func (g *Game) Undo() (MoveRecord, bool) {
	if len(g.history) == 0 {
		return MoveRecord{}, false
	}
	rec := g.history[len(g.history)-1]
	g.history = g.history[:len(g.history)-1]
	entry := g.records[len(g.records)-1]
	g.records = g.records[:len(g.records)-1]
	g.dropPosition()

	g.board.Turn = rec.turn
	g.board.Halfmove = rec.halfmove
	g.board.Fullmove = rec.fullmove
	g.board.squares[rec.move.From] = rec.piece
	if rec.captured.Type == NoPiece {
		g.board.squares[rec.move.To] = Piece{}
	} else {
		g.board.squares[rec.move.To] = rec.captured
	}
	g.outcome = OutcomeOngoing
	g.reason = ""
	g.decided = false
	return entry, true
}

// Perft counts leaf nodes of the legal move tree to the given depth. It is the
// standard correctness harness for a move generator: any rule error changes the
// counts.
func (g *Game) Perft(depth int) int {
	if depth <= 0 {
		return 1
	}
	moves := g.LegalMoves()
	if depth == 1 {
		return len(moves)
	}
	total := 0
	for _, m := range moves {
		child := g.Clone()
		child.ApplyUnchecked(m)
		total += child.Perft(depth - 1)
	}
	return total
}

// PerftDivide returns the per-move node counts at the given depth, which makes
// a mismatch easier to localise.
func (g *Game) PerftDivide(depth int) map[string]int {
	out := map[string]int{}
	for _, m := range g.LegalMoves() {
		child := g.Clone()
		child.ApplyUnchecked(m)
		out[m.String()] = child.Perft(depth - 1)
	}
	return out
}

// Notation describes a move in Chinese long algebraic form, e.g. "炮二平五".
func (g *Game) Notation(m Move) (string, error) {
	if g.board.At(m.From).Type == NoPiece {
		return "", ErrNoSuchPiece
	}
	return g.describe(m), nil
}

var numeralCN = [...]string{"一", "二", "三", "四", "五", "六", "七", "八", "九"}

// fileLabel renders a column as the file number each side would say. Both sides
// count files from their own right, but the board columns are stored
// left-to-right as seen from above (column a is on Black's right), so column i
// is file 一 and column a is file 九 for both camps: the mirror symmetry of the
// two palaces makes one formula serve both.
//
// This is what makes the standard opening read 炮二平五: the cannon that starts
// on column h is file 二 for Red, and column e is file 五. The mirrored Black
// move 炮2平5 is the cannon on column b, whose column index is 1 and whose file
// number is therefore 二.
func fileLabel(col int, side Side) string {
	return numeralCN[Cols-1-col]
}

// describe builds the Chinese notation for a move.
func (g *Game) describe(m Move) string {
	p := g.board.At(m.From)
	if p.Type == NoPiece {
		return m.String()
	}
	fromCol, toCol := m.From.Col(), m.To.Col()
	fromRow, toRow := m.From.Row(), m.To.Row()

	var sb strings.Builder
	// The 前/后 qualifier replaces the origin file and precedes everything else,
	// so a rear rook moving sideways is 后车平八 and not 车九平八.
	if prefix := g.disambiguate(m.From, p); prefix != "" {
		sb.WriteString(prefix)
		sb.WriteString(p.Text())
	} else {
		sb.WriteString(p.Text())
		sb.WriteString(fileLabel(fromCol, p.Side))
	}

	// "Forward" always means towards the opponent: Red advances up the board
	// (decreasing row), Black advances down it (increasing row).
	advancing := toRow < fromRow
	if p.Side == Black {
		advancing = toRow > fromRow
	}

	switch {
	case fromCol == toCol:
		// Straight move along a file: the trailing numeral is the distance.
		if advancing {
			sb.WriteString("进")
		} else {
			sb.WriteString("退")
		}
		sb.WriteString(numeralCN[abs(toRow-fromRow)-1])
	case fromRow == toRow:
		sb.WriteString("平")
		sb.WriteString(fileLabel(toCol, p.Side))
	default:
		if advancing {
			sb.WriteString("进")
		} else {
			sb.WriteString("退")
		}
		// Diagonal movers (horse, elephant, advisor) name the destination file
		// rather than a distance, e.g. 相七进五.
		if p.Type == Horse || p.Type == Elephant || p.Type == Advisor {
			sb.WriteString(fileLabel(toCol, p.Side))
			break
		}
		sb.WriteString(numeralCN[abs(toRow-fromRow)-1])
	}
	return sb.String()
}

// disambiguate returns the 前/后 qualifier for pieces stacked on a file.
func (g *Game) disambiguate(from Square, p Piece) string {
	col := from.Col()
	sameFile := make([]Square, 0, 3)
	for row := 0; row < Rows; row++ {
		sq := NewSquare(row, col)
		q := g.board.At(sq)
		if q.Type == p.Type && q.Side == p.Side {
			sameFile = append(sameFile, sq)
		}
	}
	if len(sameFile) < 2 {
		return ""
	}
	if p.Side == Red {
		// Red's "front" piece is the one closest to Black: the smaller row.
		if from == sameFile[0] {
			return "前"
		}
		return "后"
	}
	// Black's front piece is the one closest to Red: the larger row.
	if from == sameFile[len(sameFile)-1] {
		return "前"
	}
	return "后"
}

// Status is a compact, client-facing view of the game state.
type Status struct {
	FEN       string   `json:"fen"`
	Turn      Side     `json:"turn"`
	Outcome   Outcome  `json:"outcome"`
	Reason    Reason   `json:"reason,omitempty"`
	InCheck   bool     `json:"inCheck"`
	PlyCount  int      `json:"plyCount"`
	Fullmove  int      `json:"fullmove"`
	LegalHint int      `json:"legalMoveCount"`
	Notation  []string `json:"notation"`
}

// Status returns the current client-facing status.
func (g *Game) Status() Status {
	notation := make([]string, 0, len(g.records))
	for _, r := range g.records {
		notation = append(notation, r.Notation)
	}
	legal := 0
	if !g.Over() {
		legal = len(g.LegalMoves())
	}
	return Status{
		FEN:       g.FEN(),
		Turn:      g.board.Turn,
		Outcome:   g.Outcome(),
		Reason:    g.reason,
		InCheck:   g.InCheck(),
		PlyCount:  len(g.history),
		Fullmove:  g.board.Fullmove,
		LegalHint: legal,
		Notation:  notation,
	}
}

// Winner returns the winning side, or false for an ongoing game or a draw.
func (g *Game) Winner() (Side, bool) {
	switch g.Outcome() {
	case OutcomeRedWin:
		return Red, true
	case OutcomeBlackWin:
		return Black, true
	}
	return Red, false
}

// MoveJSON is the wire form of a legal move, used by the hint endpoint.
type MoveJSON struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Notation string `json:"notation,omitempty"`
}

// MarshalMoves converts moves to their wire form, annotating notation.
func (g *Game) MarshalMoves(moves []Move) []MoveJSON {
	out := make([]MoveJSON, 0, len(moves))
	for _, m := range moves {
		out = append(out, MoveJSON{From: m.FromName(), To: m.ToName(), Notation: g.describe(m)})
	}
	return out
}
