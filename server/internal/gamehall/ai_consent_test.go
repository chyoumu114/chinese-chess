package gamehall

import (
	"context"
	"testing"
	"time"

	"xiangqi/internal/ai"
	"xiangqi/internal/game"
)

// aiGame 开一局人机对战，返回房间号与玩家执的一方。
func aiGame(t *testing.T, s *Session, ctx context.Context) (int64, game.Side) {
	t.Helper()
	roomID, side, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice", AILevel: ai.LevelEasy})
	if err != nil {
		t.Fatalf("join against the computer: %v", err)
	}
	return roomID, side
}

// letComputerAnswer 让电脑按引擎的搜索应一手（平时由传输层的 AI worker 做）。
func letComputerAnswer(t *testing.T, s *Session, ctx context.Context, roomID int64) {
	t.Helper()
	level, ok := s.TakeAISeat(roomID)
	if !ok {
		t.Fatalf("电脑该走了，但座位没交出来")
	}
	g, err := game.NewGameFromFEN(mustFEN(t, s, roomID))
	if err != nil {
		t.Fatalf("rebuild position: %v", err)
	}
	move, found := ai.Search(g, level, 200*time.Millisecond)
	if !found {
		t.Fatalf("电脑没有可走的棋")
	}
	if _, err := s.SubmitAIMove(ctx, roomID, move); err != nil {
		t.Fatalf("submit computer move: %v", err)
	}
}

// TestAIAcceptsDrawOffer 人机对战时玩家提和，电脑立即同意、对局判和，
// 并且不留下任何等待回应的请求。
func TestAIAcceptsDrawOffer(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID, side := aiGame(t, s, ctx)

	gotSide, res, err := s.OfferDraw(ctx, roomID, 1)
	if err != nil {
		t.Fatalf("offer draw: %v", err)
	}
	if gotSide != side {
		t.Errorf("offer by = %v, want %v", gotSide, side)
	}
	if res == nil {
		t.Fatalf("电脑应当当场同意和棋，却留下了待回应的请求")
	}
	if res.Outcome != string(game.OutcomeDraw) {
		t.Errorf("outcome = %q, want %q", res.Outcome, game.OutcomeDraw)
	}
	view, _ := s.View(roomID)
	if view.Status != StatusFinished {
		t.Errorf("status = %q, want finished", view.Status)
	}
	if view.DrawOfferedBy != "" {
		t.Errorf("不该留下待回应的和棋请求，got %q", view.DrawOfferedBy)
	}
}

// TestAIAcceptsTakeback 人机对战时玩家悔棋，电脑同意，并且局面退回到玩家落子之前
// （两个半回合），而不是只撤电脑那一步——否则电脑会把同一着再走一遍。
func TestAIAcceptsTakeback(t *testing.T) {
	s, _ := newTestSession(t)
	// 换成带 recorder 的实例：接下来说明悔棋是否回滚了持久化。
	rec := &fakeRecorder{}
	s = NewSession(s.Config(), rec)
	ctx := context.Background()
	roomID, _ := aiGame(t, s, ctx)

	openingFEN := mustFEN(t, s, roomID)

	// 玩家走一步，电脑应一步：此时轮到玩家，最后一步是电脑走的。
	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("human move: %v", err)
	}
	letComputerAnswer(t, s, ctx, roomID)

	before, _ := s.View(roomID)
	if len(before.Moves) != 2 {
		t.Fatalf("前置条件不成立：应当走了 2 步，实际 %d", len(before.Moves))
	}

	_, applied, err := s.RequestTakeback(roomID, 1)
	if err != nil {
		t.Fatalf("request takeback: %v", err)
	}
	if applied == nil {
		t.Fatalf("电脑应当当场同意悔棋，却留下了待回应的请求")
	}

	after, _ := s.View(roomID)
	if len(after.Moves) != 0 {
		t.Errorf("悔棋后着法数 = %d，应当退回玩家落子之前（0）", len(after.Moves))
	}
	if after.FEN != openingFEN {
		t.Errorf("悔棋后的局面应当回到开局：\n got %s\nwant %s", after.FEN, openingFEN)
	}
	if after.TakebackRequestedBy != "" {
		t.Errorf("不该留下待回应的悔棋请求，got %q", after.TakebackRequestedBy)
	}
	if after.Status != StatusPlaying {
		t.Errorf("status = %q, want playing", after.Status)
	}
	// 悔棋必须同时回滚持久化：否则新分支会撞上 (game_id, ply) 唯一键，
	// 后续棋步被静默丢弃，棋谱与棋盘对不上。
	if len(rec.truncations) == 0 {
		t.Error("悔棋后没有调用 TruncateMoves，持久化里仍留着被撤的棋步")
	} else if last := rec.truncations[len(rec.truncations)-1]; last.KeepPly != 0 {
		t.Errorf("应当截断到 0 步，实际 keepPly=%d", last.KeepPly)
	}
}

// TestHumanTakebackStillNeedsConsent 确保真人之间仍然是「请求 → 对手回应」，
// 没有被电脑的自动同意顺手改掉。
func TestHumanTakebackStillNeedsConsent(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("red move: %v", err)
	}
	_, applied, err := s.RequestTakeback(roomID, 2)
	if err != nil {
		t.Fatalf("request takeback: %v", err)
	}
	if applied != nil {
		t.Fatalf("真人对手不该被自动同意")
	}
	view, _ := s.View(roomID)
	if view.TakebackRequestedBy != "black" || len(view.Moves) != 1 {
		t.Fatalf("请求应当挂起等待回应：requestedBy=%q moves=%d",
			view.TakebackRequestedBy, len(view.Moves))
	}
}

// TestPendingTakebackDiesWithTheNextMove 跨回合的悔棋请求必须作废：否则对手这时再点
// 「同意」，撤掉的是刚走的这一步，而不是当初请求的那一步。
func TestPendingTakebackDiesWithTheNextMove(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")

	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("red move: %v", err)
	}
	// 黑方请求悔棋，挂起等对手回应。
	if _, applied, err := s.RequestTakeback(roomID, 2); err != nil || applied != nil {
		t.Fatalf("真人之间应当挂起等待回应，err=%v applied=%v", err, applied)
	}
	if view, _ := s.View(roomID); view.TakebackRequestedBy != "black" {
		t.Fatalf("请求应当挂起，got %q", view.TakebackRequestedBy)
	}

	// 黑方自己走了一步：局面变了，旧请求不再对应当前局面。
	if _, err := s.SubmitMove(ctx, roomID, 2, "b2", "e2"); err != nil {
		t.Fatalf("black move: %v", err)
	}
	view, _ := s.View(roomID)
	if view.TakebackRequestedBy != "" {
		t.Fatalf("跨回合之后应当作废，got %q", view.TakebackRequestedBy)
	}
	if _, err := s.RespondTakeback(roomID, 1, true); err == nil {
		t.Errorf("作废之后的「同意」应当被拒")
	}
}

// TestRejectedAIMoveReleasesTheSearchClaim 一次不被采纳的延迟着法也必须把搜索认领
// 放掉：玩家在电脑思考期间悔棋时，那步棋已经不该由它走，若认领不释放，电脑此后永远
// 轮不到走棋，棋盘就卡死了。
func TestRejectedAIMoveReleasesTheSearchClaim(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()
	roomID, _ := aiGame(t, s, ctx)

	// 玩家走一步，电脑认领搜索。
	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("human move: %v", err)
	}
	if _, ok := s.TakeAISeat(roomID); !ok {
		t.Fatalf("电脑应当认领这一步")
	}

	// 玩家在电脑思考期间悔棋：撤掉自己的着法，走子权回到玩家。
	if _, applied, err := s.RequestTakeback(roomID, 1); err != nil || applied == nil {
		t.Fatalf("对电脑悔棋应当当场同意，err=%v applied=%v", err, applied)
	}

	// 搜索这时才返回，可这一步已经不该由它走了。
	g, err := game.NewGameFromFEN(mustFEN(t, s, roomID))
	if err != nil {
		t.Fatalf("rebuild position: %v", err)
	}
	move, found := ai.Search(g, ai.LevelEasy, 100*time.Millisecond)
	if !found {
		t.Fatalf("电脑没有可走的棋")
	}
	if _, err := s.SubmitAIMove(ctx, roomID, move); err == nil {
		t.Fatalf("过期的电脑着法应当被拒绝")
	}

	// 关键：认领必须已经释放，玩家走完后电脑还能接着走。
	if _, err := s.SubmitMove(ctx, roomID, 1, "b7", "e7"); err != nil {
		t.Fatalf("human move after takeback: %v", err)
	}
	if _, ok := s.TakeAISeat(roomID); !ok {
		t.Fatalf("被拒的着法没有释放搜索认领：电脑此后再也走不了棋")
	}
}
