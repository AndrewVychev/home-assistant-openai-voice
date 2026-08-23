package config

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Host                     string
	Port                     int
	HAURL                    string
	HAToken                  string
	DefaultProvider          string
	GeminiAPIKey             string
	GeminiModel              string
	OpenAIAPIKey             string
	OpenAIModel              string
	OpenAIVoice              string
	OpenAITranscriptionModel string
	SatelliteToken           string
	WakeAssets               string
	WakeThreshold            float32
}

func Load() (Config, error) {
	loadDotEnv(".env")
	port, err := strconv.Atoi(valueOrDefault("PORT", "3000"))
	if err != nil || port < 1 || port > 65535 {
		return Config{}, errors.New("PORT должен быть числом от 1 до 65535")
	}
	wakeThreshold, err := strconv.ParseFloat(valueOrDefault("HOMEVOICE_WAKE_THRESHOLD", "0.65"), 32)
	if err != nil || wakeThreshold < 0 || wakeThreshold > 1 {
		return Config{}, errors.New("HOMEVOICE_WAKE_THRESHOLD должен быть числом от 0 до 1")
	}
	config := Config{
		Host:                     valueOrDefault("HOST", "127.0.0.1"),
		Port:                     port,
		HAURL:                    strings.TrimRight(valueOrDefault("HA_URL", "http://homeassistant.local"), "/"),
		HAToken:                  strings.TrimSpace(os.Getenv("HA_TOKEN")),
		DefaultProvider:          strings.ToLower(valueOrDefault("VOICE_PROVIDER", "gemini")),
		GeminiAPIKey:             strings.TrimSpace(os.Getenv("GEMINI_API_KEY")),
		GeminiModel:              valueOrDefault("GEMINI_LIVE_MODEL", "gemini-3.1-flash-live-preview"),
		OpenAIAPIKey:             strings.TrimSpace(os.Getenv("OPENAI_API_KEY")),
		OpenAIModel:              valueOrDefault("OPENAI_REALTIME_MODEL", "gpt-realtime-2.1-mini"),
		OpenAIVoice:              valueOrDefault("OPENAI_VOICE", "marin"),
		OpenAITranscriptionModel: valueOrDefault("OPENAI_TRANSCRIPTION_MODEL", "gpt-4o-mini-transcribe"),
		SatelliteToken:           strings.TrimSpace(os.Getenv("SATELLITE_TOKEN")),
		WakeAssets:               valueOrDefault("HOMEVOICE_MICRO_WAKE_ASSETS", "wakeword-micro"),
		WakeThreshold:            float32(wakeThreshold),
	}
	if config.DefaultProvider != "gemini" && config.DefaultProvider != "openai" {
		return Config{}, errors.New("VOICE_PROVIDER должен быть gemini или openai")
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
