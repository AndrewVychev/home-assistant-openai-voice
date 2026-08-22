package satellite

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"homevoice/internal/wakeword"
)

type Client struct {
	Gateway  string
	Provider string
	Output   io.Writer
}

type WakeConfig struct {
	AssetsDir      string
	Threshold      float32
	VADThreshold   float32
	Debug          bool
	TestOnly       bool
	SessionTimeout time.Duration
}

type event struct {
	Type     string         `json:"type"`
	Provider string         `json:"provider,omitempty"`
	Model    string         `json:"model,omitempty"`
	Text     string         `json:"text,omitempty"`
	Data     string         `json:"data,omitempty"`
	MIMEType string         `json:"mimeType,omitempty"`
	Name     string         `json:"name,omitempty"`
	Args     map[string]any `json:"args,omitempty"`
	Result   map[string]any `json:"result,omitempty"`
	Message  string         `json:"message,omitempty"`
}

type outbound struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Text string `json:"text,omitempty"`
}

func (client Client) RunText(ctx context.Context, command string) error {
	connection, err := client.connect(ctx, "text")
	if err != nil {
		return err
	}
	defer connection.Close(websocket.StatusNormalClosure, "done")

	ready, err := receive(ctx, connection)
	if err != nil {
		return err
	}
	if ready.Type != "ready" {
		return eventError(ready)
	}
	fprintf(client.Output, "Подключено: %s · %s\n", ready.Provider, ready.Model)
	if err := wsjson.Write(ctx, connection, outbound{Type: "text", Text: command}); err != nil {
		return err
	}
	return client.receiveTurn(ctx, connection, nil)
}

func (client Client) RunVoice(ctx context.Context) error {
	connection, err := client.connect(ctx, "audio")
	if err != nil {
		return err
	}
	defer connection.Close(websocket.StatusNormalClosure, "done")

	ready, err := receive(ctx, connection)
	if err != nil {
		return err
	}
	if ready.Type != "ready" {
		return eventError(ready)
	}
	fprintf(client.Output, "Подключено: %s · %s\n", ready.Provider, ready.Model)

	audio, err := NewAudio()
	if err != nil {
		return fmt.Errorf("аудио: %w", err)
	}
	defer audio.Close()
	if err := audio.Start(); err != nil {
		return fmt.Errorf("запуск аудио: %w", err)
	}
	fprintf(client.Output, "Слушаю. Скажи команду…\n")

	writeCtx, stopWriter := context.WithCancel(ctx)
	defer stopWriter()
	writerErrors := make(chan error, 1)
	go func() {
		for {
			select {
			case <-writeCtx.Done():
				writerErrors <- nil
				return
			case chunk := <-audio.Input():
				err := wsjson.Write(writeCtx, connection, outbound{
					Type: "audio",
					Data: base64.StdEncoding.EncodeToString(chunk),
				})
				if err != nil {
					writerErrors <- err
					return
				}
			}
		}
	}()

	err = client.receiveTurn(ctx, connection, audio)
	stopWriter()
	select {
	case writerErr := <-writerErrors:
		if err == nil && writerErr != nil && !isNormalClose(writerErr) {
			err = writerErr
		}
	case <-time.After(time.Second):
	}
	return err
}

func (client Client) RunWake(ctx context.Context, config WakeConfig) error {
	detector, err := wakeword.New(wakeword.Config{
		AssetsDir:    config.AssetsDir,
		Threshold:    config.Threshold,
		VADThreshold: config.VADThreshold,
	})
	if err != nil {
		return err
	}
	defer detector.Close()

	detections := 0
	for {
		if err := client.waitForWake(ctx, detector, config.Debug); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if config.TestOnly {
			detections++
			fprintf(client.Output, "Тест: успешных срабатываний %d. Продолжаю слушать…\n", detections)
			detector.Reset()
			continue
		}
		sessionCtx := ctx
		cancel := func() {}
		if config.SessionTimeout > 0 {
			sessionCtx, cancel = context.WithTimeout(ctx, config.SessionTimeout)
		}
		err := client.RunVoice(sessionCtx)
		cancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			fprintf(client.Output, "Голосовая сессия: %v\n", err)
		}
		detector.Reset()
	}
}

func (client Client) waitForWake(ctx context.Context, detector *wakeword.Detector, debug bool) error {
	audio, err := NewAudio()
	if err != nil {
		return fmt.Errorf("wake audio: %w", err)
	}
	defer audio.Close()
	if err := audio.Start(); err != nil {
		return fmt.Errorf("wake audio start: %w", err)
	}
	fprintf(client.Output, "Жду: «Хей, Джарвис»…\n")
	debugStarted := time.Now()
	var debugMaximum float32
	debugPeakDBFS := -96.0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case chunk := <-audio.Input():
			if debug {
				debugPeakDBFS = max(debugPeakDBFS, pcmPeakDBFS(chunk))
			}
			detected, score, err := detector.ProcessPCM16(chunk)
			if err != nil {
				return fmt.Errorf("wake inference: %w", err)
			}
			if detected {
				audio.Listen(false)
				fprintf(client.Output, "Wake word услышан · score %.3f\n", score)
				audio.PlayWakeChime()
				audio.WaitPlayback(time.Second)
				return nil
			}
			if debug {
				if score > debugMaximum {
					debugMaximum = score
				}
				if time.Since(debugStarted) >= time.Second {
					fprintf(client.Output, "mic peak: %.1f dBFS · wake max: %.3f\n", debugPeakDBFS, debugMaximum)
					debugMaximum = 0
					debugPeakDBFS = -96
					debugStarted = time.Now()
				}
			}
		}
	}
}

func pcmPeakDBFS(data []byte) float64 {
	var peak int32
	for offset := 0; offset+1 < len(data); offset += 2 {
		value := int32(int16(binary.LittleEndian.Uint16(data[offset:])))
		if value < 0 {
			value = -value
		}
		if value > peak {
			peak = value
		}
	}
	if peak == 0 {
		return -96
	}
	return 20 * math.Log10(float64(peak)/32768)
}

func (client Client) receiveTurn(ctx context.Context, connection *websocket.Conn, audio *Audio) error {
	toolExecuted := false
	mutedForReply := false
	var textResponse strings.Builder
	for {
		message, err := receive(ctx, connection)
		if err != nil {
			if errors.Is(err, context.Canceled) || isNormalClose(err) {
				return nil
			}
			return err
		}
		switch message.Type {
		case "input_transcript":
			fprintf(client.Output, "Ты: %s\n", strings.TrimSpace(message.Text))
		case "output_transcript":
			if audio != nil && !mutedForReply {
				audio.Listen(false)
				mutedForReply = true
			}
			fprintf(client.Output, "Ассистент: %s\n", strings.TrimSpace(message.Text))
		case "text_delta":
			if audio == nil {
				textResponse.WriteString(message.Text)
			}
		case "audio_delta":
			if audio != nil {
				if !mutedForReply {
					audio.Listen(false)
					mutedForReply = true
				}
				data, decodeErr := base64.StdEncoding.DecodeString(message.Data)
				if decodeErr != nil {
					return decodeErr
				}
				audio.Play(data)
			}
		case "tool_call":
			toolExecuted = true
			textResponse.Reset()
			if audio != nil {
				audio.Listen(false)
				mutedForReply = true
			}
			fprintf(client.Output, "Дом: %s %v\n", message.Name, message.Args)
		case "ha_result":
			fprintf(client.Output, "Home Assistant: %s\n", compactJSON(message.Result))
		case "error":
			return eventError(message)
		case "closed":
			return errors.New("gateway закрыл Live-сессию")
		case "turn_complete":
			if audio == nil && textResponse.Len() > 0 {
				fprintf(client.Output, "%s\n", strings.TrimSpace(textResponse.String()))
			}
			if audio != nil {
				audio.WaitPlayback(6 * time.Second)
			}
			if toolExecuted || audio == nil {
				return nil
			}
			mutedForReply = false
			audio.Listen(true)
			fprintf(client.Output, "Слушаю уточнение…\n")
		}
	}
}

func (client Client) connect(ctx context.Context, responseMode string) (*websocket.Conn, error) {
	endpoint, err := url.Parse(client.Gateway)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("response_mode", responseMode)
	if client.Provider != "" {
		query.Set("provider", client.Provider)
	}
	endpoint.RawQuery = query.Encode()
	connection, _, err := websocket.Dial(ctx, endpoint.String(), nil)
	return connection, err
}

func receive(ctx context.Context, connection *websocket.Conn) (event, error) {
	var message event
	err := wsjson.Read(ctx, connection, &message)
	return message, err
}

func eventError(message event) error {
	if message.Message != "" {
		return errors.New(message.Message)
	}
	return fmt.Errorf("неожиданное сообщение gateway: %s", message.Type)
}

func compactJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func fprintf(output io.Writer, format string, values ...any) {
	if output != nil {
		_, _ = fmt.Fprintf(output, format, values...)
	}
}

func isNormalClose(err error) bool {
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || status == websocket.StatusNoStatusRcvd
}
