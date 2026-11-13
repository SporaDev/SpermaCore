package embedsc

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	_ "modernc.org/sqlite"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	embedWorkers      = 2
	rememberQueueSize = 1000
	embedCacheSize    = 1000
	embedTimeout      = 60 * time.Second
)

type Record struct {
	ID   int       `json:"id"`
	TS   time.Time `json:"ts"`
	Role string    `json:"role"`
	Text string    `json:"text"`
	Vec  []float32 `json:"-"`
}
type Hit struct {
	Record     Record  `json:"record"`
	Similarity float32 `json:"similarity"`
}
type rememberJob struct {
	chatID     int64
	role, text string
}
type Store struct {
	mu                     sync.Mutex
	chatID                 int64
	model, baseURL, apiKey string
	client                 *http.Client
	records                []Record
	index                  *HNSWIndex
	// last used timestamp for eviction
	lastUsed time.Time
	// whether records are loaded from db
	loaded bool
}

var (
	hnswConfigMu sync.RWMutex
	globalHNSW   = DefaultHNSWConfig()
)

// SetHNSWConfig sets the global HNSW parameters for newly created stores.
func SetHNSWConfig(cfg HNSWConfig) {
	hnswConfigMu.Lock()
	defer hnswConfigMu.Unlock()
	globalHNSW = cfg
}

func getHNSWConfig() HNSWConfig {
	hnswConfigMu.RLock()
	defer hnswConfigMu.RUnlock()
	return globalHNSW
}

var emitLog = func(format string, args ...any) {}

// set logger hook
func SetLogger(fn func(format string, args ...any)) {
	if fn != nil {
		emitLog = fn
	}
}

var (
	initMu                 sync.Mutex
	baseURL, model, apiKey string
	httpCli                *http.Client
	db                     *sql.DB
	storesMu               sync.Mutex
	stores                 = map[int64]*Store{}
	queue                  chan rememberJob
	wg                     sync.WaitGroup
	cache                  *embedCache
)

func Init(embeddingURL, embedModel, apiKeyValue, dir string) error {
	initMu.Lock()
	defer initMu.Unlock()
	if queue != nil {
		return errors.New("embedsc: already initialized")
	}
	if embeddingURL == "" || embedModel == "" {
		return errors.New("embedsc: missing URL or model")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	baseURL, model, apiKey = strings.TrimRight(embeddingURL, "/"), embedModel, strings.TrimSpace(apiKeyValue)
	emitLog("[embedsc] initialized endpoint=%s model=%s api_key_present=%t api_key_len=%d", baseURL, model, apiKey != "", len(apiKey))
	httpCli = &http.Client{Timeout: embedTimeout}
	cache = newEmbedCache(embedCacheSize)
	var err error
	db, err = sql.Open("sqlite", filepath.Join(filepath.Dir(dir), "memory.db"))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS memories (id INTEGER PRIMARY KEY AUTOINCREMENT, chat_id INTEGER NOT NULL, role TEXT NOT NULL, text TEXT NOT NULL, embedding BLOB NOT NULL, created_at TEXT NOT NULL); CREATE INDEX IF NOT EXISTS idx_memories_chat ON memories(chat_id);`); err != nil {
		db.Close()
		db = nil
		return err
	}
	if err = loadAll(); err != nil {
		db.Close()
		db = nil
		return err
	}
	queue = make(chan rememberJob, rememberQueueSize)
	for i := 0; i < embedWorkers; i++ {
		wg.Add(1)
		go worker(queue)
	}
	return nil
}
func Close() error {
	initMu.Lock()
	q := queue
	queue = nil
	initMu.Unlock()
	if q == nil {
		return nil
	}
	// drain queue before closing db
	close(q)
	wg.Wait()
	initMu.Lock()
	d := db
	db = nil
	initMu.Unlock()
	storesMu.Lock()
	stores = map[int64]*Store{}
	storesMu.Unlock()
	if d != nil {
		return d.Close()
	}
	return nil
}

// reports whether embedding service is initialized
func Enabled() bool {
	initMu.Lock()
	defer initMu.Unlock()
	return queue != nil && db != nil
}

func Remember(chatID int64, role, text string) error {
	if text == "" {
		return nil
	}
	initMu.Lock()
	q := queue
	initMu.Unlock()
	if q == nil {
		return nil
	}
	job := rememberJob{chatID, role, text}
	select {
	case q <- job:
		emitLog("[embedsc] message queued for embedding chat=%d role=%s text_len=%d", chatID, role, len(text))
		return nil
	default:
		emitLog("[embedsc] embedding queue full chat=%d role=%s text_len=%d", chatID, role, len(text))
		return errors.New("embedsc: queue full")
	}
}
func worker(q chan rememberJob) {
	defer wg.Done()
	for j := range q {
		process(j)
	}
}
func getDB() *sql.DB {
	initMu.Lock()
	defer initMu.Unlock()
	return db
}

func process(j rememberJob) {
	emitLog("[embedsc] embedding started chat=%d role=%s text_len=%d", j.chatID, j.role, len(j.text))
	s, e := getStore(j.chatID)
	if e != nil {
		emitLog("[embedsc] embedding failed chat=%d: %v", j.chatID, e)
		return
	}
	d := getDB()
	if d == nil {
		emitLog("[embedsc] embedding skipped chat=%d: db is closed", j.chatID)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	v, e := s.embed(j.text)
	if e != nil {
		emitLog("[embedsc] embedding failed chat=%d role=%s: %v", j.chatID, j.role, e)
		return
	}
	emitLog("[embedsc] embedding received chat=%d role=%s dimensions=%d", j.chatID, j.role, len(v))
	now := time.Now().UTC()
	result, e := d.Exec(`INSERT INTO memories(chat_id,role,text,embedding,created_at) VALUES(?,?,?,?,?)`, j.chatID, j.role, j.text, pack(v), now.Format(time.RFC3339Nano))
	if e != nil {
		emitLog("[embedsc] database write failed chat=%d role=%s: %v", j.chatID, j.role, e)
		return
	}
	id, err := result.LastInsertId()
	if err != nil {
		emitLog("[embedsc] database id retrieval failed chat=%d role=%s: %v", j.chatID, j.role, err)
		return
	}
	emitLog("[embedsc] database written chat=%d role=%s id=%d dimensions=%d", j.chatID, j.role, id, len(v))
	s.records = append(s.records, Record{ID: int(id), TS: now, Role: j.role, Text: j.text, Vec: v})
	if s.index == nil {
		s.index = NewHNSWIndex(getHNSWConfig())
	}
	s.index.Insert(int(id), v)
}
func loadAll() error {
	rows, e := db.Query(`SELECT id,chat_id,role,text,embedding,created_at FROM memories ORDER BY id`)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var chat int64
		var role, text, ts string
		var b []byte
		if e = rows.Scan(&id, &chat, &role, &text, &b, &ts); e != nil {
			return e
		}
		t, _ := time.Parse(time.RFC3339Nano, ts)
		s := storeLocked(chat)
		vec := unpack(b)
		if len(vec) == 0 {
			emitLog("[embedsc] skipping memory id=%d: empty embedding", id)
			continue
		}
		s.records = append(s.records, Record{id, t, role, text, vec})
		if s.index == nil {
			s.index = NewHNSWIndex(getHNSWConfig())
		}
		s.index.Insert(id, vec)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// mark all loaded stores as active
	storesMu.Lock()
	for _, s := range stores {
		s.mu.Lock()
		s.loaded = true
		s.lastUsed = time.Now()
		s.mu.Unlock()
	}
	storesMu.Unlock()
	return nil
}
func getStore(id int64) (*Store, error) {
	// check init status before acquiring storesMu
	d := getDB()
	if d == nil {
		return nil, errors.New("embedsc: not initialized")
	}
	s := storeLocked(id)
	if err := s.ensureLoaded(d); err != nil {
		return nil, err
	}
	return s, nil
}

// returns store under storesMu without locking initMu
func storeLocked(id int64) *Store {
	storesMu.Lock()
	defer storesMu.Unlock()
	if s := stores[id]; s != nil {
		return s
	}
	s := &Store{
		chatID:   id,
		model:    model,
		baseURL:  baseURL,
		apiKey:   apiKey,
		client:   httpCli,
		lastUsed: time.Now(),
		index:    NewHNSWIndex(getHNSWConfig()),
	}
	stores[id] = s
	return s
}

// store count returns number of cached stores
func StoreCount() int {
	storesMu.Lock()
	defer storesMu.Unlock()
	return len(stores)
}

// removes idle stores beyond cutoff and max limit
func PruneStores(cutoff time.Time, max int) int {
	storesMu.Lock()
	defer storesMu.Unlock()

	removed := 0
	for id, s := range stores {
		s.mu.Lock()
		idle := s.lastUsed
		s.mu.Unlock()
		if idle.Before(cutoff) {
			delete(stores, id)
			removed++
		}
	}
	if max > 0 && len(stores) > max {
		type entry struct {
			id int64
			at time.Time
		}
		rest := make([]entry, 0, len(stores))
		for id, s := range stores {
			s.mu.Lock()
			at := s.lastUsed
			s.mu.Unlock()
			rest = append(rest, entry{id, at})
		}
		sort.Slice(rest, func(i, j int) bool { return rest[i].at.Before(rest[j].at) })
		for i := 0; i < len(rest)-max; i++ {
			delete(stores, rest[i].id)
			removed++
		}
	}
	return removed
}

// lazily loads chat records from db
func (s *Store) ensureLoaded(d *sql.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUsed = time.Now()
	if s.loaded || d == nil {
		return nil
	}
	s.loaded = true
	rows, err := d.Query(`SELECT id,role,text,embedding,created_at FROM memories WHERE chat_id=? ORDER BY id`, s.chatID)
	if err != nil {
		s.loaded = false
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var role, text, ts string
		var b []byte
		if err := rows.Scan(&id, &role, &text, &b, &ts); err != nil {
			s.loaded = false
			return err
		}
		vec := unpack(b)
		if len(vec) == 0 {
			continue
		}
		t, _ := time.Parse(time.RFC3339Nano, ts)
		s.records = append(s.records, Record{ID: id, TS: t, Role: role, Text: text, Vec: vec})
		if s.index == nil {
			s.index = NewHNSWIndex(getHNSWConfig())
		}
		s.index.Insert(id, vec)
	}
	return rows.Err()
}
func Recall(chatID int64, q string, k int) ([]Hit, error) {
	if q == "" || !Enabled() {
		return nil, nil
	}
	s, e := getStore(chatID)
	if e != nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.records) == 0 {
		return nil, nil
	}
	v, e := s.embed(q)
	if e != nil {
		return nil, e
	}

	// use HNSW ANN index if available
	if s.index != nil && s.index.Len() > 0 {
		results := s.index.Search(v, k, s.index.cfg.EfSearch)
		if len(results) > 0 {
			recMap := make(map[int]Record, len(s.records))
			for _, r := range s.records {
				recMap[r.ID] = r
			}
			hits := make([]Hit, 0, len(results))
			for _, res := range results {
				if r, ok := recMap[res.ID]; ok {
					hits = append(hits, Hit{Record: r, Similarity: res.Similarity})
				}
			}
			return hits, nil
		}
	}

	// fallback to linear scan
	h := make([]Hit, 0, len(s.records))
	for _, r := range s.records {
		h = append(h, Hit{r, dot(v, r.Vec)})
	}
	sort.Slice(h, func(i, j int) bool { return h[i].Similarity > h[j].Similarity })
	if k > 0 && len(h) > k {
		h = h[:k]
	}
	return h, nil
}
func Stats(chatID int64) (int, string, error) {
	if !Enabled() {
		return 0, "", nil
	}
	s, e := getStore(chatID)
	if e != nil {
		return 0, "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records), s.model, nil
}

// IndexStats returns runtime HNSW index metrics for a chat
func IndexStats(chatID int64) (HNSWStats, error) {
	if !Enabled() {
		return HNSWStats{}, errors.New("embedsc: not enabled")
	}
	s, e := getStore(chatID)
	if e != nil {
		return HNSWStats{}, e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.index == nil {
		return HNSWStats{}, nil
	}
	return s.index.Stats(), nil
}
func (s *Store) embed(text string) ([]float32, error) {
	key := cacheKey(s.model, text)
	if v, ok := cache.get(key); ok {
		return v, nil
	}
	endpoint := s.baseURL + "/api/embeddings"
	var payload any = map[string]string{"model": s.model, "prompt": text}
	if strings.Contains(s.baseURL, "/v1") || strings.Contains(s.baseURL, "openrouter") {
		endpoint = strings.TrimRight(s.baseURL, "/") + "/embeddings"
		payload = map[string]any{"model": s.model, "input": text}
	}
	body, _ := json.Marshal(payload)
	req, e := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
		req.Header.Set("HTTP-Referer", "https://sperma.site")
		req.Header.Set("X-Title", "SpermaCore")
	} else if strings.Contains(s.baseURL, "openrouter") {
		return nil, errors.New("openrouter API key is empty")
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var detail struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&detail)
		if detail.Error.Message != "" {
			return nil, fmt.Errorf("embeddings http %d: %s", resp.StatusCode, detail.Error.Message)
		}
		return nil, fmt.Errorf("embeddings http %d", resp.StatusCode)
	}
	var x struct {
		Embedding []float64 `json:"embedding"`
		Data      []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if e = json.NewDecoder(resp.Body).Decode(&x); e != nil {
		return nil, e
	}
	values := x.Embedding
	if len(values) == 0 && len(x.Data) > 0 {
		values = x.Data[0].Embedding
	}
	if len(values) == 0 {
		return nil, errors.New("empty embedding")
	}
	v := make([]float32, len(values))
	for i, n := range values {
		value := float32(n)
		if math.IsNaN(n) || math.IsInf(n, 0) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("invalid embedding value at index %d", i)
		}
		v[i] = value
	}
	if norm(v) == 0 {
		return nil, errors.New("zero embedding")
	}
	cache.put(key, v)
	return v, nil
}
func pack(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}
func unpack(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
func norm(v []float32) float32 {
	var n float32
	for _, x := range v {
		n += x * x
	}
	if n == 0 {
		return 0
	}
	n = float32(math.Sqrt(float64(n)))
	for i := range v {
		v[i] /= n
	}
	return n
}
func dot(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	n := len(a)
	var x float32
	for i := 0; i < n; i++ {
		x += a[i] * b[i]
	}
	return x
}

type embedCache struct {
	mu    sync.Mutex
	cap   int
	ll    *list.List
	items map[string]*list.Element
}
type cacheEntry struct {
	key string
	val []float32
}

func newEmbedCache(n int) *embedCache {
	return &embedCache{cap: n, ll: list.New(), items: map[string]*list.Element{}}
}
func (c *embedCache) get(k string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(e)
	return e.Value.(cacheEntry).val, true
}
func (c *embedCache) put(k string, v []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.items[k]; e != nil {
		e.Value = cacheEntry{k, v}
		c.ll.MoveToFront(e)
		return
	}
	e := c.ll.PushFront(cacheEntry{k, v})
	c.items[k] = e
	if c.ll.Len() > c.cap {
		e = c.ll.Back()
		delete(c.items, e.Value.(cacheEntry).key)
		c.ll.Remove(e)
	}
}
func cacheKey(m, t string) string { h := sha256.Sum256([]byte(m + "\x00" + t)); return string(h[:]) }
