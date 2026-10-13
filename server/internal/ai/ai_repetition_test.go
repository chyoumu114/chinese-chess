package ai

import (
	"testing"
	"time"

	"xiangqi/internal/game"
)

// perpetualCheckSetup 是长将测试的公共局面与走法：红车 d5、黑将 f0、红帅 d9。
// 走满 8 步后红车停在 e5，此时再走 e5f5 就是第三次重复 —— 红方长将判负。
const checkFEN = "5k3/9/9/9/9/3R5/9/9/9/3K5 w - - 0 1"

var checkSetup = []string{"d5f5", "f0e0", "f5e5", "e0f0", "e5f5", "f0e0", "f5e5", "e0f0"}

// playAll 逐步走棋，任一步被拒即失败。
func playAll(t *testing.T, g *game.Game, ucis []string) {
	t.Helper()
	for _, uci := range ucis {
		m, err := game.ParseMove(uci)
		if err != nil {
			t.Fatalf("解析 %s: %v", uci, err)
		}
		if _, err := g.Play(m); err != nil {
			t.Fatalf("走 %s: %v（当前 FEN %s）", uci, err, g.FEN())
		}
	}
}

// TestSearchAvoidsPerpetualCheckLoss 确认电脑不会走进「长将判负」。
//
// 同一局面已经出现两次，红方再走 e5f5 就构成第三次重复、按规则判负。搜索必须
// 避开它，而不是继续「将军看起来不亏」的老路。
func TestSearchAvoidsPerpetualCheckLoss(t *testing.T) {
	g, err := game.NewGameFromFEN(checkFEN)
	if err != nil {
		t.Fatalf("FEN 解析失败: %v", err)
	}
	playAll(t, g, checkSetup)
	if g.Over() {
		t.Fatalf("前置条件不成立：两次重复不该终局（原因 %v）", g.Reason())
	}

	// 先确认 e5f5 确实会把红方走死，否则这个测试没有意义。
	fatal, err := game.ParseMove("e5f5")
	if err != nil {
		t.Fatalf("解析 e5f5: %v", err)
	}
	probe := g.Clone()
	if _, err := probe.Play(fatal); err != nil {
		t.Fatalf("e5f5 应当合法: %v", err)
	}
	if probe.Reason() != game.ReasonPerpetualCheck {
		t.Fatalf("前置条件不成立：e5f5 之后原因应为 %v，得到 %v",
			game.ReasonPerpetualCheck, probe.Reason())
	}

	mv, ok := Search(g, LevelHard, time.Second)
	if !ok {
		t.Fatalf("搜索没有给出着法")
	}
	if mv.FromName() == fatal.FromName() && mv.ToName() == fatal.ToName() {
		t.Fatalf("电脑走了 %s，这会把自己走成长将判负", mv.String())
	}
}

// TestSearchSeesDrawByRepetition 确认第三次重复被识别为和棋而不是胜负。
func TestSearchSeesDrawByRepetition(t *testing.T) {
	g, err := game.NewGameFromFEN("4k4/9/9/8r/9/R8/9/9/9/3K5 w - - 0 1")
	if err != nil {
		t.Fatalf("FEN 解析失败: %v", err)
	}
	playAll(t, g, []string{"a5a6", "i3i4", "a6a5", "i4i3", "a5a6", "i3i4", "a6a5"})

	m, err := game.ParseMove("i4i3")
	if err != nil {
		t.Fatalf("解析 i4i3: %v", err)
	}
	probe := g.Clone()
	probe.ApplyUnchecked(m)
	v := probe.Repetition()
	if !v.Decided || v.Outcome != game.OutcomeDraw {
		t.Fatalf("第三次重复应当判和，得到 decided=%v outcome=%v", v.Decided, v.Outcome)
	}
}
