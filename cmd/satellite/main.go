package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"homevoice/internal/satellite"
)

func main() {
	mode := flag.String("mode", "voice", "voice или text")
	gateway := flag.String("gateway", "ws://127.0.0.1:3000/gemini-live", "WebSocket URL Go gateway")
	timeout := flag.Duration("timeout", 45*time.Second, "максимальная длительность одной сессии")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client := satellite.Client{Gateway: *gateway, Output: os.Stdout}

	var err error
	switch *mode {
	case "voice":
		err = client.RunVoice(ctx)
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
		err = client.RunText(ctx, command)
	default:
		err = fmt.Errorf("неизвестный режим %q: используй voice или text", *mode)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Ошибка:", err)
		os.Exit(1)
	}
}
