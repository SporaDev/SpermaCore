package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// follow-up event for proactive messaging
type FollowUpEvent struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	TargetDate  string    `json:"target_date"`
	CheckAfter  time.Time `json:"check_after"`
	Asked       bool      `json:"asked"`
	AskedAt     time.Time `json:"asked_at,omitempty"`
}

// user profile with durable facts
type UserProfile struct {
	SenderID    int64             `json:"sender_id"`
	Username    string            `json:"username,omitempty"`
	DisplayName string            `json:"display_name,omitempty"`
	RealName    string            `json:"real_name,omitempty"`
	Facts       map[string]string `json:"facts,omitempty"`
	Notes       []string          `json:"notes,omitempty"`
	FollowUps   []FollowUpEvent   `json:"follow_ups,omitempty"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

const maxProfileCacheSize = 1000

type profileLRUEntry struct {
	id   int64
	p    *UserProfile
	prev *profileLRUEntry
	next *profileLRUEntry
}

type userProfileLRU struct {
	mu    sync.Mutex
	cap   int
	items map[int64]*profileLRUEntry
	head  *profileLRUEntry
	tail  *profileLRUEntry
}

func newProfileLRU(capacity int) *userProfileLRU {
	return &userProfileLRU{
		cap:   capacity,
		items: make(map[int64]*profileLRUEntry),
	}
}

func (l *userProfileLRU) Get(id int64) *UserProfile {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.items[id]
	if !ok {
		return nil
	}
	l.moveToHead(entry)
	return entry.p
}

func (l *userProfileLRU) Put(p *UserProfile) {
	if p == nil || p.SenderID == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	if entry, ok := l.items[p.SenderID]; ok {
		entry.p = p
		l.moveToHead(entry)
		return
	}

	entry := &profileLRUEntry{id: p.SenderID, p: p}
	l.items[p.SenderID] = entry
	l.addToHead(entry)

	if len(l.items) > l.cap {
		l.removeTail()
	}
}

func (l *userProfileLRU) Delete(id int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.items[id]
	if !ok {
		return
	}
	l.removeNode(entry)
	delete(l.items, id)
}

func (l *userProfileLRU) FindByUsername(username string) *UserProfile {
	l.mu.Lock()
	defer l.mu.Unlock()
	uClean := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	for _, entry := range l.items {
		if entry.p != nil && strings.EqualFold(strings.TrimPrefix(entry.p.Username, "@"), uClean) {
			l.moveToHead(entry)
			return entry.p
		}
	}
	return nil
}

func (l *userProfileLRU) Snapshot() []*UserProfile {
	l.mu.Lock()
	defer l.mu.Unlock()
	res := make([]*UserProfile, 0, len(l.items))
	for _, entry := range l.items {
		if entry.p != nil {
			res = append(res, entry.p)
		}
	}
	return res
}

func (l *userProfileLRU) addToHead(node *profileLRUEntry) {
	node.next = l.head
	node.prev = nil
	if l.head != nil {
		l.head.prev = node
	}
	l.head = node
	if l.tail == nil {
		l.tail = node
	}
}

func (l *userProfileLRU) removeNode(node *profileLRUEntry) {
	if node.prev != nil {
		node.prev.next = node.next
	} else {
		l.head = node.next
	}
	if node.next != nil {
		node.next.prev = node.prev
	} else {
		l.tail = node.prev
	}
}

func (l *userProfileLRU) moveToHead(node *profileLRUEntry) {
	if l.head == node {
		return
	}
	l.removeNode(node)
	l.addToHead(node)
}

func (l *userProfileLRU) removeTail() {
	if l.tail == nil {
		return
	}
	delete(l.items, l.tail.id)
	if l.tail.prev != nil {
		l.tail.prev.next = nil
		l.tail = l.tail.prev
	} else {
		l.head = nil
		l.tail = nil
	}
}

var profileCache = newProfileLRU(maxProfileCacheSize)

func userProfilePath(dataDir string, senderID int64) string {
	return filepath.Join(dataDir, "users", fmt.Sprintf("%d.json", senderID))
}

func loadUserProfile(dataDir string, senderID int64) *UserProfile {
	if senderID == 0 {
		return nil
	}
	if p := profileCache.Get(senderID); p != nil {
		return p
	}

	// 1. Try SQLite
	if p, err := dbLoadUserProfile(senderID); err == nil && p != nil {
		if p.Facts == nil {
			p.Facts = make(map[string]string)
		}
		profileCache.Put(p)
		return p
	}

	// 2. Fallback to JSON file on disk (migration)
	path := userProfilePath(dataDir, senderID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var loaded UserProfile
	if err := json.Unmarshal(data, &loaded); err != nil {
		return nil
	}
	if loaded.Facts == nil {
		loaded.Facts = make(map[string]string)
	}

	_ = dbSaveUserProfile(&loaded)
	profileCache.Put(&loaded)
	return &loaded
}

func saveUserProfile(dataDir string, p *UserProfile) error {
	if p == nil || p.SenderID == 0 {
		return nil
	}
	p.UpdatedAt = time.Now()
	if p.Facts == nil {
		p.Facts = make(map[string]string)
	}

	profileCache.Put(p)
	_ = dbSaveUserProfile(p)

	dir := filepath.Join(dataDir, "users")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := userProfilePath(dataDir, p.SenderID)
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func deleteUserProfile(dataDir string, senderID int64) {
	if senderID == 0 {
		return
	}
	profileCache.Delete(senderID)

	appDBMu.RLock()
	db := appDB
	appDBMu.RUnlock()
	if db != nil {
		_, _ = db.Exec("DELETE FROM user_profiles WHERE sender_id = ?", senderID)
	}

	_ = os.Remove(userProfilePath(dataDir, senderID))
}

// profile context block for prompt
func getUserProfileContext(dataDir string, senderID int64) string {
	if senderID == 0 {
		return ""
	}
	p := loadUserProfile(dataDir, senderID)
	if p == nil {
		return ""
	}

	var sb strings.Builder
	hasInfo := false
	sb.WriteString("=== USER PROFILE (PERSONAL CONTEXT) ===\n")
	if p.RealName != "" {
		sb.WriteString(fmt.Sprintf("- user name: %s\n", p.RealName))
		hasInfo = true
	}
	for k, v := range p.Facts {
		if strings.ToLower(k) != "name" && strings.ToLower(k) != "имя" && v != "" {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", k, v))
			hasInfo = true
		}
	}
	for _, n := range p.Notes {
		if n != "" {
			sb.WriteString(fmt.Sprintf("- %s\n", n))
			hasInfo = true
		}
	}

	// due events and follow-ups
	now := time.Now()
	var dueEvents []string
	var futureEvents []string

	for _, fu := range p.FollowUps {
		if fu.Description == "" {
			continue
		}
		if !fu.Asked && !fu.CheckAfter.IsZero() && now.After(fu.CheckAfter) {
			dueEvents = append(dueEvents, fmt.Sprintf("- [important - event occurred]: %s (date: %s). ask how it went! for example: 'how did %s go?'", fu.Description, fu.TargetDate, fu.Description))
		} else if !fu.Asked {
			futureEvents = append(futureEvents, fmt.Sprintf("- [upcoming plans]: %s (scheduled for %s)", fu.Description, fu.TargetDate))
		}
	}

	if len(dueEvents) > 0 {
		sb.WriteString("\n=== IMPORTANT DUE EVENTS (TIME TO FOLLOW UP) ===\n")
		for _, e := range dueEvents {
			sb.WriteString(e + "\n")
		}
		hasInfo = true
	}
	if len(futureEvents) > 0 {
		sb.WriteString("\n=== UPCOMING USER PLANS ===\n")
		for _, e := range futureEvents {
			sb.WriteString(e + "\n")
		}
		hasInfo = true
	}

	if !hasInfo {
		return ""
	}
	sb.WriteString("This is factual memory about this specific user across chats. Use it naturally.\n")
	return sb.String()
}

func isForgetMe(text string) bool {
	t := strings.ToLower(text)
	triggers := []string{
		"forget me", "forget about me", "clear my profile", "delete my memory", "delete my profile", "erase my data",
		"забудь обо мне", "забудь меня", "забудь мое имя", "забудь моё имя",
		"очисти мой профиль", "очисти моё профиль", "очисти мое профиль",
	}
	for _, tr := range triggers {
		if strings.Contains(t, tr) {
			return true
		}
	}
	return false
}

var (
	// name
	reNameIntro = regexp.MustCompile(`(?i)(?:меня зовут|мо[её] имя|зовут меня|my name is|call me)\s+([А-ЯЁа-яёA-Za-z0-9_\-]+)`)

	// age
	reAgeIntro = regexp.MustCompile(`(?i)(?:мне)\s+(\d{1,2})\s*(?:лет|года|год)?|(?:i am|i'm)\s+(\d{1,2})\s*(?:years old|yo)?`)

	// city
	reCityIntro = regexp.MustCompile(`(?i)(?:я из|живу в|приехал из)\s+([А-ЯЁ][а-яёA-Za-z\-]+)|(?:i live in|i am from)\s+([A-Za-zА-ЯЁа-яё\-]+)`)

	// job
	reJobIntro = regexp.MustCompile(`(?i)(?:я работаю|работаю|по профессии я|i work as|i am an?|my job is)\s+([А-ЯЁа-яёA-Za-z\-]+)`)

	// events
	reEventIntro = regexp.MustCompile(`(?i)(?:^|\s)(?:i am |i'm |я\s+)?(?:завтра|послезавтра|сегодня вечером|tomorrow|tonight)\s+(?:у меня\s+|иду\s+|еду\s+|пойду\s+|поеду\s+|лечу\s+|going to\s+|having\s+)?(?:на\s+|к\s+|в\s+|во\s+|to\s+)?([А-ЯЁа-яёA-Za-z0-9\s\-]{3,45})`)
)

// fast regex heuristics
func extractFastUserFacts(dataDir string, ev transportEvent) bool {
	if ev.SenderID == 0 || strings.TrimSpace(ev.Text) == "" {
		return false
	}
	text := strings.TrimSpace(ev.Text)

	// forget me request
	if isForgetMe(text) {
		deleteUserProfile(dataDir, ev.SenderID)
		return true
	}

	p := loadUserProfile(dataDir, ev.SenderID)
	if p == nil {
		p = &UserProfile{
			SenderID:    ev.SenderID,
			Username:    ev.SenderUsername,
			DisplayName: ev.SenderName,
			Facts:       make(map[string]string),
		}
	}
	if ev.SenderUsername != "" && p.Username == "" {
		p.Username = ev.SenderUsername
	}
	if ev.SenderName != "" && p.DisplayName == "" {
		p.DisplayName = ev.SenderName
	}

	updated := false

	// name
	if m := reNameIntro.FindStringSubmatch(text); len(m) > 1 {
		name := cleanExtractedWord(m[1])
		if name != "" && len([]rune(name)) >= 2 {
			p.RealName = name
			p.Facts["name"] = name
			updated = true
		}
	}

	// age
	if m := reAgeIntro.FindStringSubmatch(text); len(m) > 1 {
		age := m[1]
		if age == "" && len(m) > 2 {
			age = m[2]
		}
		if age != "" {
			p.Facts["age"] = age
			updated = true
		}
	}

	// city
	if m := reCityIntro.FindStringSubmatch(text); len(m) > 1 {
		city := cleanExtractedWord(m[1])
		if city == "" && len(m) > 2 {
			city = cleanExtractedWord(m[2])
		}
		if city != "" && len([]rune(city)) >= 3 {
			p.Facts["city"] = city
			updated = true
		}
	}

	// job
	if m := reJobIntro.FindStringSubmatch(text); len(m) > 1 {
		job := cleanExtractedWord(m[1])
		if job != "" && len([]rune(job)) >= 3 {
			p.Facts["job"] = job
			updated = true
		}
	}

	// events
	if m := reEventIntro.FindStringSubmatch(text); len(m) > 1 {
		rawEvent := cleanExtractedWord(m[1])
		rawEvent = strings.TrimSpace(rawEvent)
		lower := strings.ToLower(rawEvent)
		if len([]rune(rawEvent)) >= 3 && !strings.Contains(lower, "привет") && !strings.Contains(lower, "дела") && !strings.Contains(lower, "hello") {
			targetDate := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
			if strings.Contains(strings.ToLower(text), "послезавтра") || strings.Contains(strings.ToLower(text), "after tomorrow") {
				targetDate = time.Now().AddDate(0, 0, 2).Format("2006-01-02")
			} else if strings.Contains(strings.ToLower(text), "сегодня вечером") || strings.Contains(strings.ToLower(text), "tonight") {
				targetDate = time.Now().Format("2006-01-02")
			}

			dup := false
			for _, fu := range p.FollowUps {
				if strings.EqualFold(fu.Description, rawEvent) && fu.TargetDate == targetDate {
					dup = true
					break
				}
			}
			if !dup {
				checkAfter := time.Now().Add(24 * time.Hour)
				if t, err := time.Parse("2006-01-02", targetDate); err == nil {
					checkAfter = t.Add(18 * time.Hour)
				}
				p.FollowUps = append(p.FollowUps, FollowUpEvent{
					ID:          fmt.Sprintf("fu_%d_%d", p.SenderID, time.Now().UnixNano()),
					Description: rawEvent,
					TargetDate:  targetDate,
					CheckAfter:  checkAfter,
					Asked:       false,
				})
				updated = true
			}
		}
	}

	if updated {
		_ = saveUserProfile(dataDir, p)
	}
	return updated
}

func cleanExtractedWord(w string) string {
	w = strings.TrimSpace(w)
	return strings.Trim(w, ",.!?\"';:()[]{}")
}

// checks personal clues or upcoming plans
func containsPersonalClues(text string) bool {
	t := strings.ToLower(text)
	clues := []string{
		"я ", "меня ", "мое ", "моё ", "мой ", "моя ", "мои ", "мне ", "обо мне", "у меня",
		"i ", "my ", "me ", "i'm ", "i am ",
		"завтра", "послезавтра", "сегодня вечером", "на днях", "скоро", "через пару",
		"на выходных", "на следующей", "на след неделе", "в понедельник", "во вторник",
		"в среду", "в четверг", "в пятницу", "в субботу", "в воскресенье",
		"буду ", "хочу ", "собираюсь ", "планиру", "договорился", "записался",
		"иду ", "еду ", "лечу ", "пойду ", "поеду ", "полетаю", "сдам ", "сдаю ",
		"собеседован", "интервью", "оффер", "стажировк", "митинг", "созвон", "презентац",
		"проект", "дедлайн", "релиз", "экзамен", "зачет", "сесси", "диплом", "курсач",
		"универ", "школ", "колледж", "институт", "пары",
		"врач", "стоматолог", "дантист", "больниц", "клиник", "поликлиник", "терапевт",
		"окулист", "хирург", "узи", "анализ", "операци", "прививк", "тату",
		"поездк", "рейс", "билет", "самолет", "поезд", "отпуск", "командировк", "отель",
		"дача", "шашлык", "рыбалк", "поход", "свидан", "тусовк", "вечерин", "вписк",
		"др ", "день рождения", "праздник", "свадьб", "концерт", "фест", "кино", "театр",
		"матч", "игр", "стрим", "качалк", "тренировк", "зал", "бассейн",
		"купил", "купить", "покуп", "заказал", "доставк", "посылк", "курьер",
		"ремонт", "переезд", "сто ", "автосервис", "машин", "тачк", "права ", "гаи", "гибдд",
	}
	for _, c := range clues {
		if strings.Contains(t, c) {
			return true
		}
	}
	return false
}

// background reflection for facts and plans
func reflectUserFactsBackground(ev transportEvent, provider LLMProvider, cfg *Config, log *Logger) {
	if ev.SenderID == 0 || strings.TrimSpace(ev.Text) == "" || provider == nil {
		return
	}
	if !containsPersonalClues(ev.Text) {
		return
	}

	text := ev.Text
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()

		nowStr := time.Now().Format("2006-01-02 15:04 (Monday)")
		defaultUserPrompt := "you are a background long-term reflection memory module.\n" +
			"current real time: %s.\n\n" +
			"analyze the user's message and extract:\n" +
			"1. persistent personal facts (name, age, city, job, pets, hobbies, preferences).\n" +
			"2. upcoming plans and life events (interviews, exams, trips, meetings, appointments, purchases, etc.):\n" +
			"   - description: short description (e.g. 'job interview')\n" +
			"   - target_date: event date in YYYY-MM-DD format (calculated relative to %s)\n" +
			"   - check_after: when to ask about the result (evening of event or next morning in YYYY-MM-DDTHH:MM:SSZ format)\n\n" +
			"message: %q\n\n" +
			"reply STRICTLY with valid raw JSON without markdown (```json):\n" +
			`{"name": "...", "facts": {"key": "value"}, "notes": ["short fact"], "follow_ups": [{"description": "...", "target_date": "YYYY-MM-DD", "check_after": "YYYY-MM-DDTHH:MM:SSZ"}]}` + "\n" +
			"if user gave no personal facts or plans, return an empty object: {}"

		reflectionTmpl := loadPromptFile(cfg.Core.DataDir, "reflection.txt", defaultUserPrompt)
		prompt := fmt.Sprintf(reflectionTmpl, nowStr, nowStr[:10], text)

		systemPrompt := loadPromptFile(cfg.Core.DataDir, "reflection_system.txt", "you are a background reflection memory module. return strictly raw JSON.")
		messages := []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: prompt},
		}

		model := cfg.ModelFor("fast")
		if model == "" {
			model = cfg.ModelFor("chat")
		}

		resp, err := provider.Chat(ctx, model, messages, nil)
		if err != nil {
			if log != nil {
				log.Debug("reflection: chat completion error: %v", err)
			}
			return
		}

		mergeExtractedFacts(cfg.Core.DataDir, ev.SenderID, ev.SenderUsername, ev.SenderName, resp.Content, log)
	}()
}

// parse reflection json and merge into user profile
func mergeExtractedFacts(dataDir string, senderID int64, username, displayName, rawJSON string, log *Logger) {
	rawJSON = strings.TrimSpace(rawJSON)
	rawJSON = strings.TrimPrefix(rawJSON, "```json")
	rawJSON = strings.TrimPrefix(rawJSON, "```")
	rawJSON = strings.TrimSuffix(rawJSON, "```")
	rawJSON = strings.TrimSpace(rawJSON)

	if rawJSON == "" || rawJSON == "{}" {
		return
	}

	var res struct {
		Name      string            `json:"name"`
		Facts     map[string]string `json:"facts"`
		Notes     []string          `json:"notes"`
		FollowUps []struct {
			Description string `json:"description"`
			TargetDate  string `json:"target_date"`
			CheckAfter  string `json:"check_after"`
		} `json:"follow_ups"`
	}

	if err := json.Unmarshal([]byte(rawJSON), &res); err != nil {
		if log != nil {
			log.Debug("reflection: cannot parse json %q: %v", rawJSON, err)
		}
		return
	}

	p := loadUserProfile(dataDir, senderID)
	if p == nil {
		p = &UserProfile{
			SenderID:    senderID,
			Username:    username,
			DisplayName: displayName,
			Facts:       make(map[string]string),
		}
	}
	if p.Facts == nil {
		p.Facts = make(map[string]string)
	}

	updated := false

	if res.Name != "" && len([]rune(res.Name)) >= 2 {
		p.RealName = res.Name
		p.Facts["name"] = res.Name
		updated = true
	}

	for k, v := range res.Facts {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			p.Facts[k] = v
			updated = true
		}
	}

	for _, n := range res.Notes {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		dup := false
		for _, existing := range p.Notes {
			if strings.EqualFold(existing, n) {
				dup = true
				break
			}
		}
		if !dup {
			p.Notes = append(p.Notes, n)
			if len(p.Notes) > 20 {
				p.Notes = p.Notes[len(p.Notes)-20:]
			}
			updated = true
		}
	}

	// follow-up events
	for _, fu := range res.FollowUps {
		desc := strings.TrimSpace(fu.Description)
		if desc == "" {
			continue
		}
		targetDate := strings.TrimSpace(fu.TargetDate)
		if targetDate == "" {
			targetDate = time.Now().AddDate(0, 0, 1).Format("2006-01-02")
		}

		dup := false
		for _, existing := range p.FollowUps {
			if strings.EqualFold(existing.Description, desc) && existing.TargetDate == targetDate {
				dup = true
				break
			}
		}
		if dup {
			continue
		}

		var checkTime time.Time
		if fu.CheckAfter != "" {
			formats := []string{
				time.RFC3339,
				"2006-01-02T15:04:05Z07:00",
				"2006-01-02 15:04:05",
				"2006-01-02 15:04",
				"2006-01-02",
			}
			for _, f := range formats {
				if t, err := time.Parse(f, fu.CheckAfter); err == nil {
					checkTime = t
					break
				}
			}
		}
		if checkTime.IsZero() {
			if t, err := time.Parse("2006-01-02", targetDate); err == nil {
				checkTime = t.Add(18 * time.Hour)
			} else {
				checkTime = time.Now().Add(24 * time.Hour)
			}
		}

		p.FollowUps = append(p.FollowUps, FollowUpEvent{
			ID:          fmt.Sprintf("fu_%d_%d", p.SenderID, time.Now().UnixNano()),
			Description: desc,
			TargetDate:  targetDate,
			CheckAfter:  checkTime,
			Asked:       false,
		})
		updated = true
	}

	if updated {
		if err := saveUserProfile(dataDir, p); err == nil && log != nil {
			log.Info("reflection: saved user profile id=%d name=%q facts=%d followups=%d",
				senderID, p.RealName, len(p.Facts), len(p.FollowUps))
		}
	}
}

// check if dummy/test user
func isDummyUsername(u string) bool {
	clean := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(u, "@")))
	switch clean {
	case "admin", "friend", "human", "user", "bot", "test", "null", "undefined", "":
		return true
	default:
		return false
	}
}

func getKnownUsernames(dataDir string) []string {
	// 1. Try SQLite first
	dbList := dbListKnownUsernames()
	if len(dbList) > 0 {
		var list []string
		for _, u := range dbList {
			if !isDummyUsername(u) {
				list = append(list, u)
			}
		}
		if len(list) > 0 {
			return list
		}
	}

	// 2. Check in-memory LRU snapshot
	var list []string
	seen := make(map[string]bool)
	for _, p := range profileCache.Snapshot() {
		if p != nil && p.Username != "" {
			u := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(p.Username, "@")))
			if u != "" && !isDummyUsername(u) && !seen[u] {
				list = append(list, u)
				seen[u] = true
			}
		}
	}

	// 3. Fallback to disk scan (migrates profiles to SQLite)
	dir := filepath.Join(dataDir, "users")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return list
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var p UserProfile
		if err := json.Unmarshal(raw, &p); err == nil && p.Username != "" {
			u := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(p.Username, "@")))
			if u != "" && !isDummyUsername(u) && !seen[u] {
				list = append(list, u)
				seen[u] = true
				_ = dbSaveUserProfile(&p)
			}
		}
	}
	return list
}

func findUserProfileByUsername(dataDir string, username string) *UserProfile {
	uClean := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	if uClean == "" {
		return nil
	}

	// 1. Check LRU
	if p := profileCache.FindByUsername(uClean); p != nil {
		return p
	}

	// 2. Check SQLite
	if p, err := dbLoadUserProfileByUsername(uClean); err == nil && p != nil {
		if p.Facts == nil {
			p.Facts = make(map[string]string)
		}
		profileCache.Put(p)
		return p
	}

	// 3. Fallback to disk scan
	dir := filepath.Join(dataDir, "users")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var p UserProfile
		if err := json.Unmarshal(raw, &p); err == nil {
			if strings.EqualFold(strings.TrimPrefix(p.Username, "@"), uClean) {
				_ = dbSaveUserProfile(&p)
				profileCache.Put(&p)
				return &p
			}
		}
	}
	return nil
}

func hasDueFollowUp(dataDir string, username string) bool {
	p := findUserProfileByUsername(dataDir, username)
	if p == nil {
		return false
	}
	now := time.Now()
	for _, fu := range p.FollowUps {
		if !fu.Asked && !fu.CheckAfter.IsZero() && now.After(fu.CheckAfter) {
			return true
		}
	}
	return false
}

func markUserFollowUpsAsked(dataDir string, username string) {
	p := findUserProfileByUsername(dataDir, username)
	if p == nil {
		return
	}
	now := time.Now()
	updated := false
	for i := range p.FollowUps {
		if !p.FollowUps[i].Asked && !p.FollowUps[i].CheckAfter.IsZero() && now.After(p.FollowUps[i].CheckAfter) {
			p.FollowUps[i].Asked = true
			p.FollowUps[i].AskedAt = now
			updated = true
		}
	}
	if updated {
		_ = saveUserProfile(dataDir, p)
	}
}

// handle |IMPORTANT| or |IMPORTANT=event|
func handleImportantAction(dataDir string, ev transportEvent, val string) {
	if ev.SenderID == 0 {
		return
	}
	p := loadUserProfile(dataDir, ev.SenderID)
	if p == nil {
		p = &UserProfile{
			SenderID:    ev.SenderID,
			Username:    ev.SenderUsername,
			DisplayName: ev.SenderName,
			Facts:       make(map[string]string),
		}
	}
	if ev.SenderUsername != "" && p.Username == "" {
		p.Username = ev.SenderUsername
	}
	if ev.SenderName != "" && p.DisplayName == "" {
		p.DisplayName = ev.SenderName
	}

	desc := strings.TrimSpace(val)
	if desc == "" {
		if m := reEventIntro.FindStringSubmatch(ev.Text); len(m) > 1 {
			desc = cleanExtractedWord(m[1])
		}
		if desc == "" {
			desc = cleanExtractedWord(ev.Text)
			r := []rune(desc)
			if len(r) > 40 {
				desc = string(r[:40]) + "..."
			}
		}
	}

	targetDate := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	lowerText := strings.ToLower(ev.Text + " " + desc)
	if strings.Contains(lowerText, "послезавтра") || strings.Contains(lowerText, "after tomorrow") {
		targetDate = time.Now().AddDate(0, 0, 2).Format("2006-01-02")
	} else if strings.Contains(lowerText, "сегодня") || strings.Contains(lowerText, "tonight") {
		targetDate = time.Now().Format("2006-01-02")
	}

	checkAfter := time.Now().Add(24 * time.Hour)
	if t, err := time.Parse("2006-01-02", targetDate); err == nil {
		checkAfter = t.Add(18 * time.Hour)
	}

	dup := false
	for _, fu := range p.FollowUps {
		if strings.EqualFold(fu.Description, desc) && fu.TargetDate == targetDate {
			dup = true
			break
		}
	}

	if !dup {
		p.FollowUps = append(p.FollowUps, FollowUpEvent{
			ID:          fmt.Sprintf("fu_%d_%d", p.SenderID, time.Now().UnixNano()),
			Description: desc,
			TargetDate:  targetDate,
			CheckAfter:  checkAfter,
			Asked:       false,
		})
	}

	note := fmt.Sprintf("[important]: %s", desc)
	hasNote := false
	for _, n := range p.Notes {
		if strings.EqualFold(n, note) {
			hasNote = true
			break
		}
	}
	if !hasNote {
		p.Notes = append(p.Notes, note)
	}

	_ = saveUserProfile(dataDir, p)
}
