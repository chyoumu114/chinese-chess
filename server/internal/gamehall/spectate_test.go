package gamehall

import (
	"context"
	"errors"
	"testing"

	"xiangqi/internal/game"
)

// TestJoinSpecificFillsTheSeatFreedMidGame 覆盖"补位入座"：一方在对局中
// 离开 → 对局判负结束、房间留下一个空位 → 第三方用 join {roomId} 必须能坐上
// 这个空位（以前一律报 room_full / finished）；满座的已结束房间仍然拒绝。
func TestJoinSpecificFillsTheSeatFreedMidGame(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	roomID := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	if _, err := s.SubmitMove(ctx, roomID, 1, "h7", "e7"); err != nil {
		t.Fatalf("红方走棋: %v", err)
	}

	// 红方在对局中离开：判负、房间结束、红座空出来。
	out := s.Leave(ctx, roomID, 1)
	if out.Result == nil {
		t.Fatalf("对局中离开必须产生结果")
	}
	if out.Result.WinnerSide != "black" || out.Result.Reason != string(game.ReasonAbandoned) {
		t.Fatalf("结果 = %+v, want 黑方因对手逃跑获胜", out.Result)
	}
	view, ok := s.View(roomID)
	if !ok {
		t.Fatalf("对手还在房间里，房间不该消失")
	}
	if view.Status != StatusFinished {
		t.Fatalf("status = %q, want finished", view.Status)
	}
	if view.Red.UserID != 0 || view.Black.UserID != 2 {
		t.Fatalf("座位 = 红 %+v / 黑 %+v, want 红座空出、黑座仍是 bob", view.Red, view.Black)
	}
	if rooms := s.PlayingRooms(); len(rooms) != 0 {
		t.Errorf("已结束的房间不该出现在对战中列表: %+v", rooms)
	}

	// 第三方补位：只能靠房间号进来，join 必须成功而不是被 ErrGameFinished /
	// ErrRoomFull 挡回去。
	id, side, err := s.Join(ctx, JoinRequest{UserID: 3, Name: "carol", RoomID: roomID})
	if err != nil {
		t.Fatalf("补位入座被拒绝: %v（want 坐上那个空出来的红座）", err)
	}
	if id != roomID || side != game.Red {
		t.Fatalf("补位返回 room=%d side=%v, want room=%d side=red", id, side, roomID)
	}
	if got, seated := s.RoomIDForUser(3); !seated || got != roomID {
		t.Errorf("补位的玩家没有被登记到房间 %d: room=%d seated=%v", roomID, got, seated)
	}
	view, _ = s.View(roomID)
	if view.Red.UserID != 3 || view.Black.UserID != 2 {
		t.Fatalf("补位后的座位 = 红 %d / 黑 %d, want 3/2", view.Red.UserID, view.Black.UserID)
	}
	// 补位只补空位，不自动开局：房间仍停在结束态，等双方点准备。
	if view.Status != StatusFinished {
		t.Fatalf("补位后 status = %q, want finished（等双方准备）", view.Status)
	}
	// 满座（也已经是结束态）之后再来一个人：仍然拒绝。
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 4, Name: "dave", RoomID: roomID}); !errors.Is(err, ErrGameFinished) {
		t.Fatalf("满座的已结束房间: got %v, want ErrGameFinished", err)
	}

	// 双方点准备：同一房间、同一张桌子，红黑互换重开。
	mustStart(t, s, ctx, roomID, 3, 2)
	view, _ = s.View(roomID)
	if view.Status != StatusPlaying {
		t.Fatalf("双方准备后 status = %q, want playing", view.Status)
	}
	if view.Red.UserID != 2 || view.Black.UserID != 3 {
		t.Fatalf("重开没有换边: 红 %d / 黑 %d, want 2/3", view.Red.UserID, view.Black.UserID)
	}
	if len(view.Moves) != 0 || view.Result != nil {
		t.Errorf("重开没有清盘: moves=%d result=%+v", len(view.Moves), view.Result)
	}
}

// TestJoinSpecificStillRefusesFullRooms 是补位改动的边界回归：补位只对"有空
// 座的已结束房间"开放，满座照旧拒绝，而且拒绝的语义要区分 —— 对局中满座是
// room_full，已结束满座是 finished。
func TestJoinSpecificStillRefusesFullRooms(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	playing := mustRoom(t, s, ctx, 1, "alice", 2, "bob")
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 3, Name: "carol", RoomID: playing}); !errors.Is(err, ErrRoomFull) {
		t.Fatalf("对局中满座的房间: got %v, want ErrRoomFull", err)
	}
	if _, err := s.Resign(ctx, playing, 1); err != nil {
		t.Fatalf("认输: %v", err)
	}
	// 双方都还坐在座位上，房间结束但满座：第三方仍然进不来。
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 3, Name: "carol", RoomID: playing}); !errors.Is(err, ErrGameFinished) {
		t.Fatalf("已结束满座的房间: got %v, want ErrGameFinished", err)
	}
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 3, Name: "carol", RoomID: 999}); !errors.Is(err, ErrRoomNotFound) {
		t.Fatalf("未知房间号: got %v, want ErrRoomNotFound", err)
	}
}

// TestPlayingRoomsListsOnlyRunningGamesNewestFirst 覆盖大厅的"对战中"列表：
// 只包含 status=playing 的房间，MoveCount 等于已走着法数，按房间号从新到旧。
func TestPlayingRoomsListsOnlyRunningGamesNewestFirst(t *testing.T) {
	s, _ := newTestSession(t)
	ctx := context.Background()

	if rooms := s.PlayingRooms(); len(rooms) != 0 {
		t.Fatalf("空 session 的对战中列表 = %+v, want 空", rooms)
	}

	// 房间一：双方入座但都没点准备 → 不是对局中，不进列表。
	readyRoom, _, err := s.Join(ctx, JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("alice 入座: %v", err)
	}
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 2, Name: "bob"}); err != nil {
		t.Fatalf("bob 入座: %v", err)
	}
	if view, ok := s.View(readyRoom); !ok || view.Status != StatusReady {
		t.Fatalf("房间 %d = %+v, want status=ready", readyRoom, view)
	}

	// 房间二：对局中，已经走了两步。
	playing1 := mustRoom(t, s, ctx, 3, "carol", 4, "dave")
	if _, err := s.SubmitMove(ctx, playing1, 3, "h7", "e7"); err != nil {
		t.Fatalf("红方走棋: %v", err)
	}
	if _, err := s.SubmitMove(ctx, playing1, 4, "h2", "h6"); err != nil {
		t.Fatalf("黑方走棋: %v", err)
	}

	// 房间三：更新的对局，一步没走 → 排序上必须排在房间二前面。
	playing2 := mustRoom(t, s, ctx, 5, "erin", 6, "frank")
	if playing2 <= playing1 {
		t.Fatalf("测试前提不成立：房间号没有递增 %d/%d", playing1, playing2)
	}

	// 房间四：对局已结束 → 不进列表。
	finished := mustRoom(t, s, ctx, 7, "gina", 8, "henry")
	if _, err := s.Resign(ctx, finished, 7); err != nil {
		t.Fatalf("认输: %v", err)
	}

	// 房间五：只有一个人在等对手 → 不进列表。它必须在最后创建，否则后面的
	// 快速匹配会直接把它配对掉。
	if _, _, err := s.Join(ctx, JoinRequest{UserID: 9, Name: "iris"}); err != nil {
		t.Fatalf("iris 入座: %v", err)
	}

	rooms := s.PlayingRooms()
	if len(rooms) != 2 {
		t.Fatalf("对战中的房间 = %+v, want 只有两个", rooms)
	}
	if rooms[0].ID != playing2 || rooms[1].ID != playing1 {
		t.Fatalf("排序 = [%d %d], want 从新到旧 [%d %d]", rooms[0].ID, rooms[1].ID, playing2, playing1)
	}
	if rooms[1].MoveCount != 2 {
		t.Errorf("房间 %d 的 MoveCount = %d, want 2", rooms[1].ID, rooms[1].MoveCount)
	}
	if rooms[0].MoveCount != 0 {
		t.Errorf("房间 %d 的 MoveCount = %d, want 0", rooms[0].ID, rooms[0].MoveCount)
	}
	if rooms[1].RedName != "carol" || rooms[1].BlackName != "dave" {
		t.Errorf("房间 %d 的棋手 = %q/%q, want carol/dave", rooms[1].ID, rooms[1].RedName, rooms[1].BlackName)
	}
	if !rooms[1].Rated {
		t.Errorf("房间 %d 应该是计分局", rooms[1].ID)
	}
	for _, r := range rooms {
		if r.ID == readyRoom || r.ID == finished {
			t.Errorf("不该出现在对战中列表里的房间 %d: %+v", r.ID, r)
		}
	}
}
