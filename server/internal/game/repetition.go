package game

// 本文件实现局面重复的裁决：长将、长捉与不变作和。
//
// 中国象棋竞赛规则把「单方长将」「单方长捉」列为不变作负的行为。落地时它由两个
// 条件的**同时成立**决定：
//
//  1. 局面确实在来回重复 —— 当前局面在本局至少出现过第二次，说明双方在绕圈，
//     而不是一路攻杀（一路将军直到将死是允许的）；
//  2. 某一方连续将军或连续捉子到 perpetualLimit 次 —— 门槛刻意抬高到 5 次，
//     两三个回合的重复更像正常的中局纠缠，不该直接判负。
//
// 只有同时满足才判该方负；双方都不变（或都长将、都长捉）判和。
//
// 记录刻意用自己的一份 marks，而不是复用 records：AI 与 perft 走棋用的是
// ApplyUnchecked，它只动棋盘、不追加 records。若判重依赖 records 的下标，就会
// 在搜索里越界。

// boardSquares 是棋盘格数；局面键多一位用来存走子方。
const boardSquares = Rows * Cols

// perpetualLimit 是「长将 / 长捉」判负所需的连续步数。
//
// 取 5 而非 3 是刻意的：象棋里两三个回合的重复（兑子、抢先手）非常常见，用三次重复
// 直接判负会把正常的中局纠缠误杀。连续 5 次仍然每一步都在将军（或每一步都在追捉
// 同一个无根大子），才说明是在靠重复消耗对手。
const perpetualLimit = 5

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
	// Streak 是触发裁决时的连续将军（或连续捉子）次数，仅用于测试与排障。
	Streak int
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

// Repeated reports whether the current position has already occurred in this game.
// It is the cheap half of the repetition check, and the AI uses it to back off
// from a loop before the verdict can even fire.
func (g *Game) Repeated() bool {
	if g.seen == nil || len(g.marks) == 0 {
		return false
	}
	return g.seen[g.marks[len(g.marks)-1].key] >= 2
}

// Repetition 检查当前局面是否已因长将 / 长捉 / 三次重复而应当终局。它不修改 Game，
// 因此 AI 在搜索里也能安全地调用它判断某条线路会不会把自己走死。
func (g *Game) Repetition() RepetitionVerdict {
	if g.seen == nil || len(g.marks) == 0 {
		return RepetitionVerdict{}
	}
	// 前提：局面在来回重复。一路攻杀（没有重复）时无论将军多少次都合法。
	repeats := g.seen[g.marks[len(g.marks)-1].key]
	if repeats < 2 {
		return RepetitionVerdict{}
	}

	redChecks := g.consecutiveChecks(Red)
	blackChecks := g.consecutiveChecks(Black)
	switch {
	case redChecks >= perpetualLimit && blackChecks < perpetualLimit:
		return RepetitionVerdict{true, OutcomeBlackWin, ReasonPerpetualCheck, Red, redChecks}
	case blackChecks >= perpetualLimit && redChecks < perpetualLimit:
		return RepetitionVerdict{true, OutcomeRedWin, ReasonPerpetualCheck, Black, blackChecks}
	}

	// 长捉要回放中间局面，比数将军贵，所以只在没人在长将时才算。
	redChases, blackChases := 0, 0
	if redChecks < perpetualLimit && blackChecks < perpetualLimit {
		redChases = g.consecutiveChases(Red)
		blackChases = g.consecutiveChases(Black)
		switch {
		case redChases >= perpetualLimit && blackChases < perpetualLimit:
			return RepetitionVerdict{true, OutcomeBlackWin, ReasonPerpetualChase, Red, redChases}
		case blackChases >= perpetualLimit && redChases < perpetualLimit:
			return RepetitionVerdict{true, OutcomeRedWin, ReasonPerpetualChase, Black, blackChases}
		}
	}

	if repeats < 3 {
		return RepetitionVerdict{}
	}
	// 有一方还在长将 / 长捉（只是次数没数满）时不要判和：否则它会在数到 5 之前
	// 被"和棋"提前截断，规则就永远用不上了。
	redBusy := redChecks >= 2 || redChases >= 2
	blackBusy := blackChecks >= 2 || blackChases >= 2
	if redBusy != blackBusy {
		return RepetitionVerdict{}
	}
	// 双方都不变，或者双方都在长将 / 长捉：不变作和。
	return RepetitionVerdict{true, OutcomeDraw, ReasonRepetition, Red, repeats}
}

// consecutiveChecks 从最后一步往回数，某一方连续将军了多少次。中间隔着对方的应将
// 步，只数这一方自己的步子。
func (g *Game) consecutiveChecks(side Side) int {
	n := 0
	for i := len(g.marks) - 1; i >= 0; i-- {
		if g.marks[i].side != side {
			continue
		}
		if !g.marks[i].check {
			break
		}
		n++
	}
	return n
}

// consecutiveChases 报告某一方最近连续多少步走完之后都在「捉」对方。中间局面已经
// 不在棋盘上，所以先悔棋回到足够早的位置，再正向重放逐个数。
func (g *Game) consecutiveChases(side Side) int {
	rewind := perpetualLimit * 2
	if rewind > len(g.marks) {
		rewind = len(g.marks)
	}
	probe := g.Clone()
	for i := 0; i < rewind; i++ {
		if _, ok := probe.Undo(); !ok {
			break
		}
	}
	count := 0
	for i := len(g.marks) - rewind; i < len(g.marks); i++ {
		mark := g.marks[i]
		probe.ApplyUnchecked(mark.move)
		if mark.side != side {
			continue
		}
		if probe.chases(side) {
			count++
		} else {
			count = 0
		}
	}
	return count
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
