package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"hermes-tele/config"
	"hermes-tele/internal/bot"
)

var version = "2.1.0"

func main() {
	configPath := flag.String("config", "config.json", "Path to config JSON file")
	flag.Parse()

	log.Printf("[hermes-tele] Starting Hermes Telegram Gateway v%s (Golang Native 1:1 Engine)", version)

	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("[hermes-tele] Fatal: Gagal memuat file konfigurasi %s: %v", *configPath, err)
	}

	if cfg.Telegram.BotToken == "" {
		log.Fatalf("[hermes-tele] Fatal: Token bot Telegram kosong! Periksa %s", *configPath)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	server, err := bot.NewBotServer(cfg, version)
	if err != nil {
		log.Fatalf("[hermes-tele] Fatal: Gagal menginisialisasi bot server: %v", err)
	}

	if err := server.Start(ctx); err != nil {
		log.Fatalf("[hermes-tele] Bot server exited with error: %v", err)
	}

	log.Println("[hermes-tele] Hermes Telegram Gateway dihentikan dengan aman.")
}
