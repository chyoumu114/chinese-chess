package gamehall

// This file holds the wire-facing view types. They live beside the session
// rather than in the transport package so that both the WebSocket layer and the
// HTTP layer can render the same shapes.

// PlayerView describes one seat of a room.
type PlayerView struct {
	UserID    int64  `json:"userId,omitempty"`
	Name      string `json:"name"`
	Side      string `json:"side"`
	Online    bool   `json:"online"`
	IsAI      bool   `json:"isAI,omitempty"`
	AILevel   int    `json:"aiLevel,omitempty"`
	MSLeft    int64  `json:"msLeft"`
	MoveCount int    `json:"moveCount"`
}

// MoveView is one played ply as sent to clients.
type MoveView struct {
	Ply      int    `json:"ply"`
	Side     string `json:"side"`
	From     string `json:"from"`
	To       string `json:"to"`
	Notation string `json:"notation"`
	Check    bool   `json:"check"`
	MSSpent  int64  `json:"msSpent"`
}

// RoomView is the authoritative snapshot of a room.
type RoomView struct {
	ID         int64      `json:"id"`
	Status     string     `json:"status"`
	FEN        string     `json:"fen"`
	Turn       string     `json:"turn"`
	InCheck    bool       `json:"inCheck"`
	Outcome    string     `json:"outcome"`
	Reason     string     `json:"reason,omitempty"`
	WinnerSide string     `json:"winnerSide,omitempty"`
	Rated      bool       `json:"rated"`
	Red        PlayerView `json:"red"`
	Black      PlayerView `json:"black"`
	Moves      []MoveView `json:"moves"`
	// LegalFrom maps an origin square to the destinations legal for the side to
	// move, so clients can highlight moves without reimplementing the rules.
	LegalFrom map[string][]string `json:"legalFrom"`
	// YouAre names the side the receiving connection plays. It is filled in per
	// connection when the snapshot is sent, so it is empty on a plain snapshot.
	YouAre string `json:"youAre,omitempty"`
	// Result carries the final detail once the game is decided. It is included
	// in the snapshot, not only in a separate game_over frame, so a client that
	// reconnects or misses that frame still learns the outcome and can show the
	// right banner.
	Result *GameOverView `json:"result,omitempty"`
	// DrawOfferedBy names the side with an outstanding draw offer.
	DrawOfferedBy string `json:"drawOfferedBy,omitempty"`
	// TakebackRequestedBy names the side asking to undo.
	TakebackRequestedBy string `json:"takebackRequestedBy,omitempty"`
}

// RoomResult is the terminal summary stored on a room so that every snapshot
// after the game ends carries the outcome.
type RoomResult struct {
	Outcome    string
	Reason     string
	WinnerSide string
	FEN        string
	MoveCount  int
}

// GameOverView is the terminal summary of a game. It is used both as the
// payload of the game_over message and as RoomView.Result.
type GameOverView struct {
	Outcome    string `json:"outcome"`
	Reason     string `json:"reason,omitempty"`
	WinnerSide string `json:"winnerSide,omitempty"`
	FEN        string `json:"fen"`
	MoveCount  int    `json:"moveCount"`
}
