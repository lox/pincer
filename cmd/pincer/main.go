package main

import (
	"log"
	"net/http"
	"os"

	"github.com/lox/pincer/internal/server"
)

func main() {
	addr := envOr("PINCER_HTTP_ADDR", ":8080")
	dbPath := envOr("PINCER_DB_PATH", "./pincer.db")
	tokenHMACKey := envOr("PINCER_TOKEN_HMAC_KEY", "")

	app, err := server.New(server.AppConfig{
		DBPath:       dbPath,
		TokenHMACKey: tokenHMACKey,
	})
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	defer app.Close()

	log.Printf("pincer listening on %s", addr)
	if err := http.ListenAndServe(addr, app.Handler()); err != nil {
		log.Fatalf("listen and serve: %v", err)
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
