package game

// 本文件实现局面重复的裁决：长将、长捉与不变作和。
//
// 中国象棋竞赛规则把「单方长将」「单方长捉」列为不变作负的行为，落到工程上是
// 三件事：
//
//  1. 判重 —— 局面键只取「棋子摆放 + 走子方」，不含半步数与回合数，所以同一个
//     局面无论绕多少圈、花了多少回合，都是同一个键；
//  2. 归责 —— 某个局面第三次出现时，取最近一个循环（该键上一次出现到这一次之间
//     的全部着法），看谁每一步都在将军（长将）或每一步都在威胁白吃对方一个大子
//     （长捉）；
//  3. 裁决 —— 只有一方长将或长捉，该方判负；双方都长将/长捉，或双方都不变，
//     判和。
//
// 记录刻意用自己的一份 marks，而不是复用 records：AI 与 perft 走棋用的是
// ApplyUnchecked，它只动棋盘、不追加 records。若判重依赖 records 的下标，就会
// 在搜索里越界。

// boardSquares 是棋盘格数；局面键多一位用来存走子方。
const boardSquares = Rows * Cols

// positionKey 是局面的紧凑键：90 个格子的棋子编码 + 走子方，可直接用作 map 键。
type positionKey [boardSquares + 1]byte

// positionKey 生成当前局面的键。它刻意不含半步数与回合数：判重问的是「棋型和谁
// 该走是否一样」，不是「这是第几回合」。
func (b *Board) positionKey() positionKey {
	var k positionKey
	for i, p := range b.squares {
		k[i] = byte(p.Side)<<4 | byte(p.Type)
	}
	k[boardSquares] = byte(b.Turn)
	return k
}

// plyMark 是判重所需的一步棋的摘要：着法本身、落地后的局面键、走子方，以及这一步
// 是否将了对方的军。
type plyMark struct {
	move  Move
	key   positionKey
	side  Side
	check bool
}

// RepetitionVerdict 是一次重复局面裁决的结果。
type RepetitionVerdict struct {
	// Decided 为 true 表示这个局面应当终局。
	Decided bool
	Outcome Outcome
	Reason  Reason
	// Loser 是判负的一方；判和时无意义，调用方应以 Outcome 为准。
	Loser Side
	// Cycle 是触发裁决的循环长度（步数），便于测试与排障。
	Cycle int
}

// initTracking 建立判重所需的初始记录：当前局面记为第一次出现。所有构造 Game
// 的路径都必须调用它。
func (g *Game) initTracking() {
	key := g.board.positionKey()
	g.seen = map[positionKey]int{key: 1}
	g.marks = nil
}

// recordPly 在一步棋落地后登记局面，并记下这一步是谁走的、有没有将军。
func (g *Game) recordPly(m Move) {
	key := g.board.positionKey()
	if g.seen == nil {
		g.seen = make(map[positionKey]int, 8)
	}
	g.seen[key]++
	g.marks = append(g.marks, plyMark{
		move: m,
		key:  key,
		// 棋盘已经把走子权交给了对方，所以走这一步的是对方。
		side:  g.board.Turn.Opponent(),
		check: g.board.InCheck(g.board.Turn),
	})
}

// dropPosition 在悔棋时撤销一次登记，让判重状态跟着棋盘一起回退。
func (g *Game) dropPosition() {
	if len(g.marks) == 0 {
		return
	}
	mark := g.marks[len(g.marks)-1]
	g.marks = g.marks[:len(g.marks)-1]
	if g.seen == nil {
		return
	}
	if g.seen[mark.key] <= 1 {
		delete(g.seen, mark.key)
		return
	}
	g.seen[mark.key]--
}

// Repetition 检查当前局面是否已因三次重复而应当终局。它不修改 Game，因此 AI 在
// 搜索里也能安全地调用它判断某条线路会不会把自己走成长将判负。
func (g *Game) Repetition() RepetitionVerdict {
	if g.seen == nil || len(g.marks) == 0 {
		return RepetitionVerdict{}
	}
	last := g.marks[len(g.marks)-1]
	if g.seen[last.key] < 3 {
		return RepetitionVerdict{}
	}
	// 上一个同局面的位置：两者之间就是一个完整的循环。
	prev := -1
	for i := len(g.marks) - 2; i >= 0; i-- {
		if g.marks[i].key == last.key {
			prev = i
			break
		}
	}
	if prev < 0 {
		// 当前局面只在开头出现过（marks 不含开局），两次出现不足以裁决。
		return RepetitionVerdict{}
	}
	cycle := g.marks[prev+1:]
	if len(cycle) == 0 {
		return RepetitionVerdict{}
	}

	redSteps, blackSteps := 0, 0
	redChecks, blackChecks := 0, 0
	for _, mark := range cycle {
		if mark.side == Red {
			redSteps++
			if mark.check {
				redChecks++
			}
			continue
		}
		blackSteps++
		if mark.check {
			blackChecks++
		}
	}
	// 「每一步都在将军」才算长将；某一方在循环里一步未走则不成立。
	redPerpetual := redSteps > 0 && redChecks == redSteps
	blackPerpetual := blackSteps > 0 && blackChecks == blackSteps

	switch {
	case redPerpetual && !blackPerpetual:
		return RepetitionVerdict{true, OutcomeBlackWin, ReasonPerpetualCheck, Red, len(cycle)}
	case blackPerpetual && !redPerpetual:
		return RepetitionVerdict{true, OutcomeRedWin, ReasonPerpetualCheck, Black, len(cycle)}
	case redPerpetual && blackPerpetual:
		// 双方都长将：不变作和。
		return RepetitionVerdict{true, OutcomeDraw, ReasonRepetition, Red, len(cycle)}
	}

	redChase := redSteps > 0 && g.cycleChases(cycle, Red)
	blackChase := blackSteps > 0 && g.cycleChases(cycle, Black)
	switch {
	case redChase && !blackChase:
		return RepetitionVerdict{true, OutcomeBlackWin, ReasonPerpetualChase, Red, len(cycle)}
	case blackChase && !redChase:
		return RepetitionVerdict{true, OutcomeRedWin, ReasonPerpetualChase, Black, len(cycle)}
	}
	// 双方都不变、都长捉：不变作和。
	return RepetitionVerdict{true, OutcomeDraw, ReasonRepetition, Red, len(cycle)}
}

// cycleChases 报告某一方在循环里的每一步之后是否都在「捉」对方。中间局面已经不在
// 棋盘上，所以先悔棋回到循环起点，再正向重放检查。
func (g *Game) cycleChases(cycle []plyMark, side Side) bool {
	probe := g.Clone()
	for range cycle {
		if _, ok := probe.Undo(); !ok {
			return false
		}
	}
	for _, mark := range cycle {
		probe.ApplyUnchecked(mark.move)
		if mark.side != side {
			continue
		}
		if !probe.chases(side) {
			return false
		}
	}
	return true
}

// chases 报告 side 是否正威胁白吃对方一个大子：存在一步合法着法能吃掉对方的
// 无根车/马/炮。有根子的威胁只是兑子，不算捉；兵卒士象也不计，否则普通的
// 不变作和容易被误判成长捉。
func (g *Game) chases(side Side) bool {
	opp := side.Opponent()
	for _, m := range g.legalMoves(side) {
		target := g.board.At(m.To)
		if target.Type == NoPiece || target.Side != opp || !isMajorPiece(target.Type) {
			continue
		}
		if g.isDefended(m.To, opp) {
			continue
		}
		return true
	}
	return false
}

// isDefended 报告 sq 上的子是否被本方其它棋子保护（对方吃过来以后能立刻吃回）。
// 只看伪合法着法：规则问的是「能不能吃回」，与该着法是否会送将无关。
func (g *Game) isDefended(sq Square, side Side) bool {
	for _, m := range g.board.PseudoLegalMovesFor(side) {
		if m.To == sq && m.From != sq {
			return true
		}
	}
	return false
}

// isMajorPiece 报告该棋子是否属于「值得反复追捉」的大子。
func isMajorPiece(t PieceType) bool {
	switch t {
	case Rook, Cannon, Horse:
		return true
	}
	return false
}
