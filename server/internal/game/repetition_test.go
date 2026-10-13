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

// TestPerpetualCheckLoses 覆盖单方长将：第 9 步后局面第三次重复，红方判负。
func TestPerpetualCheckLoses(t *testing.T) {
	g := newGameFrom(t, perpetualCheckFEN)

	playSequence(t, g,
		"d5f5", "f0e0",
		"f5e5", "e0f0",
		"e5f5", "f0e0",
		"f5e5", "e0f0",
		"e5f5",
	)

	if !g.Over() {
		t.Fatalf("三次重复后对局应当结束，当前 FEN %s", g.FEN())
	}
	if got := g.Outcome(); got != OutcomeBlackWin {
		t.Errorf("长将方判负 ⇒ 黑胜，得到 %v", got)
	}
	if got := g.Reason(); got != ReasonPerpetualCheck {
		t.Errorf("原因应为 %v，得到 %v", ReasonPerpetualCheck, got)
	}
}

// TestPerpetualChaseLoses 覆盖单方长捉：红车追捉无根的马，黑马只能躲，第 8 步后
// 局面第三次重复，红方判负。局面刻意让马够不着车，所以只有红方在捉。
func TestPerpetualChaseLoses(t *testing.T) {
	g := newGameFrom(t, perpetualChaseFEN)

	playSequence(t, g,
		"c5a5", "a6c7",
		"a5c5", "c7a6",
		"c5a5", "a6c7",
		"a5c5", "c7a6",
	)

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
