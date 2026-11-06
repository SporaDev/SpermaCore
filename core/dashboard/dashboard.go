// package dashboard handles terminal status line and state metrics.
package dashboard

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// log levels mirror core logger
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelTool
	LevelWarn
	LevelError
)

// public dashboard data structures
type PluginInfo struct {
	Name    string
	Version string
	Desc    string
	Enabled bool
	Loaded  bool
}

type TransportInfo struct {
	Name   string
	Kind   string
	Status string
}

type BotInfo struct {
	Enabled  bool
	Name     string
	Username string
	ID       int64
	Debug    bool
	Running  bool
}

type Stats struct {
	Messages  int64
	ToolCalls int64
	Replies   int64
	LastReply time.Duration
	TotalTime time.Duration
	QueueLen  int64
	Active    int64
	Errors    int64
	Chats     ChatCounts
}

// per-chat active counts for memory tracking
type ChatCounts struct {
	History     int
	Context     int
	Locks       int
	Groups      int
	Embed       int
	OwnMessages int
}

type LogEvent struct {
	Time  time.Time
	Level Level
	Name  string
	Msg   string
}

type Dashboard struct {
	mu sync.Mutex

	// identity
	version string
	osInfo  string
	pid     int
	dataDir string
	started time.Time

	// llm info
	provider string
	model    string
	endpoint string

	// plugins and tools
	plugins    []PluginInfo
	tools      []string
	transports []TransportInfo
	bot        BotInfo

	// stats
	stats Stats

	// log ring buffer
	logs   []LogEvent
	logCap int

	// webui url
	webUIURL string

	// channels
	logCh  chan LogEvent
	stopCh chan struct{}
	doneCh chan struct{}
}

// new creates dashboard instance
func New(version, osInfo, dataDir string) *Dashboard {
	return &Dashboard{
		version: version,
		osInfo:  osInfo,
		pid:     os.Getpid(),
		dataDir: dataDir,
		started: time.Now(),
		logCap:  14,
		logCh:   make(chan LogEvent, 256),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

// set webui url for terminal header
func (d *Dashboard) SetWebUIURL(url string) {
	d.mu.Lock()
	d.webUIURL = url
	d.mu.Unlock()
}

func (d *Dashboard) RefreshHeader() {
	d.printHeader()
}

// webui url returns configured endpoint
func (d *Dashboard) WebUIURL() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.webUIURL
}

func (d *Dashboard) SetProvider(provider, model, endpoint string) {
	d.mu.Lock()
	d.provider = provider
	d.model = model
	d.endpoint = endpoint
	d.mu.Unlock()
}

func (d *Dashboard) SetPlugins(plugins []PluginInfo) {
	d.mu.Lock()
	// copy slice to prevent race conditions
	d.plugins = append([]PluginInfo(nil), plugins...)
	d.mu.Unlock()
}

func (d *Dashboard) SetTools(tools []string) {
	d.mu.Lock()
	d.tools = append([]string(nil), tools...)
	d.mu.Unlock()
}

func (d *Dashboard) AddTransport(name, kind, status string) {
	d.mu.Lock()
	d.transports = append(d.transports, TransportInfo{Name: name, Kind: kind, Status: status})
	d.mu.Unlock()
}

func (d *Dashboard) UpdateTransport(name, status string) {
	d.mu.Lock()
	for i := range d.transports {
		if d.transports[i].Name == name {
			d.transports[i].Status = status
		}
	}
	d.mu.Unlock()
}

func (d *Dashboard) SetBot(name, username string, id int64, debug, running bool) {
	d.mu.Lock()
	d.bot = BotInfo{
		Enabled:  true,
		Name:     name,
		Username: username,
		ID:       id,
		Debug:    debug,
		Running:  running,
	}
	d.mu.Unlock()
}

func (d *Dashboard) SetQueueMetrics(queueLen, active, errors int64) {
	d.mu.Lock()
	d.stats.QueueLen = queueLen
	d.stats.Active = active
	d.stats.Errors = errors
	d.mu.Unlock()
}

// update per-chat metrics
func (d *Dashboard) SetChatCounts(c ChatCounts) {
	d.mu.Lock()
	d.stats.Chats = c
	d.mu.Unlock()
}

// non-blocking log event emission
func (d *Dashboard) Log(level Level, name, msg string) {
	select {
	case d.logCh <- LogEvent{time.Now(), level, name, msg}:
	default:
	}
}

func (d *Dashboard) TrackMessage() {
	d.mu.Lock()
	d.stats.Messages++
	d.mu.Unlock()
}

func (d *Dashboard) TrackToolCall() {
	d.mu.Lock()
	d.stats.ToolCalls++
	d.mu.Unlock()
}

func (d *Dashboard) TrackReply(dur time.Duration) {
	d.mu.Lock()
	d.stats.Replies++
	d.stats.LastReply = dur
	d.stats.TotalTime += dur
	d.mu.Unlock()
}

// snapshot returns copy of statistics
func (d *Dashboard) Snapshot() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stats
}

// snapshot plugins returns copy of plugin list
func (d *Dashboard) SnapshotPlugins() []PluginInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]PluginInfo, len(d.plugins))
	copy(out, d.plugins)
	return out
}

// snapshot transports returns copy of transport list
func (d *Dashboard) SnapshotTransports() []TransportInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]TransportInfo, len(d.transports))
	copy(out, d.transports)
	return out
}

// snapshot tools returns copy of tool list
func (d *Dashboard) SnapshotTools() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, len(d.tools))
	copy(out, d.tools)
	return out
}

// snapshot logs returns copy of log buffer
func (d *Dashboard) SnapshotLogs() []LogEvent {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]LogEvent, len(d.logs))
	copy(out, d.logs)
	return out
}

// run displays status line and periodic updates
func (d *Dashboard) Run() {
	defer close(d.doneCh)

	// clear screen on start
	fmt.Print("\033[2J\033[H")

	d.printHeader()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case ev := <-d.logCh:
			d.mu.Lock()
			d.logs = append(d.logs, ev)
			if len(d.logs) > d.logCap {
				// copy tail slice to release backing array
				tail := make([]LogEvent, d.logCap)
				copy(tail, d.logs[len(d.logs)-d.logCap:])
				d.logs = tail
			}
			d.mu.Unlock()

		case <-ticker.C:
			d.printStatusLine()

		case <-d.stopCh:
			fmt.Println()
			fmt.Print("\033[?25h")
			return
		}
	}
}

// stop terminates run loop
func (d *Dashboard) Stop() {
	select {
	case <-d.doneCh:
		return
	default:
	}
	close(d.stopCh)
	<-d.doneCh
}

// pause and resume stubs for auth compatibility
func (d *Dashboard) Pause()  {}
func (d *Dashboard) Resume() {}

func (d *Dashboard) printHeader() {
	d.mu.Lock()
	ver := d.version
	osInfo := d.osInfo
	dataDir := d.dataDir
	webURL := d.webUIURL
	d.mu.Unlock()

	const (
		reset  = "\033[0m"
		bold   = "\033[1m"
		dim    = "\033[2m"
		cyan   = "\033[36m"
		accent = "\033[38;5;180m" // warm beige accent
		green  = "\033[32m"
		yellow = "\033[33m"
	)

	fmt.Println()
	fmt.Printf("%s%sspermacore%s %sv%s%s\n", bold, cyan, reset, dim, ver, reset)
	fmt.Printf("  %sos:%s     %s\n", dim, reset, osInfo)
	fmt.Printf("  %spid:%s    %d\n", dim, reset, os.Getpid())
	fmt.Printf("  %sdata:%s   %s\n", dim, reset, dataDir)
	fmt.Println()

	if webURL != "" {
		fmt.Printf("  %s->%s  open dashboard: %s%s%s%s\n",
			green, reset,
			bold, accent, webURL, reset)
		fmt.Printf("     %s(clickable in terminal, ctrl+click)%s\n\n",
			dim, reset)
	} else {
		fmt.Printf("  %s!%s  webui disabled.\n", yellow, reset)
		fmt.Printf("     %senable in config.toml:%s\n", dim, reset)
		fmt.Printf("       %s[webui]%s\n", accent, reset)
		fmt.Printf("       %senabled = true%s\n", accent, reset)
		fmt.Printf("       %sbind    = \"127.0.0.1\"%s\n", accent, reset)
		fmt.Printf("       %sport    = 8080%s\n\n", accent, reset)
	}

	fmt.Printf("%s──────────────────────────────────────────────────────────%s\n", dim, reset)
	d.printStatusLine()
}

// redraw single status line using carriage return
func (d *Dashboard) printStatusLine() {
	d.mu.Lock()
	uptime := formatUptime(time.Since(d.started))
	msgs := d.stats.Messages
	tools := d.stats.ToolCalls
	replies := d.stats.Replies
	queue := d.stats.QueueLen
	active := d.stats.Active
	errs := d.stats.Errors
	chats := d.stats.Chats
	d.mu.Unlock()

	const (
		reset = "\033[0m"
		dim   = "\033[2m"
		bold  = "\033[1m"
		cyan  = "\033[36m"
		red   = "\033[31m"
	)

	errPart := ""
	if errs > 0 {
		errPart = fmt.Sprintf("  %serrors:%s %s%d%s", dim, reset, red, errs, reset)
	}

	fmt.Printf("\r%s[%s]%s msgs %s%d%s  tools %s%d%s  replies %s%d%s  queue %s%d%s/%s%d%s%s  %schats %d/%d/%d/%d/%d/%d%s\033[K",
		cyan, uptime, reset,
		bold, msgs, reset,
		bold, tools, reset,
		bold, replies, reset,
		cyan, queue, reset, cyan, active, reset,
		errPart,
		dim,
		chats.History, chats.Context, chats.Locks, chats.Groups, chats.Embed, chats.OwnMessages,
		reset)
}

func formatUptime(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}
