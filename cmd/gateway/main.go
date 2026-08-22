package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"homevoice/internal/config"
	"homevoice/internal/gateway"
)

func main() {
	logger := log.New(os.Stdout, "homevoice ", log.LstdFlags|log.Lmsgprefix)
	cfg, err := config.Load()
	if err != nil {
		logger.Fatal(err)
	}
	server := gateway.New(cfg, logger)
	address := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	logger.Printf("Go voice gateway: http://%s", address)
	logger.Printf("Home Assistant: %s", cfg.HAURL)
	logger.Printf("Gemini Live model: %s", cfg.GeminiModel)
	if err := http.ListenAndServe(address, server.Handler(".")); err != nil {
		logger.Fatal(err)
	}
}
