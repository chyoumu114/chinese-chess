package game

import "fmt"

// Side identifies which player a piece belongs to.
type Side uint8

const (
	// Red moves first. In the traditional notation Red sits at the bottom.
	Red Side = iota
	Black
)

func (s Side) String() string {
	if s == Red {
		return "red"
	}
	return "black"
}

// Opponent returns the other side.
func (s Side) Opponent() Side {
	if s == Red {
		return Black
	}
	return Red
}

// ParseSide converts a wire value ("red"/"black", or "r"/"b") into a Side. The
// letters w/b are also accepted because xiangqi FEN reuses the western
// white/black side-to-move field to mean Red/Black.
func ParseSide(s string) (Side, error) {
	switch s {
	case "red", "r", "RED", "Red", "w", "W":
		return Red, nil
	case "black", "b", "BLACK", "Black", "B":
		return Black, nil
	}
	return Red, fmt.Errorf("game: unknown side %q", s)
}

// PieceType is one of the seven xiangqi piece kinds.
type PieceType uint8

const (
	NoPiece  PieceType = iota
	King               // 帅 / 将
	Advisor            // 仕 / 士
	Elephant           // 相 / 象
	Horse              // 马
	Rook               // 车
	Cannon             // 炮
	Pawn               // 兵 / 卒
)

var pieceNames = map[PieceType]string{
	King:     "king",
	Advisor:  "advisor",
	Elephant: "elephant",
	Horse:    "horse",
	Rook:     "rook",
	Cannon:   "cannon",
	Pawn:     "pawn",
}

func (p PieceType) String() string {
	if n, ok := pieceNames[p]; ok {
		return n
	}
	return "none"
}

// Piece is a coloured piece type.
type Piece struct {
	Side Side
	Type PieceType
}

// fenChars maps the standard xiangqi FEN letters onto (side, type).
var fenChars = map[rune]Piece{
	'K': {Red, King}, 'A': {Red, Advisor}, 'B': {Red, Elephant}, 'E': {Red, Elephant},
	'N': {Red, Horse}, 'H': {Red, Horse}, 'R': {Red, Rook}, 'C': {Red, Cannon}, 'P': {Red, Pawn},
	'k': {Black, King}, 'a': {Black, Advisor}, 'b': {Black, Elephant}, 'e': {Black, Elephant},
	'n': {Black, Horse}, 'h': {Black, Horse}, 'r': {Black, Rook}, 'c': {Black, Cannon}, 'p': {Black, Pawn},
}

// pieceFen is the canonical single character used when serialising.
var pieceFen = map[Piece]rune{
	{Red, King}: 'K', {Red, Advisor}: 'A', {Red, Elephant}: 'B', {Red, Horse}: 'N',
	{Red, Rook}: 'R', {Red, Cannon}: 'C', {Red, Pawn}: 'P',
	{Black, King}: 'k', {Black, Advisor}: 'a', {Black, Elephant}: 'b', {Black, Horse}: 'n',
	{Black, Rook}: 'r', {Black, Cannon}: 'c', {Black, Pawn}: 'p',
}

// pieceText is the Chinese glyph rendered on the client board.
var pieceText = map[Piece]string{
	{Red, King}: "帅", {Red, Advisor}: "仕", {Red, Elephant}: "相", {Red, Horse}: "马",
	{Red, Rook}: "车", {Red, Cannon}: "炮", {Red, Pawn}: "兵",
	{Black, King}: "将", {Black, Advisor}: "士", {Black, Elephant}: "象", {Black, Horse}: "马",
	{Black, Rook}: "车", {Black, Cannon}: "炮", {Black, Pawn}: "卒",
}

// FEN returns the single character for this piece in FEN notation.
func (p Piece) FEN() rune {
	if r, ok := pieceFen[p]; ok {
		return r
	}
	return '.'
}

// Text returns the Chinese glyph for this piece.
func (p Piece) Text() string { return pieceText[p] }

// PieceFromFEN parses one FEN character.
func PieceFromFEN(r rune) (Piece, bool) {
	p, ok := fenChars[r]
	return p, ok
}
