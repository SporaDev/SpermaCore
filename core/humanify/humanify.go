package humanify

import (
	"math/rand"
	"strings"
	"sync"
	"time"
	"unicode"
)

// humanifier manages timing and natural typing randomness
type Humanifier struct {
	cfg *Config

	mu  sync.Mutex
	rng *rand.Rand
}

var (
	globalMu sync.RWMutex
	global   *Humanifier
)

// init initializes global humanifier
func Init(cfg *Config) {
	if cfg == nil {
		cfg = Load(nil)
	}
	globalMu.Lock()
	global = &Humanifier{
		cfg: cfg,
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	globalMu.Unlock()
}

// global returns current instance
func Global() *Humanifier {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// enabled reports whether humanify is active
func (h *Humanifier) Enabled() bool {
	if h == nil || h.cfg == nil {
		return false
	}
	return h.cfg.Enabled
}

// auto_fix_typo reports whether auto typo fix is enabled
func (h *Humanifier) AutoFixTypo() bool {
	if h == nil || h.cfg == nil {
		return false
	}
	return h.cfg.AutoFixTypo
}

// thread-safe random helpers
func (h *Humanifier) randInt64n(n int64) int64 {
	if n <= 0 {
		return 0
	}
	h.mu.Lock()
	v := h.rng.Int63n(n)
	h.mu.Unlock()
	return v
}

func (h *Humanifier) randFloat() float64 {
	h.mu.Lock()
	v := h.rng.Float64()
	h.mu.Unlock()
	return v
}

// delay before first typing indicator
func (h *Humanifier) DelayBefore(text string) time.Duration {
	if !h.Enabled() {
		return 0
	}
	min := h.cfg.DelayMin
	max := h.cfg.DelayMax
	if max <= min {
		return min
	}

	// short text uses lower half of range
	var span time.Duration
	if len([]rune(text)) < 20 {
		span = (max - min) / 2
	} else {
		span = max - min
	}
	if span <= 0 {
		return min
	}
	delay := min + time.Duration(h.randInt64n(int64(span)))
	// simulate occasional distraction
	if h.cfg.DistractionChance > 0 && h.cfg.DistractionMax > 0 && h.randFloat() < h.cfg.DistractionChance {
		delay += time.Duration(h.randInt64n(int64(h.cfg.DistractionMax)))
	}
	return delay
}

// delay between split messages
func (h *Humanifier) DelayBetween() time.Duration {
	if !h.Enabled() {
		return 0
	}
	base := h.cfg.SplitPause
	if base <= 0 {
		return 0
	}
	// random jitter between 50% and 150% of split pause
	jitter := time.Duration(h.randInt64n(int64(base)))
	return base/2 + jitter
}

func (h *Humanifier) ShouldReply() bool {
	if !h.Enabled() || h.cfg == nil {
		return true
	}
	p := h.cfg.ReplyProbability
	if p <= 0 {
		return false
	}
	if p >= 1 {
		return true
	}
	return h.randFloat() < p
}

// typing indicator duration based on character count
func (h *Humanifier) TypingDuration(text string) time.Duration {
	runes := int64(len([]rune(text)))
	if runes <= 0 {
		return 500 * time.Millisecond
	}
	base := time.Duration(runes) * 25 * time.Millisecond

	// apply jitter
	if h.cfg != nil && h.cfg.TypingJitter > 0 {
		span := float64(base) * h.cfg.TypingJitter
		delta := time.Duration((h.randFloat()*2 - 1) * span)
		base += delta
	}

	if base < 500*time.Millisecond {
		base = 500 * time.Millisecond
	}
	if base > 8*time.Second {
		base = 8 * time.Second
	}
	return base
}

// split breaks long message into sentence or word chunks
func (h *Humanifier) Split(text string) []string {
	if !h.Enabled() || h.cfg == nil || !h.cfg.SplitLong {
		return []string{text}
	}
	if len([]rune(text)) <= h.cfg.SplitThreshold {
		return []string{text}
	}
	if h.cfg.SplitParts <= 1 {
		return []string{text}
	}

	// fallback to word split if no sentence boundaries
	sentences := splitSentences(text)
	if len(sentences) <= 1 {
		return splitWords(text, h.cfg.SplitParts, h.cfg.SplitThreshold)
	}
	if len(sentences) < h.cfg.SplitParts && len([]rune(text)) > h.cfg.SplitThreshold {
		return splitWords(text, h.cfg.SplitParts, h.cfg.SplitThreshold)
	}

	// target part count
	targetParts := h.cfg.SplitParts
	if targetParts > len(sentences) {
		targetParts = len(sentences)
	}

	// balance sentence distribution across parts
	totalLen := 0
	for _, s := range sentences {
		totalLen += len([]rune(s))
	}
	partTarget := totalLen / targetParts

	parts := make([]string, 0, targetParts)
	var cur strings.Builder
	curLen := 0
	for i, s := range sentences {
		cur.WriteString(s)
		curLen += len([]rune(s))

		remainingParts := targetParts - len(parts)
		remainingSentences := len(sentences) - i - 1

		// finalize part if target size reached
		if remainingParts > 1 &&
			curLen >= partTarget &&
			remainingSentences >= remainingParts-1 {
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
			curLen = 0
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}

	if len(parts) <= 1 {
		return []string{text}
	}
	return parts
}

// split words for long sentences without punctuation
func splitWords(text string, targetParts, threshold int) []string {
	if len([]rune(text)) <= threshold {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) < 2 {
		return []string{text}
	}
	if targetParts < 2 {
		targetParts = 2
	}
	target := (len([]rune(text)) + targetParts - 1) / targetParts
	var parts []string
	var current strings.Builder
	length := 0
	for _, word := range words {
		wordLen := len([]rune(word))
		if current.Len() > 0 && length >= target && len(parts) < targetParts-1 {
			parts = append(parts, current.String())
			current.Reset()
			length = 0
		}
		if current.Len() > 0 {
			current.WriteByte(' ')
			length++
		}
		current.WriteString(word)
		length += wordLen
	}
	if current.Len() > 0 {
		parts = append(parts, current.String())
	}
	if len(parts) > 1 {
		return parts
	}
	return []string{text}
}

// humanize introduces rare natural typos
func (h *Humanifier) Humanize(text string) string {
	if !h.Enabled() || h.cfg == nil || h.cfg.TypoChance <= 0 || h.cfg.TypoMax <= 0 {
		return text
	}
	if len([]rune(text)) < 12 {
		return text
	}
	// skip code, links, mentions, and formatting markers
	if strings.ContainsAny(text, "`|") || strings.Contains(text, "://") || strings.Contains(text, "@") {
		return text
	}
	if h.randFloat() >= h.cfg.TypoChance {
		return text
	}

	words := strings.Split(text, " ")
	candidates := make([]int, 0, len(words))
	for i, word := range words {
		if typoWordOK(word) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return text
	}
	n := h.cfg.TypoMax
	if n > len(candidates) {
		n = len(candidates)
	}
	for k := 0; k < n; k++ {
		j := int(h.randInt64n(int64(len(candidates))))
		wordIdx := candidates[j]
		words[wordIdx] = h.typoWord(words[wordIdx])
		candidates = append(candidates[:j], candidates[j+1:]...)
	}
	return strings.Join(words, " ")
}

// validate whether a word can safely have typos
func typoWordOK(word string) bool {
	runes := []rune(word)
	if len(runes) < 4 {
		return false
	}
	if strings.ToUpper(word) == word && strings.ToLower(word) != word {
		return false
	}
	if strings.ContainsAny(word, "@#/\\`|_=*{}[]()<>:;\"'") {
		return false
	}
	for _, r := range runes {
		if unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func (h *Humanifier) typoWord(word string) string {
	rs := []rune(word)
	positions := make([]int, 0, len(rs))
	// avoid first character to look natural
	for i, r := range rs {
		if i > 0 && unicode.IsLetter(r) {
			positions = append(positions, i)
		}
	}
	if len(positions) == 0 {
		return word
	}
	pos := positions[h.randInt64n(int64(len(positions)))]

	switch h.randInt64n(4) {
	case 0: // swap adjacent letters
		if pos+1 < len(rs) && unicode.IsLetter(rs[pos+1]) {
			rs[pos], rs[pos+1] = rs[pos+1], rs[pos]
		} else {
			rs = insertRune(rs, pos, rs[pos])
		}
	case 1: // double letter
		rs = insertRune(rs, pos, rs[pos])
	case 2: // drop letter
		if len(rs) > 3 {
			rs = append(rs[:pos], rs[pos+1:]...)
		}
	default: // substitute nearby key
		if repl, ok := typoReplace[unicode.ToLower(rs[pos])]; ok {
			if unicode.IsUpper(rs[pos]) {
				repl = unicode.ToUpper(repl)
			}
			rs[pos] = repl
		} else {
			rs = insertRune(rs, pos, rs[pos])
		}
	}
	return string(rs)
}

func insertRune(rs []rune, pos int, r rune) []rune {
	out := make([]rune, 0, len(rs)+1)
	out = append(out, rs[:pos+1]...)
	out = append(out, r)
	out = append(out, rs[pos+1:]...)
	return out
}

// common typo mapping
var typoReplace = map[rune]rune{
	'а': 'о', 'о': 'а', 'е': 'и', 'и': 'е', 'т': 'ь', 'ь': 'т',
	'н': 'м', 'м': 'н', 'с': 'з', 'з': 'с', 'б': 'п', 'п': 'б',
	'д': 'т', 'в': 'б', 'г': 'х', 'х': 'г', 'к': 'г', 'л': 'к',
	'р': 'п', 'у': 'и', 'ы': 'и', 'ч': 'ш', 'ш': 'щ', 'щ': 'ш',
	'ц': 'ч', 'й': 'ц', 'ф': 'в', 'э': 'е', 'ю': 'у', 'я': 'а',
	'ё': 'е',
	'a': 's', 's': 'a', 'e': 'r', 'r': 'e', 'i': 'o', 'o': 'i',
	't': 'r', 'n': 'm', 'm': 'n', 'd': 's', 'g': 'h', 'h': 'g',
	'l': 'k', 'k': 'l', 'p': 'o', 'u': 'i', 'y': 't', 'c': 'v',
	'v': 'c', 'b': 'v', 'f': 'd', 'w': 'q', 'q': 'w', 'z': 'x',
	'x': 'z',
}

// split sentences by terminal punctuation and newlines
func splitSentences(text string) []string {
	var out []string
	var cur strings.Builder
	runes := []rune(text)

	for i := 0; i < len(runes); i++ {
		cur.WriteRune(runes[i])
		r := runes[i]
		if r == '.' || r == '!' || r == '?' || r == '\n' {
			// consume trailing whitespace
			j := i + 1
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '\t') {
				cur.WriteRune(runes[j])
				j++
			}
			out = append(out, cur.String())
			cur.Reset()
			i = j - 1
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
