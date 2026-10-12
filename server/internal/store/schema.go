package store

// schema holds the DDL, applied in order on every start. Every statement is
// written to be idempotent so migrations can run unconditionally.
var schema = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		username      VARCHAR(32)     NOT NULL,
		nickname      VARCHAR(64)     NOT NULL DEFAULT '',
		password_hash VARCHAR(255)    NOT NULL,
		rating        INT             NOT NULL DEFAULT 1200,
		wins          INT             NOT NULL DEFAULT 0,
		losses        INT             NOT NULL DEFAULT 0,
		draws         INT             NOT NULL DEFAULT 0,
		created_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at    DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		last_seen_at  DATETIME        NULL,
		PRIMARY KEY (id),
		UNIQUE KEY uk_users_username (username),
		KEY idx_users_rating (rating)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	`CREATE TABLE IF NOT EXISTS games (
		id             BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		red_user_id    BIGINT UNSIGNED NULL,
		black_user_id  BIGINT UNSIGNED NULL,
		red_name       VARCHAR(64)     NOT NULL DEFAULT '红方',
		black_name     VARCHAR(64)     NOT NULL DEFAULT '黑方',
		status         VARCHAR(16)     NOT NULL DEFAULT 'waiting',
		outcome        VARCHAR(16)     NOT NULL DEFAULT 'ongoing',
		reason         VARCHAR(32)     NOT NULL DEFAULT '',
		winner_side    VARCHAR(8)      NOT NULL DEFAULT '',
		fen            VARCHAR(128)    NOT NULL DEFAULT '',
		move_count     INT             NOT NULL DEFAULT 0,
		red_ms_left    BIGINT          NOT NULL DEFAULT 0,
		black_ms_left  BIGINT          NOT NULL DEFAULT 0,
		rated          TINYINT(1)      NOT NULL DEFAULT 1,
		ai_level       INT             NOT NULL DEFAULT 0,
		created_at     DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
		started_at     DATETIME        NULL,
		finished_at    DATETIME        NULL,
		PRIMARY KEY (id),
		KEY idx_games_status (status),
		KEY idx_games_red (red_user_id),
		KEY idx_games_black (black_user_id),
		KEY idx_games_created (created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	`CREATE TABLE IF NOT EXISTS moves (
		id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		game_id    BIGINT UNSIGNED NOT NULL,
		ply        INT             NOT NULL,
		side       VARCHAR(8)      NOT NULL,
		uci        VARCHAR(8)      NOT NULL,
		notation   VARCHAR(32)     NOT NULL DEFAULT '',
		fen        VARCHAR(128)    NOT NULL,
		check_give TINYINT(1)      NOT NULL DEFAULT 0,
		ms_spent   INT             NOT NULL DEFAULT 0,
		created_at DATETIME        NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_moves_game_ply (game_id, ply),
		CONSTRAINT fk_moves_game FOREIGN KEY (game_id) REFERENCES games (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
}
