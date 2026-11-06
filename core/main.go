package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"time"

	"spermacore/core/dashboard"
	scEmbedsc "spermacore/core/embedsc"
	"spermacore/core/humanify"
	"spermacore/core/sandbox"
	"spermacore/core/webui"
)

const Version = "1.9.0"

func main() {
	configPath := flag.String("config", "config.toml", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	rebuild := flag.Bool("rebuild", false, "remove all module build dirs and force full rebuild")
	flag.Parse()

	if *showVersion {
		fmt.Printf("SpermaCore v%s\n", Version)
		os.Exit(0)
	}

	runtime.GOMAXPROCS(runtime.NumCPU())

	cfg, err := LoadConfigRaw(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
	SetGlobalConfig(cfg)
	if err := sandbox.Validate(sandbox.Config{
		Enabled:       cfg.Core.Sandbox.Enabled,
		Mode:          cfg.Core.Sandbox.Mode,
		AllowUnsigned: cfg.Core.Sandbox.AllowUnsigned,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}

	gDash = dashboard.New(Version, detectRedStarOS(), cfg.Core.DataDir)

	if err := InitLogger(cfg.LogsDir(), ParseLevel(cfg.Core.LogLevel)); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: logger init: %v\n", err)
		os.Exit(1)
	}

	if err := InitAppDB(cfg.Core.DataDir, cfg.Core.DBKey); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: sqlite db init: %v\n", err)
		os.Exit(1)
	}

	// webui
	var webInst *webui.Instance

	// snapshots for webui
	var (
		snapEndpoint string
		snapProvider string
		snapModel    string
	)

	{
		created, added, err := cfg.EnsureSection("webui", webui.DefaultsWithAuthToken())
		if err != nil {
			GetLogger("webui").Warn("config_ensure [webui] error: %v", err)
		} else if created {
			GetLogger("webui").Info("created default [webui] section")
		} else if added {
			GetLogger("webui").Info("added missing keys to [webui]")
		}

		webCfg := webui.Load(cfg.RootSections["webui"])
		if webCfg.Enabled {
			snapFn := func() webui.Snapshot {
				var stats dashboard.Stats
				transports := []webui.TransportPayload{}
				plugins := []webui.PluginPayload{}
				tools := []string{}
				var widgets []webui.WidgetPayload

				if gDash != nil {
					stats = gDash.Snapshot()
					for _, t := range gDash.SnapshotTransports() {
						transports = append(transports, webui.TransportPayload{
							Name: t.Name, Kind: t.Kind, Status: t.Status,
						})
					}
					for _, p := range gDash.SnapshotPlugins() {
						plugins = append(plugins, webui.PluginPayload{
							Name: p.Name, Version: p.Version, Desc: p.Desc,
							Enabled: p.Enabled, Loaded: p.Loaded,
						})
					}
					tools = gDash.SnapshotTools()
					if tools == nil {
						tools = []string{}
					}
				}

				widgets = widgetRegistry.SnapshotWidgets()

				return webui.Snapshot{
					Version:    Version,
					OS:         detectRedStarOS(),
					PID:        os.Getpid(),
					DataDir:    cfg.Core.DataDir,
					Provider:   snapProvider,
					Model:      snapModel,
					Endpoint:   snapEndpoint,
					Transports: transports,
					Plugins:    plugins,
					Tools:      tools,
					Widgets:    widgets,
					Stats: webui.StatsPayload{
						Messages:  stats.Messages,
						ToolCalls: stats.ToolCalls,
						Replies:   stats.Replies,
					},
					Metrics: webui.MetricsPayload{
						QueueLen: stats.QueueLen,
						Active:   stats.Active,
						Errors:   stats.Errors,
					},
					Uptime: 0,
				}
			}
			var werr error
			webInst, werr = webui.Start(webCfg, snapFn, func(f string, a ...any) {
				GetLogger("webui").Info(f, a...)
			})
			if werr != nil {
				GetLogger("webui").Error("startup error: %v", werr)
				webInst = nil
			} else if webInst != nil {
				webInst.Prompter().SetQRLoginHandler(telegramStartQRLogin)
				if gDash != nil {
					gDash.SetWebUIURL(webInst.URL())
				}
				GetLogger("webui").Info("webui available at: %s", webInst.URL())
			}
		} else {
			GetLogger("webui").Info("webui disabled (enable in config.toml: [webui].enabled = true)")
		}
	}

	go gDash.Run()

	// route logs to dashboard and webui
	SetUISink(func(l Level, name, msg string) {
		if gDash != nil {
			gDash.Log(mapLevel(l), name, msg)
		}
		if w := webui.Global(); w != nil {
			w.Log(levelName(l), name, msg)
		}
	})

	// ---- embedsc (optional service for the embedsc plugin) ----
	embeddingStarted := cfg.Memory.IsEnabled() && cfg.isPluginEnabled("embedsc")
	if embeddingStarted {
		scEmbedsc.SetLogger(func(format string, args ...any) { GetLogger("embedsc").Info(format, args...) })
		embed := resolveEmbeddingSettings(cfg)
		if embed.Disabled {
			GetLogger("embedsc").Info("vector memory disabled in settings")
			embeddingStarted = false
		} else {
			scEmbedsc.SetHNSWConfig(scEmbedsc.HNSWConfig{
				M:              cfg.Memory.HNSWM,
				M0:             cfg.Memory.HNSWM * 2,
				EfConstruction: cfg.Memory.HNSWEfConstruction,
				EfSearch:       cfg.Memory.HNSWEfSearch,
				Heuristic:      true,
			})
			embedDir := filepath.Join(cfg.Core.DataDir, "embedsc")
			if err := scEmbedsc.Init(embed.Base, embed.Model, embed.Key, embedDir); err != nil {
				GetLogger("embedsc").Warn("vector memory init error: %v", err)
				embeddingStarted = false
			} else {
				GetLogger("embedsc").Info("vector memory initialized: endpoint=%s model=%s index=hnsw(m=%d,ef_search=%d) dir=%s", embed.Base, embed.Model, cfg.Memory.HNSWM, cfg.Memory.HNSWEfSearch, embedDir)
			}
		}
	} else {
		GetLogger("embedsc").Info("vector memory disabled (memory.enabled=false or embedsc plugin not loaded)")
	}

	// ---- humanify ----
	{
		created, added, err := cfg.EnsureSection("humanify", humanify.Defaults)
		if err != nil {
			GetLogger("humanify").Warn("config_ensure [humanify] error: %v", err)
		} else if created {
			GetLogger("humanify").Info("created default [humanify] section")
		} else if added {
			GetLogger("humanify").Info("added missing keys to [humanify]")
		}
		hcfg := humanify.Load(cfg.RootSections["humanify"])
		humanify.Init(hcfg)
		GetLogger("humanify").Info("humanify active: delay=%v..%v, split=%v (%d chars), typo=%.0f%% (max=%d)",
			hcfg.DelayMin, hcfg.DelayMax,
			hcfg.SplitLong, hcfg.SplitThreshold,
			hcfg.TypoChance*100, hcfg.TypoMax)
	}

	log := GetLogger("core.agent")
	{
		created, added, err := cfg.EnsureSection("groups", groupPolicyDefaults)
		if err != nil {
			GetLogger("groups").Warn("config_ensure [groups] error: %v", err)
		} else if created {
			GetLogger("groups").Info("created default [groups] section")
		} else if added {
			GetLogger("groups").Info("added missing keys to [groups]")
		}
	}
	loadGroupPolicy(filepath.Dir(cfg.SourcePath), cfg.RootSections["groups"], log)
	{
		created, added, err := cfg.EnsureSection("context", contextDefaults)
		if err != nil {
			GetLogger("context").Warn("config_ensure [context] error: %v", err)
		} else if created {
			GetLogger("context").Info("created default [context] section")
		} else if added {
			GetLogger("context").Info("added missing keys to [context]")
		}
	}
	loadChatContextConfig(cfg.RootSections["context"])
	log.Info("starting spermacore core v%s...", Version)
	log.Info("os: %s, pid: %d, cpu cores: %d", detectRedStarOS(), os.Getpid(), runtime.NumCPU())
	log.Info("data dir: %s", cfg.Core.DataDir)
	if n := cleanupStaleSearchDumps(cfg.Core.DataDir, time.Now()); n > 0 {
		log.Info("cleaned %d stale search dumps from %s", n, cfg.Core.DataDir)
	}

	// clean module build dirs if -rebuild is set
	if *rebuild {
		log.Info("-rebuild flag: full module rebuild")
		cleanAllModuleBuilds(filepath.Dir(cfg.SourcePath), log)
	}

	addons := loadAddons(filepath.Dir(cfg.SourcePath), cfg, log)
	setEventAddons(addons, log)

	// register plugin widgets and start periodic updater
	registerWidgetsFromAddons(addons, cfg, log)
	widgetRegistry.startUpdater()

	// wire webui click handler with widget registry
	if webInst != nil {
		webInst.Prompter().SetWidgetClickHandler(func(id, payload string) string {
			return widgetRegistry.handleClick(id, payload)
		})
	}

	provider, endpoint, maxConc, err := buildProviders(cfg)
	if err != nil {
		log.Error("llm provider init error: %v", err)
		for _, a := range addons {
			a.deinit()
			a.Unload()
		}
		if webInst != nil {
			webInst.Stop()
		}
		if gDash != nil {
			gDash.Stop()
		}
		CloseAppDB()
		fmt.Printf("\n[!] startup error: llm provider %q not configured (%v).\n", cfg.LLM.Provider, err)
		fmt.Printf("    set api key in .env or config.toml and run again.\n\n")
		os.Exit(1)
	}
	SetGlobalProvider(provider)

	snapProvider = provider.Name()
	snapModel = cfg.ModelNameFor("chat")
	snapEndpoint = endpoint

	if gDash != nil {
		gDash.SetProvider(provider.Name(), snapModel, endpoint)
	}
	log.Info("main provider: %s, model: %s, max concurrent requests: %d", provider.Name(), snapModel, maxConc)

	for _, role := range []string{"vision", "embedding"} {
		if !cfg.HasRoute(role) {
			continue
		}
		log.Info("route %s: provider=%s model=%s", role, cfg.ProviderFor(role), cfg.ModelNameFor(role))
	}

	llmSem = make(chan struct{}, maxConc)

	workers := cfg.Core.Workers
	if workers <= 0 {
		workers = defaultWorkerCount
	}
	// task queue buffers incoming items for worker pool
	taskQueue = make(chan workerTask, workers*4)

	for i := 1; i <= workers; i++ {
		go worker(i, log)
	}
	log.Info("started worker pool: %d workers", workers)

	// initialize grouper after task queue
	{
		created, added, err := cfg.EnsureSection("grouper", grouperDefaults)
		if err != nil {
			GetLogger("grouper").Warn("config_ensure [grouper] error: %v", err)
		} else if created {
			GetLogger("grouper").Info("created default [grouper] section")
		} else if added {
			GetLogger("grouper").Info("added missing keys to [grouper]")
		}
		gcfg := LoadGrouperConfig(cfg.RootSections["grouper"])
		// submit func forwards grouped messages to worker queue
		submit := makeSubmitFunc("grouper", addons, provider, cfg, log)
		InitGrouper(gcfg, submit)
		GetLogger("grouper").Info("message grouper active: window=%v, max wait=%v",
			gcfg.Window, gcfg.MaxWait)
	}

	// register unified event ingest sink after grouper init
	setIngestSink(makeTransportSink("ingest", addons, provider, cfg, log))

	for _, a := range addons {
		if a.TransportName != "" {
			transportRegister(a.TransportName)
			gDash.AddTransport(a.TransportName, "plugin", "connecting")
			if webInst != nil {
				webInst.Transport(a.TransportName, "plugin", "connecting")
			}
			if a.HasNativeTransport() {
				log.Info("transport plugin: %s (module %s, abi 1.7, caps=0x%x)",
					a.TransportName, a.Name, a.TransportCapabilities())
			} else {
				log.Info("transport plugin: %s (module %s, hook)", a.TransportName, a.Name)
			}
		}
	}

	tools, _ := collectTools(addons, log)
	toolNames := make([]string, 0, len(tools))
	for _, t := range tools {
		fn, ok := t["function"].(map[string]any)
		if !ok {
			continue
		}
		name, ok := fn["name"].(string)
		if !ok || name == "" {
			continue
		}
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	gDash.SetTools(toolNames)

	for _, a := range addons {
		rc := a.OnStartup()
		if rc != 0 && rc != -7 {
			log.Warn("plugin %s: on_startup error (code=%d)", a.Name, rc)
		}
	}

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		pluginTick := 0
		stateTick := 0
		for range ticker.C {
			q := metricQueueLen.Load()
			a := metricActive.Load()
			e := metricErrors.Load()
			gDash.SetQueueMetrics(q, a, e)

			// refresh per-chat state counts every 5s
			stateTick++
			if stateTick >= 10 {
				stateTick = 0
				gDash.SetChatCounts(chatStateSnapshot())
			}

			if webInst != nil {
				webInst.Metrics(q, a, e)
				stats := gDash.Snapshot()
				webInst.Stats(stats.Messages, stats.ToolCalls, stats.Replies)

				pluginTick++
				if pluginTick >= 10 {
					pluginTick = 0
					plugs := []webui.PluginPayload{}
					for _, p := range gDash.SnapshotPlugins() {
						plugs = append(plugs, webui.PluginPayload{
							Name: p.Name, Version: p.Version, Desc: p.Desc,
							Enabled: p.Enabled, Loaded: p.Loaded,
						})
					}
					webInst.PluginList(plugs)
				}
			}
		}
	}()

	log.Info("spermacore running and ready. press ctrl+c to exit.")

	startConfigWatch(log)
	startChatStateJanitor(log)

	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	doneCh := make(chan struct{})
	go func() {
		count := 0
		for range sigCh {
			count++
			switch count {
			case 1:
				log.Info("shutdown: waiting for pending tasks... (ctrl+c x3 to force exit)")
				go func() {
					gracefulShutdown(log, addons, embeddingStarted)
					if webInst != nil {
						webInst.Stop()
					}
					close(doneCh)
				}()
			case 2:
				log.Warn("shutdown in progress - press ctrl+c again for immediate exit")
			default:
				forceExit(log)
			}
		}
	}()

	<-doneCh
	fmt.Printf("spermacore stopped. bye!\n")
}
