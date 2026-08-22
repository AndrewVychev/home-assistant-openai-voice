package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"homevoice/internal/satellite"
)

func main() {
	mode := flag.String("mode", "wake", "wake, wake-test, voice или text")
	gateway := flag.String("gateway", "ws://127.0.0.1:3000/live", "WebSocket URL Go gateway")
	provider := flag.String("provider", envOr("VOICE_PROVIDER", "gemini"), "voice provider: gemini или openai")
	timeout := flag.Duration("timeout", 45*time.Second, "максимальная длительность одной сессии")
	wakeAssets := flag.String("wake-assets", "", "каталог моделей и ONNX Runtime")
	wakeThreshold := flag.Float64("wake-threshold", 0.35, "порог Hey Jarvis от 0 до 1")
	vadThreshold := flag.Float64("vad-threshold", 0, "порог голосовой активности от 0 до 1; 0 отключает VAD")
	wakeDebug := flag.Bool("wake-debug", false, "показывать максимальный wake score раз в секунду")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	*provider = strings.ToLower(strings.TrimSpace(*provider))
	if *provider != "gemini" && *provider != "openai" {
		fmt.Fprintf(os.Stderr, "Ошибка: неизвестный provider %q: используй gemini или openai\n", *provider)
		os.Exit(1)
	}
	client := satellite.Client{Gateway: *gateway, Provider: *provider, Output: os.Stdout}

	var err error
	switch *mode {
	case "wake", "wake-test":
		assets, assetsErr := resolveWakeAssets(*wakeAssets)
		if assetsErr != nil {
			err = assetsErr
			break
		}
		err = client.RunWake(ctx, satellite.WakeConfig{
			AssetsDir:      assets,
			Threshold:      float32(*wakeThreshold),
			VADThreshold:   float32(*vadThreshold),
			Debug:          *wakeDebug || *mode == "wake-test",
			TestOnly:       *mode == "wake-test",
			SessionTimeout: *timeout,
		})
	case "voice":
		sessionCtx, cancel := context.WithTimeout(ctx, *timeout)
		err = client.RunVoice(sessionCtx)
		cancel()
	case "text":
		command := strings.TrimSpace(strings.Join(flag.Args(), " "))
		if command == "" {
			fmt.Print("Команда: ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				command = strings.TrimSpace(scanner.Text())
			}
			if scanErr := scanner.Err(); scanErr != nil {
				err = scanErr
				break
			}
		}
		if command == "" {
			err = fmt.Errorf("пустая команда")
			break
		}
		sessionCtx, cancel := context.WithTimeout(ctx, *timeout)
		err = client.RunText(sessionCtx, command)
		cancel()
	default:
		err = fmt.Errorf("неизвестный режим %q: используй wake, wake-test, voice или text", *mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func resolveWakeAssets(configured string) (string, error) {
	if configured == "" {
		configured = strings.TrimSpace(os.Getenv("HOMEVOICE_WAKE_ASSETS"))
	}
	candidates := make([]string, 0, 3)
	if configured != "" {
		candidates = append(candidates, configured)
	} else {
		candidates = append(candidates, "wakeword")
		if executable, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(executable), "..", "wakeword"))
		}
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		library := "libonnxruntime.so"
		if runtime.GOOS == "darwin" {
			library = "libonnxruntime.dylib"
		} else if runtime.GOOS == "windows" {
			library = "onnxruntime.dll"
		}
		if _, err := os.Stat(filepath.Join(absolute, "runtime", library)); err == nil {
			return absolute, nil
		}
	}
	return "", fmt.Errorf("wake word assets не найдены; выполни `make setup-wakeword` в каталоге проекта или передай -wake-assets /полный/путь/wakeword")
}
