package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	appDBMu sync.RWMutex
	appDB   *sql.DB
)

// InitAppDB initializes the consolidated SQLite database at dataDir/spermacore.db.
// If an encryption key is specified (via argument or SPERMACORE_DB_KEY / SPERMACORE_SQLCIPHER_KEY),
// it configures SQLCipher / AES-256 database encryption pragmas.
func InitAppDB(dataDir string, encryptionKey ...string) error {
	appDBMu.Lock()
	defer appDBMu.Unlock()

	if appDB != nil {
		return nil
	}

	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir data_dir %s: %w", dataDir, err)
	}

	key := ""
	if len(encryptionKey) > 0 && encryptionKey[0] != "" {
		key = encryptionKey[0]
	} else if env := os.Getenv("SPERMACORE_DB_KEY"); env != "" {
		key = env
	} else if env := os.Getenv("SPERMACORE_SQLCIPHER_KEY"); env != "" {
		key = env
	}

	dbPath := filepath.Join(dataDir, "spermacore.db")
	dsn := dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}

	if key != "" {
		// Configure SQLCipher encryption and security parameters
		escaped := strings.ReplaceAll(key, "'", "''")
		_, _ = db.Exec(fmt.Sprintf("PRAGMA key = '%s';", escaped))
		_, _ = db.Exec("PRAGMA cipher_page_size = 4096;")
		_, _ = db.Exec("PRAGMA kdf_iter = 64000;")
		_, _ = db.Exec("PRAGMA cipher_hmac_algorithm = HMAC_SHA512;")
		_, _ = db.Exec("PRAGMA cipher_kdf_algorithm = PBKDF2_HMAC_SHA512;")
	}

	// create tables
	schema := `
	CREATE TABLE IF NOT EXISTS chat_history (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_chat_history_chat ON chat_history(chat_id, id);

	CREATE TABLE IF NOT EXISTS user_profiles (
		sender_id INTEGER PRIMARY KEY,
		username TEXT,
		display_name TEXT,
		real_name TEXT,
		profile_json TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	CREATE INDEX IF NOT EXISTS idx_user_profiles_username ON user_profiles(username);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return fmt.Errorf("init sqlite schema: %w", err)
	}

	appDB = db
	if key != "" {
		GetLogger("core.db").Info("sqlite db initialized: %s (SQLCipher AES-256 enabled, wal mode)", dbPath)
	} else {
		GetLogger("core.db").Info("sqlite db initialized: %s (wal mode)", dbPath)
	}
	return nil
}

// CloseAppDB closes the database handle.
func CloseAppDB() {
	appDBMu.Lock()
	defer appDBMu.Unlock()
	if appDB != nil {
		_ = appDB.Close()
		appDB = nil
	}
}

// dbSaveChatMessage inserts a chat message into the database.
func dbSaveChatMessage(chatID int64, role, content string) {
	appDBMu.RLock()
	db := appDB
	appDBMu.RUnlock()
	if db == nil {
		return
	}

	go func() {
		_, _ = db.Exec(
			"INSERT INTO chat_history (chat_id, role, content) VALUES (?, ?, ?)",
			chatID, role, content,
		)
	}()
}

// dbLoadChatHistory loads the last N messages for a chat.
func dbLoadChatHistory(chatID int64, limit int) []ChatMessage {
	appDBMu.RLock()
	db := appDB
	appDBMu.RUnlock()
	if db == nil {
		return nil
	}

	if limit <= 0 {
		limit = 20
	}

	query := `
		SELECT role, content FROM (
			SELECT id, role, content FROM chat_history
			WHERE chat_id = ?
			ORDER BY id DESC
			LIMIT ?
		) ORDER BY id ASC
	`
	rows, err := db.Query(query, chatID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var msgs []ChatMessage
	for rows.Next() {
		var role, content string
		if err := rows.Scan(&role, &content); err == nil {
			msgs = append(msgs, ChatMessage{Role: role, Content: content})
		}
	}
	return msgs
}

// dbClearChatHistory removes history for a chat.
func dbClearChatHistory(chatID int64) {
	appDBMu.RLock()
	db := appDB
	appDBMu.RUnlock()
	if db == nil {
		return
	}
	_, _ = db.Exec("DELETE FROM chat_history WHERE chat_id = ?", chatID)
}

// dbSaveUserProfile stores user profile in SQLite.
func dbSaveUserProfile(p *UserProfile) error {
	if p == nil || p.SenderID == 0 {
		return nil
	}
	appDBMu.RLock()
	db := appDB
	appDBMu.RUnlock()
	if db == nil {
		return nil
	}

	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}

	uname := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(p.Username), "@"))
	query := `
		INSERT INTO user_profiles (sender_id, username, display_name, real_name, profile_json, updated_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(sender_id) DO UPDATE SET
			username=excluded.username,
			display_name=excluded.display_name,
			real_name=excluded.real_name,
			profile_json=excluded.profile_json,
			updated_at=CURRENT_TIMESTAMP
	`
	_, err = db.Exec(query, p.SenderID, uname, p.DisplayName, p.RealName, string(raw))
	return err
}

// dbLoadUserProfile retrieves user profile by sender_id.
func dbLoadUserProfile(senderID int64) (*UserProfile, error) {
	if senderID == 0 {
		return nil, nil
	}
	appDBMu.RLock()
	db := appDB
	appDBMu.RUnlock()
	if db == nil {
		return nil, nil
	}

	var raw string
	err := db.QueryRow("SELECT profile_json FROM user_profiles WHERE sender_id = ?", senderID).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	var p UserProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// dbLoadUserProfileByUsername retrieves user profile by username.
func dbLoadUserProfileByUsername(username string) (*UserProfile, error) {
	uname := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(username), "@"))
	if uname == "" {
		return nil, nil
	}
	appDBMu.RLock()
	db := appDB
	appDBMu.RUnlock()
	if db == nil {
		return nil, nil
	}

	var raw string
	err := db.QueryRow("SELECT profile_json FROM user_profiles WHERE username = ? LIMIT 1", uname).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	var p UserProfile
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// dbListKnownUsernames returns all known usernames from SQLite.
func dbListKnownUsernames() []string {
	appDBMu.RLock()
	db := appDB
	appDBMu.RUnlock()
	if db == nil {
		return nil
	}

	rows, err := db.Query("SELECT username FROM user_profiles WHERE username != ''")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var list []string
	seen := make(map[string]bool)
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err == nil && u != "" && !seen[u] {
			seen[u] = true
			list = append(list, u)
		}
	}
	return list
}
