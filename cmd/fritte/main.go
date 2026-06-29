package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"fritte/internal/eventlog"
	"fritte/internal/fritz"
	"fritte/internal/server"
)

func main() {
	cfg := loadConfig()

	client := fritz.NewClient(cfg.Host, cfg.Username, cfg.Password)
	log.Printf("fritte startet: host=%s interval=%s addr=%s", cfg.Host, cfg.PollInterval, cfg.Addr)

	scraper := fritz.NewScraper(client, fritz.DefaultEndpoints(), cfg.PollInterval)
	scraper.SetBoxHost(strings.TrimPrefix(cfg.Host, "https://"))

	var eventlogStore *eventlog.Store
	var syslogSender *eventlog.Sender
	if cfg.DatabaseURL != "" {
		var err error
		eventlogStore, err = eventlog.NewStore(cfg.DatabaseURL)
		if err != nil {
			log.Fatalf("eventlog store: %v", err)
		}
		defer eventlogStore.Close()
		syslogSender = eventlog.NewSender(cfg.SyslogHost, cfg.SyslogPort, cfg.SyslogProtocol)
		scraper.WithEventlog(eventlogStore, syslogSender)
		log.Printf("eventlog: postgres=%s syslog=%s:%s/%s", cfg.DatabaseURL, cfg.SyslogHost, cfg.SyslogPort, cfg.SyslogProtocol)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go scraper.Run(ctx)

	srv, err := server.New(scraper)
	if err != nil {
		log.Fatalf("server init: %v", err)
	}
	mux := http.NewServeMux()
	srv.Routes(mux)
	httpSrv := &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		log.Printf("dashboard auf http://0.0.0.0%s", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutdown ...")
	cancel()
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sCancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

type config struct {
	Host         string
	Username     string
	Password     string
	PollInterval time.Duration
	Addr         string

	DatabaseURL    string
	SyslogHost     string
	SyslogPort     string
	SyslogProtocol string
}

func loadConfig() config {
	c := config{
		Host:           envOr("FRITZ_HOST", "https://fritz.box"),
		Username:       os.Getenv("FRITZ_USERNAME"),
		Password:       os.Getenv("FRITZ_PASSWORD"),
		PollInterval:   parseDuration(envOr("POLL_INTERVAL", "5s"), 5*time.Second),
		Addr:           envOr("DASHBOARD_ADDR", ":8080"),
		DatabaseURL:    os.Getenv("DATABASE_URL"),
		SyslogHost:     os.Getenv("SYSLOG_HOST"),
		SyslogPort:     envOr("SYSLOG_PORT", "514"),
		SyslogProtocol: envOr("SYSLOG_PROTOCOL", "udp"),
	}
	if c.Username == "" || c.Password == "" {
		log.Fatalf("FRITZ_USERNAME und FRITZ_PASSWORD muessen gesetzt sein (Env)")
	}
	return c
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseDuration(s string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}
