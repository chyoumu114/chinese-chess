// Package ai plays xiangqi with a negamax search over the engine's move
// generator. It powers the single-player mode and is deliberately dependency
// free so it can run inside the API server.
package ai

import (
	"math/rand"
	"time"

	"xiangqi/internal/game"
)

// Level selects how hard the computer plays.
type Level int

const (
	LevelEasy   Level = 1
	LevelNormal Level = 2
	LevelHard   Level = 3
)

// String names the level.
func (l Level) String() string {
	switch l {
	case LevelEasy:
		return "easy"
	case LevelNormal:
		return "normal"
	case LevelHard:
		return "hard"
	}
	return "normal"
}

// ParseLevel maps a wire value onto a level, defaulting to normal.
func ParseLevel(v int) Level {
	switch v {
	case 1:
		return LevelEasy
	case 3:
		return LevelHard
	default:
		return LevelNormal
	}
}

// settingsFor returns the search depth and the amount of randomness applied to
// the root move choice. Easy play deliberately blunders by picking among the
// near-best moves.
func settingsFor(l Level) (depth int, slack int) {
	switch l {
	case LevelEasy:
		return 2, 120
	case LevelHard:
		return 4, 0
	default:
		return 3, 40
	}
}

// Search picks a move for the side to play in g. It returns false when no move
// is available.
func Search(g *game.Game, level Level, budget time.Duration) (game.Move, bool) {
	moves := g.LegalMoves()
	if len(moves) == 0 {
		return game.Move{}, false
	}
	depth, slack := settingsFor(level)

	deadline := time.Now().Add(budget)
	var best []game.Move
	bestScore := negInf
	ordered := orderMoves(g, moves)

	for _, m := range ordered {
		child := g.Clone()
		child.ApplyUnchecked(m)
		score := -negamax(child, depth-1, negInf, posInf, deadline)
		switch {
		case score > bestScore:
			bestScore = score
			best = []game.Move{m}
		case score == bestScore:
			best = append(best, m)
		}
		if time.Now().After(deadline) {
			break
		}
	}

	if len(best) == 0 {
		return moves[0], true
	}
	if slack > 0 && len(best) == 1 {
		// Give the easy levels a chance to choose a slightly weaker move so a
		// beginner is not crushed every game.
		if alt := withinSlack(g, ordered, bestScore, slack, depth, deadline); alt != nil {
			best = alt
		}
	}
	return best[rand.Intn(len(best))], true
}

// withinSlack collects root moves whose score is within slack of the best.
func withinSlack(g *game.Game, ordered []game.Move, bestScore, slack, depth int, deadline time.Time) []game.Move {
	out := make([]game.Move, 0, 4)
	for _, m := range ordered {
		child := g.Clone()
		child.ApplyUnchecked(m)
		score := -negamax(child, depth-1, negInf, posInf, deadline)
		if score >= bestScore-slack {
			out = append(out, m)
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

const (
	posInf = 1 << 30
	negInf = -posInf
	// mateScore is returned for a position where the side to move has already
	// lost its king, or has no legal move at all.
	mateScore = 900_000
)

// negamax searches to the given depth. The score is always from the point of
// view of the side to move in g.
func negamax(g *game.Game, depth, alpha, beta int, deadline time.Time) int {
	if g.Over() {
		// The side to move has lost: mated, stalemated or its king is gone.
		return -mateScore - depth
	}
	if depth <= 0 || time.Now().After(deadline) {
		return evaluate(g)
	}

	moves := orderMoves(g, g.LegalMoves())
	if len(moves) == 0 {
		// No legal move: in xiangqi that is a loss whether or not the king is
		// attacked.
		return -mateScore - depth
	}

	best := negInf
	for _, m := range moves {
		child := g.Clone()
		child.ApplyUnchecked(m)
		score := -negamax(child, depth-1, -beta, -alpha, deadline)
		if score > best {
			best = score
		}
		if best > alpha {
			alpha = best
		}
		if alpha >= beta {
			break // beta cutoff
		}
	}
	return best
}

// evaluate scores the position from the point of view of the side to move.
func evaluate(g *game.Game) int {
	board := g.Board()
	score := 0
	var redKing, blackKing bool
	for _, entry := range board.Pieces() {
		value := materialValue(entry.Piece.Type) + positionalValue(entry.Square, entry.Piece)
		switch entry.Piece.Side {
		case game.Red:
			score += value
			if entry.Piece.Type == game.King {
				redKing = true
			}
		case game.Black:
			score -= value
			if entry.Piece.Type == game.King {
				blackKing = true
			}
		}
	}
	if !redKing {
		return -mateScore
	}
	if !blackKing {
		return mateScore
	}
	if g.Turn() == game.Black {
		return -score
	}
	return score
}

// materialValue is the conventional centipawn value of each piece type.
func materialValue(t game.PieceType) int {
	switch t {
	case game.King:
		return 100_000
	case game.Rook:
		return 900
	case game.Cannon:
		return 450
	case game.Horse:
		return 400
	case game.Advisor:
		return 200
	case game.Elephant:
		return 200
	case game.Pawn:
		return 100
	}
	return 0
}

// positionalValue rewards pieces for good squares: central files, advanced
// pawns, and cannons and horses that are developed rather than boxed in.
func positionalValue(sq game.Square, p game.Piece) int {
	row, col := sq.Row(), sq.Col()
	// Distance from the central file, 0 at file e and 4 at the edges.
	centre := abs(col - 4)

	switch p.Type {
	case game.Pawn:
		// Pawns are worth more the further they have advanced.
		if p.Side == game.Red {
			return (9 - row) * 6
		}
		return row * 6
	case game.Horse, game.Cannon:
		// Prefer the middle files and discourage dawdling on the back rank.
		advance := 0
		if p.Side == game.Red {
			advance = (9 - row) * 3
		} else {
			advance = row * 3
		}
		return advance + (4-centre)*4
	case game.Rook:
		return (4 - centre) * 3
	case game.King:
		// Staying home is safer.
		if p.Side == game.Red {
			return (9 - row) * 2
		}
		return row * 2
	case game.Advisor, game.Elephant:
		return (4 - centre) * 2
	}
	return 0
}

// orderMoves sorts moves so that captures and checks are searched first, which
// makes the alpha-beta cutoffs far more effective.
func orderMoves(g *game.Game, moves []game.Move) []game.Move {
	type scored struct {
		move  game.Move
		score int
	}
	ranked := make([]scored, 0, len(moves))
	board := g.Board()
	for _, m := range moves {
		score := 0
		if target := board.At(m.To); target.Type != game.NoPiece {
			score += 1000 + materialValue(target.Type)*10 - materialValue(board.At(m.From).Type)
		}
		ranked = append(ranked, scored{move: m, score: score})
	}
	// Insertion sort: the move lists are small (a few dozen at most) and this
	// avoids allocating a comparator closure per node.
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
	out := make([]game.Move, len(ranked))
	for i, s := range ranked {
		out[i] = s.move
	}
	return out
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
