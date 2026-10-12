package main

import (
	"context"
	"time"

	"xiangqi/internal/gamehall"
	"xiangqi/internal/store"
)

// This file adapts a persistence backend onto the two seams the live-game layer
// uses: gamehall.Recorder (writes moves and results) and ws.Persister (creates
// the game row behind a room). Both the MySQL store and the in-memory store
// satisfy GameStore, so the wiring is identical for either.

// GameStore is the persistence surface the game flow needs.
type GameStore interface {
	StartGame(ctx context.Context, gameID int64) error
	AddMove(ctx context.Context, gameID int64, m store.Move) error
	FinishGame(ctx context.Context, gameID int64, r store.GameResult) error
	ApplyResult(ctx context.Context, redID, blackID int64, redDelta, blackDelta int) error
	CreateGame(ctx context.Context, g store.Game) (store.Game, error)
}

// storeRecorder adapts a GameStore onto gamehall.Recorder.
type storeRecorder struct {
	api GameStore
}

func newStoreRecorder(st GameStore) *storeRecorder { return &storeRecorder{api: st} }

// StartGame marks the persisted row as playing.
func (r *storeRecorder) StartGame(ctx context.Context, gameID int64) error {
	if gameID == 0 {
		return nil
	}
	return r.api.StartGame(ctx, gameID)
}

// AddMove appends a ply. A failed insert must not roll the game back in memory,
// because both players have already seen the move, so the error is dropped
// here; the game row keeps its own counters and the failure is visible in the
// server log through the recorder's caller.
func (r *storeRecorder) AddMove(ctx context.Context, gameID int64, m gamehall.MoveRecord) {
	if gameID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = r.api.AddMove(ctx, gameID, store.Move{
		Ply:      m.Ply,
		Side:     m.Side,
		UCI:      m.UCI,
		Notation: m.Notation,
		FEN:      m.FEN,
		Check:    m.Check,
		MSSpent:  int(m.MSSpent),
	})
}

// FinishGame records the result.
func (r *storeRecorder) FinishGame(ctx context.Context, gameID int64, res gamehall.Result) {
	if gameID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = r.api.FinishGame(ctx, gameID, store.GameResult{
		Outcome:     res.Outcome,
		Reason:      res.Reason,
		WinnerSide:  res.WinnerSide,
		FEN:         res.FEN,
		MoveCount:   res.MoveCount,
		RedMSLeft:   res.RedMSLeft,
		BlackMSLeft: res.BlackMSLeft,
	})
}

// Finish applies the rating exchange.
func (r *storeRecorder) Finish(ctx context.Context, redID, blackID int64, redDelta, blackDelta int) {
	if redID == 0 && blackID == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = r.api.ApplyResult(ctx, redID, blackID, redDelta, blackDelta)
}

// storePersister creates the games row that backs a live room.
type storePersister struct {
	api GameStore
}

func newStorePersister(st GameStore) *storePersister { return &storePersister{api: st} }

// CreateGame inserts a row for a freshly seated room and returns its id.
func (p *storePersister) CreateGame(ctx context.Context, view gamehall.RoomView) (int64, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	row := store.Game{
		RedName:     orDefault(view.Red.Name, "红方"),
		BlackName:   orDefault(view.Black.Name, "黑方"),
		Status:      store.StatusPlaying,
		Outcome:     "ongoing",
		FEN:         view.FEN,
		RedMSLeft:   view.Red.MSLeft,
		BlackMSLeft: view.Black.MSLeft,
		Rated:       view.Rated,
	}
	if view.Red.UserID != 0 {
		id := view.Red.UserID
		row.RedUserID = &id
	}
	if view.Black.UserID != 0 && !view.Black.IsAI {
		id := view.Black.UserID
		row.BlackUserID = &id
	}
	if view.Black.IsAI {
		row.AILevel = view.Black.AILevel
	}

	created, err := p.api.CreateGame(ctx, row)
	if err != nil {
		return 0, err
	}
	return created.ID, nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
