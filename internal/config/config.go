package config

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Host         string
	Port         int
	HAURL        string
	HAToken      string
	GeminiAPIKey string
	GeminiModel  string
}

func Load() (Config, error) {
	loadDotEnv(".env")
	port, err := strconv.Atoi(valueOrDefault("PORT", "3000"))
	if err != nil || port < 1 || port > 65535 {
		return Config{}, errors.New("PORT должен быть числом от 1 до 65535")
	}
	config := Config{
		Host:         valueOrDefault("HOST", "127.0.0.1"),
		Port:         port,
		HAURL:        strings.TrimRight(valueOrDefault("HA_URL", "http://homeassistant.local"), "/"),
		HAToken:      strings.TrimSpace(os.Getenv("HA_TOKEN")),
		GeminiAPIKey: strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		GeminiModel:  valueOrDefault("GEMINI_LIVE_MODEL", "gemini-3.1-flash-live-preview"),
	}
	return config, nil
}

func valueOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || os.Getenv(strings.TrimSpace(name)) != "" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		_ = os.Setenv(strings.TrimSpace(name), value)
	}
}
