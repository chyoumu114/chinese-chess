// Package ws implements the real-time layer: a WebSocket hub that tracks the
// live connections and their rooms, and drives the gamehall session, which owns
// all game state behind a single mutex. Every state change therefore happens in
// one place, and the hub only fans the resulting snapshots out to clients.
package ws

import "encoding/json"

// Client-to-server message types.
const (
	// TypeJoin attaches the connection to a room. An empty RoomID asks the hub
	// to place the player: into a waiting game, or into a fresh one.
	TypeJoin = "join"
	// TypeReady accepts the next game in the caller's room. Two human players
	// both have to send it before the pieces move; after a finished game it
	// starts a rematch in the same room with the colours swapped.
	TypeReady = "ready"
	// TypeLeave gives up the caller's seat. Leaving the last seat dissolves the
	// room, which is how a quick match is cancelled.
	TypeLeave = "leave"
	// TypeMove submits a move in the current room using UCI-like coordinates.
	TypeMove = "move"
	// TypeResign concedes the current game.
	TypeResign = "resign"
	// TypeOfferDraw offers a draw to the opponent.
	TypeOfferDraw = "offer_draw"
	// TypeRespondDraw accepts or declines an outstanding draw offer.
	TypeRespondDraw = "respond_draw"
	// TypeRequestTakeback asks the opponent to undo the last ply.
	TypeRequestTakeback = "request_takeback"
	// TypeRespondTakeback accepts or declines an outstanding takeback request.
	TypeRespondTakeback = "respond_takeback"
	// TypeChat sends a chat line to the room.
	TypeChat = "chat"
	// TypeSync asks the server to resend the full room state.
	TypeSync = "sync"
	// TypeWatch attaches the caller to a room as a pure spectator. Unlike join
	// it never takes a seat, so it works on a game already under way.
	TypeWatch = "watch"
	// TypePing is an application-level keepalive.
	TypePing = "ping"
)

// Server-to-client message types.
const (
	// TypeWelcome acknowledges the connection with the caller's identity.
	TypeWelcome = "welcome"
	// TypeRoom carries the authoritative room snapshot.
	TypeRoom = "room"
	// TypeMoveMade announces an accepted move.
	TypeMoveMade = "move_made"
	// TypeGameOver announces the result.
	TypeGameOver = "game_over"
	// TypeError reports a rejected command.
	TypeError = "error"
	// TypeChatMessage carries a chat line.
	TypeChatMessage = "chat"
	// TypeDrawOffered announces a draw offer.
	TypeDrawOffered = "draw_offered"
	// TypeDrawDeclined announces that a draw offer was declined.
	TypeDrawDeclined = "draw_declined"
	// TypeTakebackRequested announces a takeback request.
	TypeTakebackRequested = "takeback_requested"
	// TypeTakebackApplied announces that a takeback was accepted.
	TypeTakebackApplied = "takeback_applied"
	// TypeTakebackDeclined announces that a takeback was declined.
	TypeTakebackDeclined = "takeback_declined"
	// TypeOpponentLeft announces that the opponent disconnected.
	TypeOpponentLeft = "opponent_left"
	// TypeLeft acknowledges that the caller gave up its seat.
	TypeLeft = "left"
	// TypePong answers TypePing.
	TypePong = "pong"
	// TypePresence lists who is online in the lobby.
	TypePresence = "presence"
	// TypeWatching acknowledges that the caller now follows a room.
	TypeWatching = "watching"
)

// Envelope is the outer frame of every message in both directions.
type Envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// JoinRequest is the payload of a join message.
type JoinRequest struct {
	// RoomID asks to join a specific game. Zero means "place me".
	RoomID int64 `json:"roomId,omitempty"`
	// AILevel requests a game against the computer at that level. It is only
	// honoured for a solo join.
	AILevel int `json:"aiLevel,omitempty"`
	// Side is the side the player wants against the computer: "red", "black" or
	// empty for the server's choice.
	Side string `json:"side,omitempty"`
	// Rated marks the game as rated. It defaults to true.
	Rated *bool `json:"rated,omitempty"`
}

// MoveRequest is the payload of a move message.
type MoveRequest struct {
	// From and To are square names such as "b2" and "e2".
	From string `json:"from"`
	To   string `json:"to"`
}

// RespondRequest is the payload of a draw or takeback response.
type RespondRequest struct {
	Accept bool `json:"accept"`
}

// ChatRequest is the payload of a chat message.
type ChatRequest struct {
	Text string `json:"text"`
}

// PlayerView, MoveView, RoomView and GameOverView live in the gamehall package,
// which owns the state they describe. This file defines the wire envelope, the
// request payloads the transport decodes, and the message types that are pure
// transport concerns.

// ErrorView is the payload of an error message.
type ErrorView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// LeftView confirms that a client no longer occupies a seat, so it can drop the
// room it was rendering.
type LeftView struct {
	RoomID int64 `json:"roomId,omitempty"`
	// Dissolved is true when the room itself was dropped because it was empty.
	Dissolved bool `json:"dissolved,omitempty"`
}

// ChatView is a chat line in the room.
type ChatView struct {
	UserID int64  `json:"userId,omitempty"`
	Name   string `json:"name"`
	Side   string `json:"side,omitempty"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
}

// WelcomeView greets a newly connected client.
type WelcomeView struct {
	UserID   int64  `json:"userId,omitempty"`
	Name     string `json:"name"`
	Guest    bool   `json:"guest"`
	RoomID   int64  `json:"roomId,omitempty"`
	ServerAt int64  `json:"serverAt"`
}

// 在线状态：大厅里每个人的三种状态，前端按这个字符串渲染。
const (
	StatusOnline  = "online"
	StatusWaiting = "waiting"
	StatusPlaying = "playing"
)

// WatchRequest is the payload of a watch message.
type WatchRequest struct {
	// RoomID is the room to follow. A zero value is rejected.
	RoomID int64 `json:"roomId"`
}

// PresenceEntry lists one online player.
type PresenceEntry struct {
	UserID int64  `json:"userId,omitempty"`
	Name   string `json:"name"`
	RoomID int64  `json:"roomId,omitempty"`
	Status string `json:"status"`
}

// PresenceView is the lobby roster.
type PresenceView struct {
	Online []PresenceEntry `json:"online"`
	Rooms  int             `json:"rooms"`
}

// NewEnvelope marshals a payload into an Envelope, ignoring marshalling errors
// because every payload in this package is a plain struct.
func NewEnvelope(kind string, payload any) Envelope {
	if payload == nil {
		return Envelope{Type: kind}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw, _ = json.Marshal(ErrorView{Code: "encode_failed", Message: err.Error()})
		return Envelope{Type: TypeError, Data: raw}
	}
	return Envelope{Type: kind, Data: raw}
}

// Decode unmarshals an envelope payload into v.
func (e Envelope) Decode(v any) error {
	if len(e.Data) == 0 {
		return nil
	}
	return json.Unmarshal(e.Data, v)
}
