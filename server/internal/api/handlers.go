package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"xiangqi/internal/ai"
	"xiangqi/internal/auth"
	"xiangqi/internal/game"
	"xiangqi/internal/gamehall"
	"xiangqi/internal/store"
	"xiangqi/internal/ws"
)

// ---- health and rules ----

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	dbStatus := "ok"
	if err := s.store.Ping(r.Context()); err != nil {
		status = "degraded"
		dbStatus = err.Error()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   status,
		"database": dbStatus,
		"rooms":    len(s.session.RoomIDs()),
		"serverAt": time.Now().UnixMilli(),
	})
}

// handleRules publishes the board geometry and the piece vocabulary so the
// frontend never hard-codes a second copy of the rules.
func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	type pieceDoc struct {
		Key   string `json:"key"`
		Side  string `json:"side"`
		Type  string `json:"type"`
		FEN   string `json:"fen"`
		Glyph string `json:"glyph"`
	}
	var pieces []pieceDoc
	seen := map[string]bool{}
	for _, side := range []game.Side{game.Red, game.Black} {
		for _, t := range []game.PieceType{game.King, game.Advisor, game.Elephant, game.Horse, game.Rook, game.Cannon, game.Pawn} {
			p := game.Piece{Side: side, Type: t}
			key := gamehall.SideName(side) + "_" + t.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			pieces = append(pieces, pieceDoc{
				Key: key, Side: gamehall.SideName(side), Type: t.String(),
				FEN: string(p.FEN()), Glyph: p.Text(),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":      game.Rows,
		"cols":      game.Cols,
		"startFen":  game.StartFEN,
		"pieces":    pieces,
		"turnClock": s.cfg.TurnTimeout.Milliseconds(),
		"gameClock": s.cfg.GameTimeout.Milliseconds(),
		"notes": []string{
			"红方先行",
			"将帅不能在同一直线上直接相对",
			"困毙（无子可动）判负",
			"六十回合无吃子判和",
		},
	})
}

// ---- accounts ----

type registerRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Nickname string `json:"nickname"`
}

type sessionResponse struct {
	Token     string     `json:"token"`
	ExpiresAt time.Time  `json:"expiresAt"`
	User      store.User `json:"user"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := s.store.CreateUser(r.Context(), req.Username, req.Nickname, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrUsernameTaken):
			writeError(w, http.StatusConflict, "username_taken", "该用户名已被注册")
		case errors.Is(err, store.ErrDuplicate):
			writeError(w, http.StatusConflict, "username_taken", "该用户名已被注册")
		default:
			writeError(w, http.StatusBadRequest, "invalid_registration", err.Error())
		}
		return
	}
	token, exp, err := s.tokens.Issue(user.ID, user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_failed", "无法签发登录凭证")
		return
	}
	s.logger.Info("user registered", "id", user.ID, "username", user.Username)
	writeJSON(w, http.StatusCreated, sessionResponse{Token: token, ExpiresAt: exp, User: user})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	user, err := s.store.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		if errors.Is(err, store.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "bad_credentials", "用户名或密码不正确")
			return
		}
		writeError(w, http.StatusInternalServerError, "login_failed", err.Error())
		return
	}
	token, exp, err := s.tokens.Issue(user.ID, user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_failed", "无法签发登录凭证")
		return
	}
	writeJSON(w, http.StatusOK, sessionResponse{Token: token, ExpiresAt: exp, User: user})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user, ok := currentUser(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing_token", "请先登录")
		return
	}
	stats, err := s.store.StatsForUser(r.Context(), user.ID)
	if err != nil {
		s.logger.Warn("cannot load stats", "user", user.ID, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "stats": stats})
}

func (s *Server) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	limit := queryInt(r, "limit", 50)
	users, err := s.store.Leaderboard(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// handleUserProfile returns one account with its record.
func (s *Server) handleUserProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "用户编号不正确")
		return
	}
	user, err := s.store.UserByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "用户不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	stats, err := s.store.StatsForUser(r.Context(), id)
	if err != nil {
		s.logger.Warn("cannot load stats", "user", id, "error", err)
	}
	games, err := s.store.ListGames(r.Context(), store.GameFilter{UserID: id, Limit: 10})
	if err != nil {
		s.logger.Warn("cannot load games", "user", id, "error", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "stats": stats, "games": games})
}

// ---- lobby ----

// handleLobby lists the waiting rooms and who is online.
func (s *Server) handleLobby(w http.ResponseWriter, r *http.Request) {
	open := s.session.OpenRooms()
	type openRoom struct {
		ID        int64     `json:"id"`
		HostName  string    `json:"hostName"`
		HostSide  string    `json:"hostSide"`
		Rated     bool      `json:"rated"`
		CreatedAt time.Time `json:"createdAt"`
	}
	rooms := make([]openRoom, 0, len(open))
	for _, o := range open {
		rooms = append(rooms, openRoom{
			ID: o.ID, HostName: o.HostName, HostSide: o.HostSide,
			Rated: o.Rated, CreatedAt: o.CreatedAt,
		})
	}
	// 对局中的房间：任何人都可以进去观战，所以大厅要把它们列出来。
	type playingRoom struct {
		ID         int64     `json:"id"`
		RedName    string    `json:"redName"`
		BlackName  string    `json:"blackName"`
		MoveCount  int       `json:"moveCount"`
		Rated      bool      `json:"rated"`
		Spectators int       `json:"spectators"`
		CreatedAt  time.Time `json:"createdAt"`
	}
	playing := s.session.PlayingRooms()
	ongoing := make([]playingRoom, 0, len(playing))
	for _, p := range playing {
		spectators := 0
		if s.hub != nil {
			spectators = s.hub.SpectatorCount(p.ID)
		}
		ongoing = append(ongoing, playingRoom{
			ID: p.ID, RedName: p.RedName, BlackName: p.BlackName,
			MoveCount: p.MoveCount, Rated: p.Rated,
			Spectators: spectators, CreatedAt: p.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"openRooms":    rooms,
		"playingRooms": ongoing,
		"playing":      len(s.session.RoomIDs()),
	})
}

// ---- saved games ----

func (s *Server) handleListGames(w http.ResponseWriter, r *http.Request) {
	filter := store.GameFilter{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Limit:  queryInt(r, "limit", 20),
		Offset: queryInt(r, "offset", 0),
	}
	if r.URL.Query().Get("mine") == "1" {
		if user, ok := currentUser(s.optionalAuth(r)); ok {
			filter.UserID = user.ID
		}
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("userId")); raw != "" {
		var id int64
		for _, c := range raw {
			if c < '0' || c > '9' {
				writeError(w, http.StatusBadRequest, "bad_id", "用户编号不正确")
				return
			}
			id = id*10 + int64(c-'0')
		}
		filter.UserID = id
	}

	games, err := s.store.ListGames(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": games})
}

func (s *Server) handleGetGame(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "对局编号不正确")
		return
	}
	g, err := s.store.GameByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "对局不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"game": g})
}

func (s *Server) handleGetGameMoves(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id", "对局编号不正确")
		return
	}
	g, err := s.store.GameByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "对局不存在")
			return
		}
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	moves, err := s.store.Moves(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"game": g, "moves": moves})
}

// ---- computer opponent ----

// handleAIMove returns a move for a position. It lets the frontend offer
// "play against the computer" without a WebSocket, and doubles as a way to
// inspect the engine.
func (s *Server) handleAIMove(w http.ResponseWriter, r *http.Request) {
	fen := strings.TrimSpace(r.URL.Query().Get("fen"))
	if fen == "" {
		fen = game.StartFEN
	}
	g, err := game.NewGameFromFEN(fen)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_fen", "局面字符串不正确")
		return
	}
	level := ai.ParseLevel(queryInt(r, "level", int(ai.LevelNormal)))
	budget := time.Duration(queryInt(r, "budgetMs", int(s.cfg.TurnTimeout.Milliseconds()))) * time.Millisecond
	if budget <= 0 || budget > 5*time.Second {
		budget = s.session.Config().AIBudget
	}

	move, ok := ai.Search(g, level, budget)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"fen": g.FEN(), "move": nil, "gameOver": true,
			"reason": string(g.Reason()),
		})
		return
	}
	notation, _ := g.Notation(move)
	writeJSON(w, http.StatusOK, map[string]any{
		"fen":      g.FEN(),
		"from":     move.FromName(),
		"to":       move.ToName(),
		"notation": notation,
		"level":    level.String(),
	})
}

// ---- websocket ----

// handleWebSocket upgrades the connection. A valid bearer token is optional: a
// signed-out visitor may connect, but the session will refuse to seat them.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	identity := ws.Identity{Guest: true}

	// Browsers cannot set headers on a WebSocket handshake, so the token is
	// accepted from the query string as well as the Authorization header.
	token := ""
	if headerToken, ok := auth.ParseBearer(r.Header.Get("Authorization")); ok {
		token = headerToken
	} else if q := strings.TrimSpace(r.URL.Query().Get("token")); q != "" {
		token = q
	}

	if token != "" {
		claims, err := s.tokens.Parse(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "bad_token", "登录状态已失效，请重新登录")
			return
		}
		user, err := s.store.UserByID(r.Context(), claims.Subject)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unknown_user", "账号不存在")
			return
		}
		identity = ws.Identity{UserID: user.ID, Name: user.DisplayName(), Guest: false}
	}

	s.hub.ServeHTTP(w, r, identity)
}
