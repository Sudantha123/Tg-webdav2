// TgWebDAV — a Telegram-backed WebDAV + web file server in pure Go.
//
// Files sent to the bot inbox are forwarded to a Telegram channel and
// appear instantly in /dav (WebDAV) and /web (web UI). Content streams
// straight from Telegram with range/seek support.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Sudantha123/Tg-webdav2/config"
	"github.com/Sudantha123/Tg-webdav2/server"
	"github.com/Sudantha123/Tg-webdav2/store"
	"github.com/Sudantha123/Tg-webdav2/tgram"
)

// version is injected by -ldflags in CI/release builds.
var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-v" || os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("tgwebdav", version)
		return
	}

	log := newLogger()
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration error", "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Error("cannot create data dir", "dir", cfg.DataDir, "err", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(cfg.TmpDir, 0o755); err != nil {
		log.Error("cannot create tmp dir", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ---- metadata index ----
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Error("cannot open sqlite", "err", err)
		os.Exit(1)
	}
	defer st.Close()
	if err := st.EnsureFolder(ctx, "/"); err == nil {
		if err := st.SeedDefaultFolder(ctx, cfg.DefaultFolder); err != nil {
			log.Warn("seed default folder failed", "err", err)
		}
		def, _ := st.DefaultFolder(ctx, cfg.DefaultFolder)
		_ = st.EnsureFolder(ctx, def)
	}

	// ---- telegram MTProto (streaming) ----
	log.Info("connecting to telegram (MTProto)...")
	tg, err := tgram.Connect(ctx, tgram.Config{
		APIID:       cfg.APIID,
		APIHash:     cfg.APIHash,
		BotToken:    cfg.BotToken,
		ChannelID:   cfg.ChannelID,
		SessionPath: cfg.SessionPath,
		AppVersion:  version,
		Log:         log,
	})
	if err != nil {
		log.Error("telegram connect failed", "err", err)
		os.Exit(1)
	}
	log.Info("telegram connected")

	// ---- bot api (ingestion) ----
	bot := tgram.NewBotAPI(cfg.BotToken)
	me, err := bot.GetMe(ctx)
	if err != nil {
		log.Error("bot token invalid", "err", err)
		os.Exit(1)
	}
	log.Info("bot ready", "username", "@"+me.Username)

	ingester := &tgram.Ingester{
		Bot: bot,
		St:  st,
		Cfg: tgram.IngesterConfig{
			ChannelID:      cfg.ChannelID,
			AllowedUserIDs: cfg.AllowedUserIDs,
			Log:            log,
		},
	}
	go ingester.Run(ctx)

	// ---- cache + webdav fs ----
	cache := store.NewCache(int64(cfg.CacheMB) << 20)
	vfs := store.NewFS(st, tg, cache, cfg.TmpDir, cfg.Prefetch, cfg.DeleteFromTelegram)

	// ---- http ----
	srv := server.New(cfg, st, vfs, tg, bot, log)
	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	log.Info("tgwebdav started",
		"version", version,
		"port", cfg.Port,
		"webdav", fmt.Sprintf("http://<vps_ip>:%d/dav/", cfg.Port),
		"web", fmt.Sprintf("http://<vps_ip>:%d/web/", cfg.Port),
		"cache_mb", cfg.CacheMB,
	)

	ln, err := net.Listen("tcp", httpSrv.Addr)
	if err != nil {
		log.Error("listen failed", "err", err)
		os.Exit(1)
	}
	if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("http server", "err", err)
		os.Exit(1)
	}
	log.Info("bye")
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(h)
}
