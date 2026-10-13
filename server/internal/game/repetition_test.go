package game

import "testing"

// 三个测试局面：
//
//	perpetualCheckFEN —— 红车在 d5/f5 之间反复将军，黑将在 e0/f0 之间闪避；
//	perpetualChaseFEN —— 红车反复追捉黑方无根的马，黑马来回躲；
//	quietRepeatFEN    —— 两车各走各的、行列都不同，既不将军也不捉子。
//
// 第三个局面刻意让两车不在同一行/列：否则会构成「一方追、一方逃」的长捉，
// 那是要判负的（见 TestPerpetualChaseLoses），不是不变作和。
const (
	perpetualCheckFEN = "5k3/9/9/9/9/3R5/9/9/9/3K5 w - - 0 1"
	perpetualChaseFEN = "4k4/9/9/9/9/2R6/n8/9/9/5K3 w - - 0 1"
	quietRepeatFEN    = "4k4/9/9/8r/9/R8/9/9/9/3K5 w - - 0 1"
)

// playSequence 按 UCI 着法名逐步走棋，任一步非法即让测试失败。
func playSequence(t *testing.T, g *Game, ucis ...string) {
	t.Helper()
	for i, uci := range ucis {
		m, err := ParseMove(uci)
		if err != nil {
			t.Fatalf("第 %d 步 %s 解析失败: %v", i+1, uci, err)
		}
		if _, err := g.Play(m); err != nil {
			t.Fatalf("第 %d 步 %s 被拒: %v（当前 FEN %s）", i+1, uci, err, g.FEN())
		}
	}
}

func newGameFrom(t *testing.T, fen string) *Game {
	t.Helper()
	g, err := NewGameFromFEN(fen)
	if err != nil {
		t.Fatalf("FEN 解析失败: %v", err)
	}
	return g
}

// TestPerpetualCheckLoses 覆盖单方长将：红方连续将军到第 5 次时才判负，
// 数到第 4 次（第 8 步）不该终局。
func TestPerpetualCheckLoses(t *testing.T) {
	g := newGameFrom(t, perpetualCheckFEN)

	// 前 8 步：红方将军 4 次，局面已重复两次——门槛是连续 5 次，所以还没到。
	playSequence(t, g,
		"d5f5", "f0e0",
		"f5e5", "e0f0",
		"e5f5", "f0e0",
		"f5e5", "e0f0",
	)
	if g.Over() {
		t.Fatalf("连续将军 4 次不该判负（原因 %v）", g.Reason())
	}

	// 第 9 步是红方第 5 次连续将军。
	playSequence(t, g, "e5f5")
	if !g.Over() {
		t.Fatalf("连续将军 5 次应当判负，当前 FEN %s", g.FEN())
	}
	if got := g.Outcome(); got != OutcomeBlackWin {
		t.Errorf("长将方判负 ⇒ 黑胜，得到 %v", got)
	}
	if got := g.Reason(); got != ReasonPerpetualCheck {
		t.Errorf("原因应为 %v，得到 %v", ReasonPerpetualCheck, got)
	}
}

// TestPerpetualChaseLoses 覆盖单方长捉：红车连续追捉黑方无根的马，同样要数满 5 次
// 才判负。局面刻意让马够不着车，所以只有红方在捉。
func TestPerpetualChaseLoses(t *testing.T) {
	g := newGameFrom(t, perpetualChaseFEN)

	// 前 8 步：红方捉了 4 次。虽然局面已经重复三次，但有一方仍在长捉（只是没数满），
	// 此时不能判和，否则规则永远走不到第 5 次。
	playSequence(t, g,
		"c5a5", "a6c7",
		"a5c5", "c7a6",
		"c5a5", "a6c7",
		"a5c5", "c7a6",
	)
	if g.Over() {
		t.Fatalf("连续捉 4 次不该终局（原因 %v）", g.Reason())
	}

	playSequence(t, g, "c5a5")
	if !g.Over() {
		t.Fatalf("三次重复后对局应当结束，当前 FEN %s", g.FEN())
	}
	if got := g.Outcome(); got != OutcomeBlackWin {
		t.Errorf("长捉方判负 ⇒ 黑胜，得到 %v", got)
	}
	if got := g.Reason(); got != ReasonPerpetualChase {
		t.Errorf("原因应为 %v，得到 %v", ReasonPerpetualChase, got)
	}
}

// TestThreefoldRepetitionIsDraw 覆盖真正的「双方不变」：两车各行其道，既不将军也
// 不捉子，第三次重复时判和。
func TestThreefoldRepetitionIsDraw(t *testing.T) {
	g := newGameFrom(t, quietRepeatFEN)

	playSequence(t, g,
		"a5a6", "i3i4",
		"a6a5", "i4i3",
		"a5a6", "i3i4",
		"a6a5", "i4i3",
	)

	if !g.Over() {
		t.Fatalf("三次重复后对局应当结束，当前 FEN %s", g.FEN())
	}
	if got := g.Outcome(); got != OutcomeDraw {
		t.Errorf("双方不变应当判和，得到 %v（原因 %v）", got, g.Reason())
	}
	if got := g.Reason(); got != ReasonRepetition {
		t.Errorf("原因应为 %v，得到 %v", ReasonRepetition, got)
	}
}

// TestRepetitionNeedsThirdOccurrence 确认两次重复不触发裁决，避免误杀正常对局。
func TestRepetitionNeedsThirdOccurrence(t *testing.T) {
	g := newGameFrom(t, quietRepeatFEN)

	playSequence(t, g, "a5a6", "i3i4", "a6a5", "i4i3")

	if g.Over() {
		t.Fatalf("第二次出现同一局面不应终局（原因 %v）", g.Reason())
	}
}

// TestUndoRollsBackRepetitionTracking 确认悔棋会把判重状态一起回退。
func TestUndoRollsBackRepetitionTracking(t *testing.T) {
	g := newGameFrom(t, quietRepeatFEN)

	playSequence(t, g,
		"a5a6", "i3i4",
		"a6a5", "i4i3",
		"a5a6", "i3i4",
		"a6a5", "i4i3",
	)
	if !g.Over() {
		t.Fatalf("前置条件不成立：应当已经判和")
	}

	if _, ok := g.Undo(); !ok {
		t.Fatalf("悔棋失败")
	}
	if g.Over() {
		t.Errorf("悔棋后不应当仍处于终局状态（原因 %v）", g.Reason())
	}
}

// TestPerpetualCheckFromARealGame 是一盘真实人机对局的回归：黑马在 c8 与 d6 之间
// 反复跳，每一步都在将军红帅（moves 表里这几步的 check 都是 1），红帅只能在 e8/e9
// 之间应招。连续将军数满 5 次时，长将方（黑）判负。
//
// 局面与着法直接取自那盘棋的 31–44 手。
func TestPerpetualCheckFromARealGame(t *testing.T) {
	g := newGameFrom(t, "r1b2ab2/1R2ak3/6n2/p5C1p/2p2C3/9/P1n1P1P1P/9/9/2BAKAB2 b - - 9 16")

	moves := []string{
		"c6d8", "b1b3", "d8f7", "e9e8", "f7d6", "e8e9",
		"d6c8", "e9e8", "c8d6", "e8e9", "d6c8", "e9e8", "c8d6",
	}

	verdictAt := 0
	var verdict RepetitionVerdict
	for i, uci := range moves {
		m, err := ParseMove(uci)
		if err != nil {
			t.Fatalf("第 %d 步 %s 解析失败: %v", i+1, uci, err)
		}
		if _, err := g.Play(m); err != nil {
			t.Fatalf("第 %d 步 %s 被拒: %v", i+1, uci, err)
		}
		if v := g.Repetition(); v.Decided {
			verdictAt, verdict = i+1, v
			break
		}
	}

	if verdictAt == 0 {
		t.Fatalf("这盘棋应当触发长将判负，却没有")
	}
	if verdict.Outcome != OutcomeDraw && verdict.Outcome == OutcomeRedWin {
		// 红胜 = 长将方（黑）判负，正是期望。
	} else if verdict.Outcome != OutcomeRedWin {
		t.Errorf("长将方（黑）判负 ⇒ 红胜，得到 %v", verdict.Outcome)
	}
	if verdict.Reason != ReasonPerpetualCheck {
		t.Errorf("原因应为 %v，得到 %v", ReasonPerpetualCheck, verdict.Reason)
	}
	if verdict.Loser != Black {
		t.Errorf("判负方应为黑（长将方），得到 %v", verdict.Loser)
	}
	if verdict.Streak < perpetualLimit {
		t.Errorf("触发时连续将军 = %d，应当达到 %d", verdict.Streak, perpetualLimit)
	}
	// 着法序号 1 对应 moves 表的第 32 手。
	t.Logf("在第 %d 个着法判负（对应 moves 表第 %d 手），连续将军 %d 次",
		verdictAt, 31+verdictAt, verdict.Streak)

	// 触发之后对局状态应当已经落地。
	if !g.Over() || g.Outcome() != OutcomeRedWin || g.Reason() != ReasonPerpetualCheck {
		t.Errorf("Play 应当已经结束对局：over=%v outcome=%v reason=%v",
			g.Over(), g.Outcome(), g.Reason())
	}
}
