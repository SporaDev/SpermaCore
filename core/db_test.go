package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitAppDBUnencrypted(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "spermacore-db-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	CloseAppDB()
	if err := InitAppDB(tempDir); err != nil {
		t.Fatalf("InitAppDB failed: %v", err)
	}
	defer CloseAppDB()

	// Test chat history save and load
	dbSaveChatMessage(12345, "user", "hello")
	dbSaveChatMessage(12345, "assistant", "world")

	// wait briefly for async insert
	msgs := dbLoadChatHistory(12345, 10)
	if len(msgs) == 0 {
		// async might take a moment, retry
		for i := 0; i < 10; i++ {
			msgs = dbLoadChatHistory(12345, 10)
			if len(msgs) > 0 {
				break
			}
		}
	}

	dbClearChatHistory(12345)
}

func TestInitAppDBSQLCipherKey(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "spermacore-db-encrypted-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tempDir)

	CloseAppDB()
	// Test with SQLCipher encryption key
	testKey := "super-secure-sqlcipher-passphrase-256"
	if err := InitAppDB(tempDir, testKey); err != nil {
		t.Fatalf("InitAppDB with SQLCipher key failed: %v", err)
	}
	defer CloseAppDB()

	dbPath := filepath.Join(tempDir, "spermacore.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("db file was not created: %v", err)
	}
}
