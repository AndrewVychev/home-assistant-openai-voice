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
	satelliteID := flag.String("satellite-id", envOr("HOMEVOICE_SATELLITE_ID", "local"), "стабильное имя микрофона для краткосрочного контекста")
	satelliteToken := flag.String("token", envOr("SATELLITE_TOKEN", ""), "Bearer-токен доступа к gateway")
	timeout := flag.Duration("timeout", 45*time.Second, "максимальная длительность одной сессии")
	wakeEngine := flag.String("wake-engine", envOr("HOMEVOICE_WAKE_ENGINE", "micro"), "wake engine: micro (Куза) или openwakeword (Hey Jarvis)")
	wakeAssets := flag.String("wake-assets", "", "каталог моделей выбранного wake engine")
	wakeThreshold := flag.Float64("wake-threshold", 0, "порог от 0 до 1; 0 использует значение модели")
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
	client := satellite.Client{Gateway: *gateway, Provider: *provider, SatelliteID: strings.TrimSpace(*satelliteID), Token: strings.TrimSpace(*satelliteToken), Output: os.Stdout}

	var err error
	switch *mode {
	case "wake", "wake-test":
		*wakeEngine = strings.ToLower(strings.TrimSpace(*wakeEngine))
		if *wakeEngine != "micro" && *wakeEngine != "openwakeword" {
			err = fmt.Errorf("неизвестный wake engine %q: используй micro или openwakeword", *wakeEngine)
			break
		}
		if *wakeThreshold < 0 || *wakeThreshold > 1 {
			err = fmt.Errorf("wake-threshold должен быть от 0 до 1")
			break
		}
		if *wakeThreshold == 0 && *wakeEngine == "openwakeword" {
			*wakeThreshold = 0.35
		}
		assets, assetsErr := resolveWakeAssets(*wakeEngine, *wakeAssets)
		if assetsErr != nil {
			err = assetsErr
			break
		}
		err = client.RunWake(ctx, satellite.WakeConfig{
			Engine:         *wakeEngine,
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

func resolveWakeAssets(engine, configured string) (string, error) {
	if configured == "" {
		if engine == "micro" {
			configured = strings.TrimSpace(os.Getenv("HOMEVOICE_MICRO_WAKE_ASSETS"))
		} else {
			configured = strings.TrimSpace(os.Getenv("HOMEVOICE_WAKE_ASSETS"))
		}
	}
	candidates := make([]string, 0, 3)
	if configured != "" {
		candidates = append(candidates, configured)
	} else {
		assetsName := "wakeword"
		if engine == "micro" {
			assetsName = "wakeword-micro"
		}
		candidates = append(candidates, assetsName)
		if executable, err := os.Executable(); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(executable), "..", assetsName))
		}
	}
	for _, candidate := range candidates {
		absolute, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if engine == "micro" {
			python := filepath.Join(absolute, "venv", "bin", "python")
			if runtime.GOOS == "windows" {
				python = filepath.Join(absolute, "venv", "Scripts", "python.exe")
			}
			if _, err := os.Stat(filepath.Join(absolute, "Kuza.json")); err == nil {
				if _, err := os.Stat(python); err == nil {
					return absolute, nil
				}
			}
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
	if engine == "micro" {
		return "", fmt.Errorf("модель Куза не найдена; выполни `make setup-kuza` или передай -wake-assets /полный/путь/wakeword-micro")
	}
	return "", fmt.Errorf("wake word assets не найдены; выполни `make setup-wakeword` в каталоге проекта или передай -wake-assets /полный/путь/wakeword")
}
