package ws

import (
	"context"
	"testing"
	"time"

	"xiangqi/internal/gamehall"
)

// presenceOf 取出大厅名单里某个用户的条目，找不到直接判失败。
func presenceOf(t *testing.T, view PresenceView, userID int64) PresenceEntry {
	t.Helper()
	for _, entry := range view.Online {
		if entry.UserID == userID {
			return entry
		}
	}
	t.Fatalf("用户 %d 不在在线名单里: %+v", userID, view.Online)
	return PresenceEntry{}
}

// checkPresence 连续多轮断言大厅名单。
//
// 状态必须只取决于房间的真实状态，与 hub 内 map 的遍历顺序无关。老实现是
// "按连接数统计"：遍历 hub 的连接 map 看谁连了几个房间，于是同一房间里的
// 两个人谁被算成 playing、谁被算成 waiting 完全取决于那一轮 map 的遍历
// 顺序。单次断言有可能恰好落在正确的顺序上，所以这里重复多轮，把顺序相关
// 的回归放大成稳定失败。
func checkPresence(t *testing.T, hub *Hub, rounds int, want func(t *testing.T, view PresenceView)) {
	t.Helper()
	for i := 0; i < rounds; i++ {
		want(t, hub.presenceView())
	}
}

// TestPresenceStatusFollowsTheRoomState 是"在线状态必须来自房间真实状态"的
// 回归测试，一次覆盖规格里的四种状态：
//
//	对局进行中           → playing（两人都是）
//	双方入座但都没点准备 → waiting（两人都是）
//	没有进任何房间       → online
//	只是在观战           → online（观战不改变状态）
//
// 每个场景都重复断言 32 轮，专门盯住"同一房间的两个人状态不一致"这个 bug。
func TestPresenceStatusFollowsTheRoomState(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 广播 worker 必须跑起来，这样挂到房间上的连接会收到真实的广播帧；
	// 在线状态本身是同步计算的，不依赖它，但不启动 worker 会让房间帧堆积。
	go hub.runBroadcasts(ctx)

	alice := fakeClient(hub, 1, "alice")
	bob := fakeClient(hub, 2, "bob")
	carol := fakeClient(hub, 3, "carol")
	hub.add(alice)
	hub.add(bob)
	hub.add(carol)

	// 场景一：alice 一个人在房间里等对手，连上但没进过任何房间的 carol。
	// 房间真实状态是 waiting，alice 就必须是 waiting；carol 是 online。
	roomID, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("alice 入座: %v", err)
	}
	hub.attachRoom(alice, roomID)

	checkPresence(t, hub, 32, func(t *testing.T, view PresenceView) {
		if got := presenceOf(t, view, 1); got.Status != StatusWaiting || got.RoomID != roomID {
			t.Fatalf("房间里只有 alice 时: status=%q roomId=%d, want waiting/%d", got.Status, got.RoomID, roomID)
		}
		if got := presenceOf(t, view, 3); got.Status != StatusOnline || got.RoomID != 0 {
			t.Fatalf("没有入座的 carol: status=%q roomId=%d, want online/0", got.Status, got.RoomID)
		}
	})

	// 场景二：bob 也入座，双方都没点准备（房间 status=ready）→ 两人都 waiting。
	// 这正是老 bug 的现场：同一个房间挂了两个连接，两人的状态跟着 map 遍历
	// 顺序抖动，一个显示 waiting、一个显示 playing。
	if _, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 2, Name: "bob"}); err != nil {
		t.Fatalf("bob 入座: %v", err)
	}
	hub.attachRoom(bob, roomID)
	if view, ok := session.View(roomID); !ok || view.Status != gamehall.StatusReady {
		t.Fatalf("双方入座后房间 = %+v, want status=ready", view)
	}

	checkPresence(t, hub, 32, func(t *testing.T, view PresenceView) {
		for _, userID := range []int64{1, 2} {
			if got := presenceOf(t, view, userID); got.Status != StatusWaiting || got.RoomID != roomID {
				t.Fatalf("双方入座未准备时 用户%d: status=%q roomId=%d, want waiting/%d",
					userID, got.Status, got.RoomID, roomID)
			}
		}
		if got := presenceOf(t, view, 3); got.Status != StatusOnline {
			t.Fatalf("没有入座的 carol: status=%q, want online", got.Status)
		}
	})

	// 场景三：双方都点准备，对局进行中 → 两人都必须是 playing，房间号一致。
	for _, userID := range []int64{1, 2} {
		if _, err := session.Ready(ctx, roomID, userID); err != nil {
			t.Fatalf("玩家 %d 准备: %v", userID, err)
		}
	}
	if view, ok := session.View(roomID); !ok || view.Status != gamehall.StatusPlaying {
		t.Fatalf("双方准备后房间 = %+v, want status=playing", view)
	}

	checkPresence(t, hub, 32, func(t *testing.T, view PresenceView) {
		for _, userID := range []int64{1, 2} {
			if got := presenceOf(t, view, userID); got.Status != StatusPlaying || got.RoomID != roomID {
				t.Fatalf("对局中 用户%d: status=%q roomId=%d, want playing/%d",
					userID, got.Status, got.RoomID, roomID)
			}
		}
		if got := presenceOf(t, view, 3); got.Status != StatusOnline {
			t.Fatalf("没有入座的 carol: status=%q, want online", got.Status)
		}
	})

	// 场景四：carol 观战这个房间 → 她仍然是 online，观战不改变状态；
	// 两个棋手仍然都是 playing。
	carol.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: roomID}))

	checkPresence(t, hub, 32, func(t *testing.T, view PresenceView) {
		for _, userID := range []int64{1, 2} {
			if got := presenceOf(t, view, userID); got.Status != StatusPlaying || got.RoomID != roomID {
				t.Fatalf("有观众时 用户%d: status=%q roomId=%d, want playing/%d",
					userID, got.Status, got.RoomID, roomID)
			}
		}
		if got := presenceOf(t, view, 3); got.Status != StatusOnline || got.RoomID != 0 {
			t.Fatalf("观战中的 carol: status=%q roomId=%d, want online/0（观战不改变状态）",
				got.Status, got.RoomID)
		}
	})

	// 场景五：alice 在对局中离开（判负、房间 finished 且空出红座）→ 她回到
	// online。bob 还在一个已结束的房间里：presenceStatus 只把
	// playing/waiting/ready 映射成房间状态，已结束的房间按 online 上报。
	alice.handle(Envelope{Type: TypeLeave})

	checkPresence(t, hub, 32, func(t *testing.T, view PresenceView) {
		if got := presenceOf(t, view, 1); got.Status != StatusOnline || got.RoomID != 0 {
			t.Fatalf("离开房间后的 alice: status=%q roomId=%d, want online/0", got.Status, got.RoomID)
		}
		if got := presenceOf(t, view, 2); got.Status != StatusOnline {
			t.Fatalf("房间已结束的 bob: status=%q, want online", got.Status)
		}
		// 观战者本来就没座位，离开房间的棋手也不该把房间从名单里带走。
		if got := presenceOf(t, view, 3); got.Status != StatusOnline || got.RoomID != 0 {
			t.Fatalf("观战中的 carol: status=%q roomId=%d, want online/0", got.Status, got.RoomID)
		}
	})
}
