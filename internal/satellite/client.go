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
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"homevoice/internal/wakeword"
)

type Client struct {
	Gateway     string
	Provider    string
	SatelliteID string
	Output      io.Writer
}

type WakeConfig struct {
	Engine         string
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
	AudioMS  int            `json:"audioMs,omitempty"`
}

type sessionMetrics struct {
	audioBytes atomic.Int64
}

// Half a second is enough to restore a command prefix consumed by wake-word
// detection without replaying the entire wake phrase as a separate speech turn.
const wakePreRollBytes = captureRate * pcmBytes / 2

func (metrics *sessionMetrics) audioDuration() time.Duration {
	return time.Duration(float64(metrics.audioBytes.Load()) / float64(captureRate*pcmBytes) * float64(time.Second))
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
	return client.receiveTurn(ctx, connection, nil, &sessionMetrics{})
}

func (client Client) RunVoice(ctx context.Context) error {
	return client.runVoice(ctx, nil, nil)
}

func (client Client) runVoice(ctx context.Context, audio *Audio, preRoll []byte) error {
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

	if audio == nil {
		audio, err = NewAudio()
		if err != nil {
			return fmt.Errorf("аудио: %w", err)
		}
		defer audio.Close()
		if err := audio.Start(); err != nil {
			return fmt.Errorf("запуск аудио: %w", err)
		}
	}
	buffered := audio.BufferedDuration() + pcmDuration(len(preRoll))
	if buffered > 0 {
		fprintf(client.Output, "Аудио: в буфере после wake ~%.2f с\n", buffered.Seconds())
	}
	fprintf(client.Output, "Слушаю. Скажи команду…\n")

	writeCtx, stopWriter := context.WithCancel(ctx)
	defer stopWriter()
	writerErrors := make(chan error, 1)
	metrics := &sessionMetrics{}
	go func() {
		if len(preRoll) > 0 {
			metrics.audioBytes.Add(int64(len(preRoll)))
			if err := wsjson.Write(writeCtx, connection, outbound{
				Type: "audio", Data: base64.StdEncoding.EncodeToString(preRoll),
			}); err != nil {
				writerErrors <- err
				return
			}
		}
		for {
			select {
			case <-writeCtx.Done():
				writerErrors <- nil
				return
			case chunk := <-audio.Input():
				metrics.audioBytes.Add(int64(len(chunk)))
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

	err = client.receiveTurn(ctx, connection, audio, metrics)
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
	var detector wakeword.StreamDetector
	var err error
	if config.Engine == "micro" {
		detector, err = wakeword.NewMicro(wakeword.MicroConfig{
			AssetsDir: config.AssetsDir,
			Threshold: config.Threshold,
		})
	} else {
		detector, err = wakeword.New(wakeword.Config{
			AssetsDir:    config.AssetsDir,
			Threshold:    config.Threshold,
			VADThreshold: config.VADThreshold,
		})
	}
	if err != nil {
		return err
	}
	defer detector.Close()

	detections := 0
	for {
		audio, preRoll, wakeErr := client.waitForWake(ctx, detector, config.Debug)
		if wakeErr != nil {
			if ctx.Err() != nil || errors.Is(wakeErr, context.Canceled) {
				return nil
			}
			return wakeErr
		}
		if config.TestOnly {
			detections++
			fprintf(client.Output, "Тест: успешных срабатываний %d. Продолжаю слушать…\n", detections)
			audio.Close()
			if err := detector.Reset(); err != nil {
				return err
			}
			continue
		}
		sessionCtx := ctx
		cancel := func() {}
		if config.SessionTimeout > 0 {
			sessionCtx, cancel = context.WithTimeout(ctx, config.SessionTimeout)
		}
		err := client.runVoice(sessionCtx, audio, preRoll)
		cancel()
		audio.Close()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			fprintf(client.Output, "Голосовая сессия: %v\n", err)
		}
		if err := detector.Reset(); err != nil {
			return err
		}
	}
}

func (client Client) waitForWake(ctx context.Context, detector wakeword.StreamDetector, debug bool) (*Audio, []byte, error) {
	audio, err := NewAudio()
	if err != nil {
		return nil, nil, fmt.Errorf("wake audio: %w", err)
	}
	if err := audio.Start(); err != nil {
		audio.Close()
		return nil, nil, fmt.Errorf("wake audio start: %w", err)
	}
	fprintf(client.Output, "Жду: «%s»…\n", detector.Phrase())
	debugStarted := time.Now()
	var debugMaximum float32
	debugPeakDBFS := -96.0
	preRoll := make([]byte, 0, wakePreRollBytes)
	for {
		select {
		case <-ctx.Done():
			audio.Close()
			return nil, nil, ctx.Err()
		case chunk := <-audio.Input():
			preRoll = appendPCMPreRoll(preRoll, chunk, wakePreRollBytes)
			if debug {
				debugPeakDBFS = max(debugPeakDBFS, pcmPeakDBFS(chunk))
			}
			detected, score, err := detector.ProcessPCM16(chunk)
			if err != nil {
				audio.Close()
				return nil, nil, fmt.Errorf("wake inference: %w", err)
			}
			if detected {
				fprintf(client.Output, "Wake word услышан · score %.3f\n", score)
				return audio, preRoll, nil
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

func appendPCMPreRoll(buffer, chunk []byte, limit int) []byte {
	if limit <= 0 {
		return nil
	}
	if len(chunk) >= limit {
		return append(buffer[:0], chunk[len(chunk)-limit:]...)
	}
	overflow := len(buffer) + len(chunk) - limit
	if overflow > 0 {
		copy(buffer, buffer[overflow:])
		buffer = buffer[:len(buffer)-overflow]
	}
	return append(buffer, chunk...)
}

func pcmDuration(bytes int) time.Duration {
	return time.Duration(float64(bytes) / float64(captureRate*pcmBytes) * float64(time.Second))
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

func (client Client) receiveTurn(ctx context.Context, connection *websocket.Conn, audio *Audio, metrics *sessionMetrics) error {
	toolExecuted := false
	mutedForReply := false
	var textResponse strings.Builder
	var assistantResponse strings.Builder
	for {
		message, err := receive(ctx, connection)
		if err != nil {
			if errors.Is(err, context.Canceled) || isNormalClose(err) {
				return nil
			}
			return err
		}
		switch message.Type {
		case "speech_started":
			fprintf(client.Output, "OpenAI VAD: речь началась · audio %d мс · отправлено %.2f с\n", message.AudioMS, metrics.audioDuration().Seconds())
		case "speech_stopped":
			fprintf(client.Output, "OpenAI VAD: речь закончилась · audio %d мс · отправлено %.2f с\n", message.AudioMS, metrics.audioDuration().Seconds())
		case "input_transcript":
			fprintf(client.Output, "Ты: %s\n", strings.TrimSpace(message.Text))
		case "output_transcript":
			if audio != nil && !mutedForReply {
				audio.Listen(false)
				mutedForReply = true
			}
			fprintf(client.Output, "Ассистент: %s\n", strings.TrimSpace(message.Text))
			assistantResponse.WriteString(message.Text)
		case "text_delta":
			assistantResponse.WriteString(message.Text)
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
			if toolExecuted || audio == nil || !requestsClarification(assistantResponse.String()) {
				return nil
			}
			assistantResponse.Reset()
			mutedForReply = false
			audio.Listen(true)
			fprintf(client.Output, "Слушаю уточнение…\n")
		}
	}
}

func requestsClarification(response string) bool {
	normalized := strings.ToLower(strings.TrimSpace(response))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "?") {
		return true
	}
	for _, marker := range []string{"уточни", "какой ", "какая ", "какое ", "какие ", "в какой ", "что именно"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
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
	if client.SatelliteID != "" {
		query.Set("satellite_id", client.SatelliteID)
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
