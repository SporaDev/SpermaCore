package embedsc

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestInitWithExistingMemoriesDoesNotDeadlock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "embedsc")
	dbPath := filepath.Join(root, "memory.db")
	d, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.Exec(`CREATE TABLE memories (id INTEGER PRIMARY KEY AUTOINCREMENT, chat_id INTEGER NOT NULL, role TEXT NOT NULL, text TEXT NOT NULL, embedding BLOB NOT NULL, created_at TEXT NOT NULL);
INSERT INTO memories(chat_id,role,text,embedding,created_at) VALUES(7,'user','hi',X'0000803F','2026-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	d.Close()

	done := make(chan error, 1)
	go func() { done <- Init("http://example.invalid", "m", "k", dir) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("init: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Init deadlocked with existing memories")
	}
	if err := Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestStoreHNSWIndex(t *testing.T) {
	s := &Store{
		chatID: 42,
		index:  NewHNSWIndex(DefaultHNSWConfig()),
		loaded: true,
	}

	// add records with vectors
	vec1 := []float32{1, 0, 0, 0}
	vec2 := []float32{0, 1, 0, 0}
	vec3 := []float32{0, 0, 1, 0}

	s.records = append(s.records,
		Record{ID: 1, Role: "user", Text: "apple", Vec: vec1},
		Record{ID: 2, Role: "assistant", Text: "banana", Vec: vec2},
		Record{ID: 3, Role: "user", Text: "cherry", Vec: vec3},
	)
	s.index.Insert(1, vec1)
	s.index.Insert(2, vec2)
	s.index.Insert(3, vec3)

	if s.index.Len() != 3 {
		t.Fatalf("expected index len 3, got %d", s.index.Len())
	}

	stats := s.index.Stats()
	if stats.Count != 3 {
		t.Fatalf("expected stats count 3, got %d", stats.Count)
	}

	// Query with vector closest to vec2
	query := []float32{0.1, 0.99, 0, 0}
	norm(query)
	results := s.index.Search(query, 2, 16)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ID != 2 {
		t.Fatalf("expected closest result to be ID 2 (banana), got ID %d", results[0].ID)
	}
}
