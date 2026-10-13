package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"xiangqi/internal/gamehall"
)

// mustPlayingRoom 走真实路径起一局真人对局：两名棋手入座、都点准备，
// 返回房间号和两条已经挂在房间上的连接。
func mustPlayingRoom(t *testing.T, hub *Hub, session *gamehall.Session, ctx context.Context, redID, blackID int64) (int64, *Client, *Client) {
	t.Helper()
	red := fakeClient(hub, redID, fmt.Sprintf("player%d", redID))
	black := fakeClient(hub, blackID, fmt.Sprintf("player%d", blackID))
	hub.add(red)
	hub.add(black)

	roomID, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: redID, Name: red.identity.Name})
	if err != nil {
		t.Fatalf("红方入座: %v", err)
	}
	hub.attachRoom(red, roomID)
	if _, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: blackID, Name: black.identity.Name, RoomID: roomID}); err != nil {
		t.Fatalf("黑方入座: %v", err)
	}
	hub.attachRoom(black, roomID)
	for _, userID := range []int64{redID, blackID} {
		if _, err := session.Ready(ctx, roomID, userID); err != nil {
			t.Fatalf("玩家 %d 准备: %v", userID, err)
		}
	}
	view, ok := session.View(roomID)
	if !ok || view.Status != gamehall.StatusPlaying {
		t.Fatalf("房间 %d 没能进入对局中: %+v", roomID, view)
	}
	return roomID, red, black
}

// quiesce waits until a seated player has received the "playing" snapshot.
//
// 这一步同时把广播 worker 的队列排空：入座与准备产生的通知都已被处理，
// 之后再下发 watch，观战者的队列里就只会是观战自己的帧，顺序可以断言。
func quiesce(t *testing.T, c *Client) {
	t.Helper()
	waitRoom(t, c, func(v gamehall.RoomView) bool { return v.Status == gamehall.StatusPlaying }, 3*time.Second)
}

// collectUntil 按顺序读帧直到 done 为真，返回读到的全部帧（含最后一帧）。
// 与 waitUntil 的区别是它保留原始帧，测试因此可以校验帧的顺序和 payload。
func collectUntil(t *testing.T, c *Client, wait time.Duration, done func([]Envelope) bool) []Envelope {
	t.Helper()
	deadline := time.After(wait)
	var frames []Envelope
	for {
		select {
		case env := <-c.send:
			frames = append(frames, env)
			if done(frames) {
				return frames
			}
		case <-deadline:
			t.Fatalf("等待帧超时，只看到 %v", frameTypes(frames))
			return frames
		}
	}
}

// frameTypes 返回帧类型序列，用于断言与失败信息。
func frameTypes(frames []Envelope) []string {
	out := make([]string, 0, len(frames))
	for _, env := range frames {
		out = append(out, env.Type)
	}
	return out
}

// firstOf 返回某类型的第一帧。
func firstOf(frames []Envelope, kind string) (Envelope, bool) {
	for _, env := range frames {
		if env.Type == kind {
			return env, true
		}
	}
	return Envelope{}, false
}

// roomSnapshot 把 room 帧解成房间快照。
func roomSnapshot(t *testing.T, env Envelope) gamehall.RoomView {
	t.Helper()
	var view gamehall.RoomView
	if err := json.Unmarshal(env.Data, &view); err != nil {
		t.Fatalf("room 帧解不出来: %v", err)
	}
	return view
}

// waitTypedFrame 读到指定类型的帧并把它解进 v（v 为 nil 时只看类型）。
// 中间出现的其它帧被跳过，所以它既能当"等到某帧"用，也能当错误码断言用。
func waitTypedFrame(t *testing.T, c *Client, kind string, v any, wait time.Duration) {
	t.Helper()
	deadline := time.After(wait)
	for {
		select {
		case env := <-c.send:
			if env.Type != kind {
				continue
			}
			if v != nil && len(env.Data) > 0 {
				if err := json.Unmarshal(env.Data, v); err != nil {
					t.Fatalf("解 %s 帧失败: %v", kind, err)
				}
			}
			return
		case <-deadline:
			t.Fatalf("等待 %s 帧超时", kind)
			return
		}
	}
}

// assertNoMoreRoomFrames 非阻塞地清空队列，断言里面再也没有该房间的快照。
// 调用点必须已经等到棋手收到对应的广播，也就是这次 fan-out 已经完成。
func assertNoMoreRoomFrames(t *testing.T, c *Client, roomID int64) {
	t.Helper()
	for {
		select {
		case env := <-c.send:
			if env.Type != TypeRoom {
				continue
			}
			if view := roomSnapshot(t, env); view.ID == roomID {
				t.Fatalf("不该再收到房间 %d 的快照", roomID)
			}
		default:
			return
		}
	}
}

// TestWatchAttachesAsASpectatorWithoutASeat 覆盖观战的基本契约：
// watching 回执必须在 room 快照之前到达；快照的 youAre 为空（没占座）；
// 房间的座位与状态不因观战而改变；观战人数被算进快照。
func TestWatchAttachesAsASpectatorWithoutASeat(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.runBroadcasts(ctx)

	roomID, red, _ := mustPlayingRoom(t, hub, session, ctx, 1, 2)
	quiesce(t, red)

	before, ok := session.View(roomID)
	if !ok {
		t.Fatalf("房间 %d 不见了", roomID)
	}

	carol := fakeClient(hub, 3, "carol")
	hub.add(carol)
	carol.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: roomID}))

	frames := collectUntil(t, carol, 3*time.Second, func(frames []Envelope) bool {
		types := frameTypes(frames)
		return hasFrame(types, TypeWatching) && hasFrame(types, TypeRoom)
	})
	types := frameTypes(frames)

	// 回执先于快照：客户端先知道自己在观战，再拿到只读棋盘。
	if types[0] != TypeWatching {
		t.Errorf("观战的第一帧 = %q，want watching（回执要在快照之前）: %v", types[0], types)
	}
	ack := struct {
		RoomID     int64 `json:"roomId"`
		Spectating bool  `json:"spectating"`
	}{}
	if err := json.Unmarshal(frames[0].Data, &ack); err != nil {
		t.Fatalf("watching 帧解不出来: %v", err)
	}
	if ack.RoomID != roomID || !ack.Spectating {
		t.Errorf("watching 回执 = %+v, want roomId=%d spectating=true", ack, roomID)
	}

	roomEnv, ok := firstOf(frames, TypeRoom)
	if !ok {
		t.Fatalf("观众没有收到房间快照: %v", types)
	}
	view := roomSnapshot(t, roomEnv)
	if view.ID != roomID {
		t.Errorf("观众收到的快照是房间 %d, want %d", view.ID, roomID)
	}
	if view.YouAre != "" {
		t.Errorf("观众的快照 youAre = %q, want 空（观战不占座）", view.YouAre)
	}
	if view.Status != gamehall.StatusPlaying {
		t.Errorf("观众的快照 status = %q, want playing", view.Status)
	}
	if view.Spectators != 1 {
		t.Errorf("观众自己看到的 spectators = %d, want 1", view.Spectators)
	}

	// 观战不占座、不改状态：座位与房间状态必须和观战前一模一样。
	after, ok := session.View(roomID)
	if !ok {
		t.Fatalf("观战后房间 %d 不见了", roomID)
	}
	if after.Status != before.Status || after.Red.UserID != before.Red.UserID || after.Black.UserID != before.Black.UserID {
		t.Errorf("观战改动了房间: before=%+v after=%+v", before.Red, after.Red)
	}
	if _, seated := session.RoomIDForUser(3); seated {
		t.Errorf("观战者 carol 占到了座位")
	}
	hub.mu.RLock()
	_, watching := hub.byRoom[roomID][carol]
	hub.mu.RUnlock()
	if !watching {
		t.Errorf("观战者没有被登记进房间 %d 的订阅名单", roomID)
	}
	if got := hub.SpectatorCount(roomID); got != 1 {
		t.Errorf("SpectatorCount(%d) = %d, want 1", roomID, got)
	}
}

// TestWatchReceivesTheGameBroadcasts 覆盖"观众能收到后续广播"：棋手走一步后，
// 观众必须同时看到 move_made 和带这一步的房间快照。
func TestWatchReceivesTheGameBroadcasts(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.runBroadcasts(ctx)

	roomID, red, _ := mustPlayingRoom(t, hub, session, ctx, 1, 2)
	quiesce(t, red)

	carol := fakeClient(hub, 3, "carol")
	hub.add(carol)
	carol.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: roomID}))

	// 先把观战自身的回执与快照收干净，后面的断言只关于走棋广播。
	collectUntil(t, carol, 3*time.Second, func(frames []Envelope) bool {
		types := frameTypes(frames)
		return hasFrame(types, TypeWatching) && hasFrame(types, TypeRoom)
	})

	// 红方走一步（炮二平五）。
	red.handle(NewEnvelope(TypeMove, MoveRequest{From: "h7", To: "e7"}))

	types, rooms := waitUntil(t, carol, 3*time.Second, func(types []string, rooms []gamehall.RoomView) bool {
		return hasFrame(types, TypeMoveMade) && len(rooms) > 0 && len(rooms[len(rooms)-1].Moves) == 1
	})
	if !hasFrame(types, TypeMoveMade) {
		t.Fatalf("观众没收到 move_made: %v", types)
	}
	last := rooms[len(rooms)-1]
	if last.ID != roomID {
		t.Fatalf("观众收到的是房间 %d 的快照, want %d", last.ID, roomID)
	}
	if last.Moves[0].From != "h7" || last.Moves[0].To != "e7" {
		t.Errorf("观众看到的走法 = %s%s, want h7e7", last.Moves[0].From, last.Moves[0].To)
	}
	if last.Moves[0].Notation != "炮二平五" {
		t.Errorf("观众看到的记谱 = %q, want 炮二平五", last.Moves[0].Notation)
	}
	if last.Status != gamehall.StatusPlaying || last.YouAre != "" {
		t.Errorf("观众快照 status=%q youAre=%q, want playing/空", last.Status, last.YouAre)
	}
}

// TestWatchIsRefusedWhileSeatedInAnotherRoom 覆盖观战的拒绝路径：
// 已坐在另一个未结束房间里的人发 watch 会被拒绝，错误码 already_seated，
// 而且这次拒绝不能把连接挂到被拒绝的房间上；游客可以观战。
func TestWatchIsRefusedWhileSeatedInAnotherRoom(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.runBroadcasts(ctx)

	// 先起对局中的房间 B，这样它的匹配把等待队列清空；随后 alice 的快速
	// 匹配才会开出一个新的、只有一个座位的房间 A。
	roomB, _, _ := mustPlayingRoom(t, hub, session, ctx, 20, 21)
	roomA, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 10, Name: "alice"})
	if err != nil {
		t.Fatalf("alice 入座: %v", err)
	}
	if roomA == roomB {
		t.Fatalf("alice 落到了对局中的房间 %d，测试前提不成立", roomA)
	}
	alice := fakeClient(hub, 10, "alice")
	hub.add(alice)
	hub.attachRoom(alice, roomA)

	// alice 坐在未结束的房间 A 里，不能观战房间 B。
	alice.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: roomB}))
	var ev ErrorView
	waitTypedFrame(t, alice, TypeError, &ev, 3*time.Second)
	if ev.Code != "already_seated" {
		t.Errorf("拒绝观战的错误码 = %q, want already_seated（%s）", ev.Code, ev.Message)
	}
	hub.mu.RLock()
	_, attachedB := hub.byRoom[roomB][alice]
	hub.mu.RUnlock()
	if attachedB {
		t.Errorf("被拒绝的 watch 仍然把连接挂到了房间 %d", roomB)
	}
	if id, ok := session.RoomIDForUser(10); !ok || id != roomA {
		t.Errorf("alice 的座位被观战改动了: room=%d ok=%v, want %d", id, ok, roomA)
	}

	// 观战自己所在的房间是允许的：不会换掉座位，只是重新订阅。
	alice.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: roomA}))
	aliceFrames := collectUntil(t, alice, 3*time.Second, func(frames []Envelope) bool {
		types := frameTypes(frames)
		return hasFrame(types, TypeWatching) && hasFrame(types, TypeRoom)
	})
	ownEnv, _ := firstOf(aliceFrames, TypeRoom)
	if own := roomSnapshot(t, ownEnv); own.ID != roomA {
		t.Errorf("观战自己的房间收到 %d 的快照, want %d", own.ID, roomA)
	}

	// 没登录的游客也能观战（观战不需要账号），而且没有座位。
	guest := fakeClient(hub, 0, "guest")
	hub.add(guest)
	guest.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: roomB}))
	guestFrames := collectUntil(t, guest, 3*time.Second, func(frames []Envelope) bool {
		types := frameTypes(frames)
		return hasFrame(types, TypeWatching) && hasFrame(types, TypeRoom)
	})
	guestEnv, _ := firstOf(guestFrames, TypeRoom)
	if view := roomSnapshot(t, guestEnv); view.YouAre != "" || view.ID != roomB {
		t.Errorf("游客的快照 = room %d youAre %q, want room %d 且 youAre 为空", view.ID, view.YouAre, roomB)
	}

	// 不存在的房间号和空房间号都是错误，不能静默成功。
	guest.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: 9999}))
	waitTypedFrame(t, guest, TypeError, &ev, 3*time.Second)
	if ev.Code != "room_not_found" {
		t.Errorf("观战不存在的房间: code=%q, want room_not_found", ev.Code)
	}
	guest.handle(NewEnvelope(TypeWatch, WatchRequest{}))
	waitTypedFrame(t, guest, TypeError, &ev, 3*time.Second)
	if ev.Code != "bad_payload" {
		t.Errorf("观战缺少房间号: code=%q, want bad_payload", ev.Code)
	}
}

// TestSpectatorLeaveDoesNotEndTheGame 覆盖观众退出的契约：不发判负、不改动
// 房间状态与座位，只是不再收到这个房间的帧。
func TestSpectatorLeaveDoesNotEndTheGame(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.runBroadcasts(ctx)

	roomID, red, _ := mustPlayingRoom(t, hub, session, ctx, 1, 2)
	quiesce(t, red)
	before, _ := session.View(roomID)

	carol := fakeClient(hub, 3, "carol")
	hub.add(carol)
	carol.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: roomID}))
	collectUntil(t, carol, 3*time.Second, func(frames []Envelope) bool {
		types := frameTypes(frames)
		return hasFrame(types, TypeWatching) && hasFrame(types, TypeRoom)
	})

	// 观众 leave。
	carol.handle(Envelope{Type: TypeLeave})
	var left LeftView
	waitTypedFrame(t, carol, TypeLeft, &left, 3*time.Second)
	if left.Dissolved {
		t.Errorf("观众退出不该解散房间: %+v", left)
	}

	// 对局与座位必须完全没变：没有判负、没有结果、座位照旧。
	after, ok := session.View(roomID)
	if !ok {
		t.Fatalf("观众退出后房间 %d 不见了", roomID)
	}
	if after.Status != gamehall.StatusPlaying || after.Result != nil {
		t.Errorf("观众退出改动了对局: status=%q result=%+v, want playing/无结果", after.Status, after.Result)
	}
	if after.Red.UserID != before.Red.UserID || after.Black.UserID != before.Black.UserID {
		t.Errorf("观众退出改动了座位: 红 %d/%d 黑 %d/%d",
			after.Red.UserID, before.Red.UserID, after.Black.UserID, before.Black.UserID)
	}
	hub.mu.RLock()
	_, watching := hub.byRoom[roomID][carol]
	hub.mu.RUnlock()
	if watching {
		t.Errorf("观众退出后仍然订阅着房间 %d", roomID)
	}
	if got := hub.SpectatorCount(roomID); got != 0 {
		t.Errorf("观众退出后 SpectatorCount(%d) = %d, want 0", roomID, got)
	}

	// 棋手再走一步；以棋手收到带这一步的快照为同步点，说明广播已经发出，
	// 此时观众队列里不该出现该房间的任何帧。
	red.handle(NewEnvelope(TypeMove, MoveRequest{From: "h7", To: "e7"}))
	waitUntil(t, red, 3*time.Second, func(_ []string, rooms []gamehall.RoomView) bool {
		return len(rooms) > 0 && len(rooms[len(rooms)-1].Moves) == 1
	})
	assertNoMoreRoomFrames(t, carol, roomID)
}

// TestSpectatorCountExcludesSeatedPlayers 覆盖 hub.SpectatorCount 的定义：
// 订阅该房间的连接数减去占用座位的棋手数，下限 0。
func TestSpectatorCountExcludesSeatedPlayers(t *testing.T) {
	session := gamehall.NewSession(gamehall.Config{TurnTimeout: time.Minute}, nil)
	hub := newTestHub(t, session)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.runBroadcasts(ctx)

	// 一个只坐了一个人的房间：一个订阅连接 = 一个棋手 → 0（不能被减成负数）。
	roomID, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 1, Name: "alice"})
	if err != nil {
		t.Fatalf("alice 入座: %v", err)
	}
	alice := fakeClient(hub, 1, "alice")
	hub.add(alice)
	hub.attachRoom(alice, roomID)
	if got := hub.SpectatorCount(roomID); got != 0 {
		t.Errorf("只有一个棋手时 SpectatorCount = %d, want 0（下限）", got)
	}

	// 第二个棋手入座，仍然是 0：棋手自己不算观众。
	if _, _, err := session.Join(ctx, gamehall.JoinRequest{UserID: 2, Name: "bob", RoomID: roomID}); err != nil {
		t.Fatalf("bob 入座: %v", err)
	}
	bob := fakeClient(hub, 2, "bob")
	hub.add(bob)
	hub.attachRoom(bob, roomID)
	if got := hub.SpectatorCount(roomID); got != 0 {
		t.Errorf("两名棋手时 SpectatorCount = %d, want 0", got)
	}

	// 两名观众：订阅连接数 4 − 座位数 2 = 2。
	for i, id := range []int64{3, 4} {
		viewer := fakeClient(hub, id, fmt.Sprintf("viewer%d", id))
		hub.add(viewer)
		viewer.handle(NewEnvelope(TypeWatch, WatchRequest{RoomID: roomID}))
		if got, want := hub.SpectatorCount(roomID), i+1; got != want {
			t.Errorf("第 %d 名观众之后 SpectatorCount = %d, want %d", i+1, got, want)
		}
	}

	// 没人订阅的房间：0。
	if got := hub.SpectatorCount(9999); got != 0 {
		t.Errorf("没人订阅的房间 SpectatorCount = %d, want 0", got)
	}
}
