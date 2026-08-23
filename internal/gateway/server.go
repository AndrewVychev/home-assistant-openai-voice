package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"homevoice/internal/config"
	"homevoice/internal/homeassistant"
	liveapi "homevoice/internal/live"
	"homevoice/internal/wakeword"
)

type Server struct {
	config       config.Config
	home         *homeassistant.Client
	logger       *log.Logger
	providers    map[string]liveapi.Provider
	recentMu     sync.Mutex
	recentTarget map[string]recentTarget
}

const recentTargetTTL = 10 * time.Minute

const (
	serverWakePreRollBytes = 16000 * 2
	serverWakeAudioQueue   = 300
)

type recentTarget struct {
	EntityID  string
	Name      string
	ExpiresAt time.Time
}

type clientEvent struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Text string `json:"text,omitempty"`
}

type socketWriter struct {
	connection *websocket.Conn
	mu         sync.Mutex
}

type liveSession struct {
	session   liveapi.Session
	sendMu    sync.Mutex
	closeOnce sync.Once
}

func New(cfg config.Config, logger *log.Logger) *Server {
	return &Server{
		config: cfg,
		home:   homeassistant.New(cfg.HAURL, cfg.HAToken),
		logger: logger,
		providers: map[string]liveapi.Provider{
			"gemini": liveapi.NewGemini(cfg.GeminiAPIKey, cfg.GeminiModel),
			"openai": liveapi.NewOpenAI(cfg.OpenAIAPIKey, cfg.OpenAIModel, cfg.OpenAIVoice, cfg.OpenAITranscriptionModel),
		},
		recentTarget: make(map[string]recentTarget),
	}
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", server.health)
	mux.HandleFunc("GET /live", server.live)
	mux.HandleFunc("GET /satellite", server.satellite)
	return withRecovery(server.logger, mux)
}

func (server *Server) health(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()

	haReady := server.home.Configured() && server.home.Health(ctx)
	providerStatus := make(map[string]any, len(server.providers))
	anyProviderReady := false
	for id, provider := range server.providers {
		configured := provider.Configured()
		anyProviderReady = anyProviderReady || configured
		providerStatus[id] = map[string]any{"configured": configured, "model": provider.Model()}
	}
	status := http.StatusOK
	if !haReady || !anyProviderReady {
		status = http.StatusServiceUnavailable
	}
	writeJSON(response, status, map[string]any{
		"ok": status == http.StatusOK,
		"homeAssistant": map[string]any{
			"url":           server.config.HAURL,
			"reachable":     haReady,
			"authenticated": haReady,
		},
		"provider":  server.config.DefaultProvider,
		"providers": providerStatus,
		"model":     server.providers[server.config.DefaultProvider].Model(),
		"pricing":   nil,
	})
}

func (server *Server) live(response http.ResponseWriter, request *http.Request) {
	if !authorizedSatellite(request, server.config.SatelliteToken) {
		response.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "Неверный токен сателлита."})
		return
	}
	protocol := request.URL.Query().Get("protocol")
	if protocol != "" && protocol != "1" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "Неподдерживаемая версия satellite protocol."})
		return
	}
	transportQuery := request.URL.Query().Get("transport")
	if transportQuery != "" && transportQuery != "binary" && transportQuery != "json-base64" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "Неподдерживаемый audio transport."})
		return
	}
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		server.logger.Printf("satellite websocket: %v", err)
		return
	}
	connection.SetReadLimit(2 << 20)
	writer := &socketWriter{connection: connection}
	defer connection.Close(websocket.StatusNormalClosure, "session ended")

	providerID := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("provider")))
	if providerID == "" {
		providerID = server.config.DefaultProvider
	}
	provider, ok := server.providers[providerID]
	if !ok {
		writer.send(request.Context(), map[string]any{"type": "error", "message": "Неизвестный voice provider: " + providerID})
		return
	}
	if !provider.Configured() {
		writer.send(request.Context(), map[string]any{"type": "error", "message": "Добавьте " + providerKeyName(providerID) + " в .env"})
		return
	}

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	entities, err := server.home.ControllableEntities(ctx)
	if err != nil {
		writer.send(ctx, map[string]any{"type": "error", "message": err.Error()})
		return
	}
	responseMode := request.URL.Query().Get("response_mode")
	if responseMode != "text" {
		responseMode = "audio"
	}
	satelliteID := strings.TrimSpace(request.URL.Query().Get("satellite_id"))
	if satelliteID == "" {
		satelliteID = "anonymous"
	}
	if len(satelliteID) > 64 {
		satelliteID = satelliteID[:64]
	}
	recent := server.getRecentTarget(satelliteID, time.Now())

	session, err := provider.Connect(ctx, liveapi.SessionConfig{
		Instructions: buildInstructions(entities, responseMode, recent),
		ResponseMode: responseMode,
		Tools:        buildTools(entities),
		Vocabulary:   transcriptionVocabulary(entities),
	})
	if err != nil {
		writer.send(ctx, map[string]any{"type": "error", "message": err.Error()})
		return
	}
	conversation := &liveSession{session: session}
	buffer := &responseBuffer{}
	defer conversation.close()
	binaryAudio := transportQuery == "binary"
	transport := "json-base64"
	if binaryAudio {
		transport = "binary"
	}
	writer.send(ctx, map[string]any{
		"type": "ready", "protocolVersion": 1, "transport": transport,
		"provider": provider.ID(), "model": provider.Model(), "responseMode": responseMode,
		"inputAudio":  map[string]any{"encoding": "pcm_s16le", "sampleRate": 16000, "channels": 1},
		"outputAudio": map[string]any{"encoding": "pcm_s16le", "sampleRate": 24000, "channels": 1},
	})

	readErrors := make(chan error, 1)
	go func() {
		readErrors <- server.readSatellite(ctx, connection, conversation, binaryAudio)
		conversation.close()
	}()

	for {
		message, receiveErr := conversation.receive()
		if receiveErr != nil {
			select {
			case satelliteErr := <-readErrors:
				if satelliteErr != nil && !isNormalClose(satelliteErr) {
					server.logger.Printf("satellite receive: %v", satelliteErr)
				}
			default:
				if !isNormalClose(receiveErr) && !errors.Is(receiveErr, context.Canceled) {
					writer.send(ctx, map[string]any{"type": "error", "message": provider.ID() + " Live: " + receiveErr.Error()})
				}
			}
			return
		}
		for _, deliver := range buffer.accept(message) {
			server.relayMessage(ctx, writer, conversation, deliver, responseMode, satelliteID, binaryAudio)
		}
	}
}

// satellite keeps a cheap local-network PCM stream open and creates a paid
// Live session only after the server-side wake-word detector fires.
func (server *Server) satellite(response http.ResponseWriter, request *http.Request) {
	if !authorizedSatellite(request, server.config.SatelliteToken) {
		response.Header().Set("WWW-Authenticate", "Bearer")
		writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "Неверный токен сателлита."})
		return
	}
	if protocol := request.URL.Query().Get("protocol"); protocol != "" && protocol != "1" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "Неподдерживаемая версия satellite protocol."})
		return
	}
	if transport := request.URL.Query().Get("transport"); transport != "binary" {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "Server wake требует transport=binary."})
		return
	}

	providerID := strings.ToLower(strings.TrimSpace(request.URL.Query().Get("provider")))
	if providerID == "" {
		providerID = server.config.DefaultProvider
	}
	provider, ok := server.providers[providerID]
	if !ok {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "Неизвестный voice provider: " + providerID})
		return
	}
	if !provider.Configured() {
		writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "Добавьте " + providerKeyName(providerID) + " в .env"})
		return
	}
	satelliteID := strings.TrimSpace(request.URL.Query().Get("satellite_id"))
	if satelliteID == "" {
		satelliteID = "anonymous"
	}
	if len(satelliteID) > 64 {
		satelliteID = satelliteID[:64]
	}

	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		server.logger.Printf("persistent satellite websocket: %v", err)
		return
	}
	connection.SetReadLimit(2 << 20)
	writer := &socketWriter{connection: connection}
	defer connection.Close(websocket.StatusNormalClosure, "satellite stopped")

	detector, err := wakeword.NewMicro(wakeword.MicroConfig{
		AssetsDir: server.config.WakeAssets,
		Threshold: server.config.WakeThreshold,
	})
	if err != nil {
		writer.send(request.Context(), map[string]any{"type": "error", "message": "Server wake: " + err.Error()})
		return
	}
	defer detector.Close()

	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	audioFrames := make(chan []byte, serverWakeAudioQueue)
	readerErrors := make(chan error, 1)
	go server.readPersistentSatellite(ctx, connection, audioFrames, readerErrors)

	for {
		writer.send(ctx, map[string]any{
			"type": "wake_ready", "protocolVersion": 1, "transport": "binary", "phrase": detector.Phrase(),
			"inputAudio": map[string]any{"encoding": "pcm_s16le", "sampleRate": 16000, "channels": 1},
		})
		_, score, err := waitForServerWake(ctx, detector, audioFrames, readerErrors, func(maxScore float32, peakDBFS float64) {
			writer.send(ctx, map[string]any{
				"type": "wake_status", "maxScore": maxScore, "peakDBFS": peakDBFS,
				"threshold": effectiveWakeThreshold(server.config.WakeThreshold),
			})
		})
		if err != nil {
			if !isNormalClose(err) && !errors.Is(err, context.Canceled) {
				server.logger.Printf("server wake %s: %v", satelliteID, err)
			}
			return
		}
		server.logger.Printf("server wake %s: detected score %.3f", satelliteID, score)
		writer.send(ctx, map[string]any{"type": "wake_detected", "phrase": detector.Phrase(), "score": score})
		// Do not forward the wake-word pre-roll to the provider. The persistent
		// reader keeps buffering every frame after detection while Live connects,
		// so the command is preserved without making "Куза" a separate VAD turn.
		if err := server.runSatelliteLive(ctx, writer, provider, satelliteID, nil, audioFrames, readerErrors); err != nil {
			if isNormalClose(err) || errors.Is(err, context.Canceled) {
				return
			}
			server.logger.Printf("satellite live %s: %v", satelliteID, err)
			writer.send(ctx, map[string]any{"type": "error", "message": err.Error()})
		}
		if !drainAudioFrames(audioFrames) {
			return
		}
		if err := detector.Reset(); err != nil {
			writer.send(ctx, map[string]any{"type": "error", "message": "Server wake reset: " + err.Error()})
			return
		}
	}
}

func (server *Server) readPersistentSatellite(ctx context.Context, connection *websocket.Conn, audioFrames chan<- []byte, readerErrors chan<- error) {
	defer close(audioFrames)
	for {
		messageType, payload, err := connection.Read(ctx)
		if err != nil {
			readerErrors <- err
			return
		}
		if messageType != websocket.MessageBinary {
			continue
		}
		if len(payload) == 0 || len(payload)%2 != 0 {
			continue
		}
		frame := append([]byte(nil), payload...)
		select {
		case audioFrames <- frame:
		case <-ctx.Done():
			return
		}
	}
}

func waitForServerWake(
	ctx context.Context,
	detector wakeword.StreamDetector,
	audioFrames <-chan []byte,
	readerErrors <-chan error,
	report func(maxScore float32, peakDBFS float64),
) ([]byte, float32, error) {
	preRoll := make([]byte, 0, serverWakePreRollBytes)
	metricsStarted := time.Now()
	var maximumScore float32
	peakDBFS := -96.0
	for {
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case err := <-readerErrors:
			return nil, 0, err
		case frame, ok := <-audioFrames:
			if !ok {
				return nil, 0, errors.New("satellite audio stream closed")
			}
			preRoll = appendServerPreRoll(preRoll, frame, serverWakePreRollBytes)
			detected, score, err := detector.ProcessPCM16(frame)
			if err != nil {
				return nil, 0, err
			}
			if detected {
				return preRoll, score, nil
			}
			maximumScore = max(maximumScore, score)
			peakDBFS = max(peakDBFS, pcmPeakDBFS(frame))
			if report != nil && time.Since(metricsStarted) >= time.Second {
				report(maximumScore, peakDBFS)
				metricsStarted = time.Now()
				maximumScore = 0
				peakDBFS = -96
			}
		}
	}
}

func effectiveWakeThreshold(configured float32) float32 {
	if configured > 0 {
		return configured
	}
	return 0.85
}

func pcmPeakDBFS(data []byte) float64 {
	var peak int32
	for offset := 0; offset+1 < len(data); offset += 2 {
		value := int32(int16(binary.LittleEndian.Uint16(data[offset:])))
		if value < 0 {
			value = -value
		}
		peak = max(peak, value)
	}
	if peak == 0 {
		return -96
	}
	return 20 * math.Log10(float64(peak)/32768)
}

func (server *Server) runSatelliteLive(
	ctx context.Context,
	writer *socketWriter,
	provider liveapi.Provider,
	satelliteID string,
	preRoll []byte,
	audioFrames <-chan []byte,
	readerErrors <-chan error,
) error {
	entities, err := server.home.ControllableEntities(ctx)
	if err != nil {
		return err
	}
	recent := server.getRecentTarget(satelliteID, time.Now())
	session, err := provider.Connect(ctx, liveapi.SessionConfig{
		Instructions: buildInstructions(entities, "audio", recent),
		ResponseMode: "audio",
		Tools:        buildTools(entities),
		Vocabulary:   transcriptionVocabulary(entities),
	})
	if err != nil {
		return err
	}
	conversation := &liveSession{session: session}
	defer conversation.close()
	writer.send(ctx, map[string]any{
		"type": "live_ready", "provider": provider.ID(), "model": provider.Model(), "responseMode": "audio",
		"outputAudio": map[string]any{"encoding": "pcm_s16le", "sampleRate": 24000, "channels": 1},
	})
	server.logger.Printf("satellite live %s: connected %s %s", satelliteID, provider.ID(), provider.Model())
	if len(preRoll) > 0 {
		if err := conversation.sendAudio(preRoll); err != nil {
			return err
		}
	}

	sendCtx, stopSending := context.WithCancel(ctx)
	defer stopSending()
	senderErrors := make(chan error, 1)
	go func() {
		for {
			select {
			case <-sendCtx.Done():
				senderErrors <- nil
				return
			case frame, ok := <-audioFrames:
				if !ok {
					senderErrors <- errors.New("satellite audio stream closed")
					return
				}
				if err := conversation.sendAudio(frame); err != nil {
					senderErrors <- err
					return
				}
			}
		}
	}()

	providerEvents := make(chan liveapi.Event, 1)
	providerErrors := make(chan error, 1)
	go func() {
		for {
			event, err := conversation.receive()
			if err != nil {
				providerErrors <- err
				return
			}
			select {
			case providerEvents <- event:
			case <-ctx.Done():
				return
			}
		}
	}()

	buffer := &responseBuffer{}
	toolExecuted := false
	var assistantText strings.Builder
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-readerErrors:
			return err
		case err := <-senderErrors:
			if err != nil {
				return err
			}
		case err := <-providerErrors:
			return err
		case event := <-providerEvents:
			for _, deliver := range buffer.accept(event) {
				switch deliver.Kind {
				case liveapi.EventToolCall:
					toolExecuted = true
					for _, call := range deliver.ToolCalls {
						server.logger.Printf("satellite live %s: tool %s %v", satelliteID, call.Name, call.Args)
					}
				case liveapi.EventInputTranscript:
					server.logger.Printf("satellite live %s: user %q", satelliteID, strings.TrimSpace(deliver.Text))
				case liveapi.EventOutputTranscript, liveapi.EventTextDelta:
					assistantText.WriteString(deliver.Text)
				}
				server.relayMessage(ctx, writer, conversation, deliver, "audio", satelliteID, true)
				if deliver.Kind == liveapi.EventTurnComplete {
					server.logger.Printf("satellite live %s: turn complete tool=%t assistant=%q", satelliteID, toolExecuted, strings.TrimSpace(assistantText.String()))
					if toolExecuted || !asksForClarification(assistantText.String()) {
						return nil
					}
					toolExecuted = false
					assistantText.Reset()
				}
			}
		}
	}
}

func appendServerPreRoll(buffer, frame []byte, limit int) []byte {
	if len(frame) >= limit {
		return append(buffer[:0], frame[len(frame)-limit:]...)
	}
	overflow := len(buffer) + len(frame) - limit
	if overflow > 0 {
		copy(buffer, buffer[overflow:])
		buffer = buffer[:len(buffer)-overflow]
	}
	return append(buffer, frame...)
}

func drainAudioFrames(audioFrames <-chan []byte) bool {
	for {
		select {
		case _, ok := <-audioFrames:
			if !ok {
				return false
			}
		default:
			return true
		}
	}
}

func asksForClarification(response string) bool {
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

func (server *Server) readSatellite(ctx context.Context, connection *websocket.Conn, conversation *liveSession, binaryAudio bool) error {
	for {
		messageType, payload, err := connection.Read(ctx)
		if err != nil {
			return err
		}
		if messageType == websocket.MessageBinary {
			if !binaryAudio {
				return errors.New("binary audio requires transport=binary")
			}
			if len(payload) > 0 {
				if err := conversation.sendAudio(payload); err != nil {
					return err
				}
			}
			continue
		}
		if messageType != websocket.MessageText {
			return fmt.Errorf("unsupported websocket message type: %d", messageType)
		}
		var event clientEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return fmt.Errorf("decode satellite event: %w", err)
		}
		switch event.Type {
		case "audio":
			data, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				return fmt.Errorf("decode satellite audio: %w", err)
			}
			if err := conversation.sendAudio(data); err != nil {
				return err
			}
		case "audio_stream_end":
			if err := conversation.endAudio(); err != nil {
				return err
			}
		case "text":
			if strings.TrimSpace(event.Text) != "" {
				if err := conversation.sendText(event.Text); err != nil {
					return err
				}
			}
		}
	}
}

func (server *Server) relayMessage(ctx context.Context, writer *socketWriter, conversation *liveSession, event liveapi.Event, responseMode, satelliteID string, binaryAudio bool) {
	switch event.Kind {
	case liveapi.EventInputTranscript:
		writer.send(ctx, map[string]any{"type": string(event.Kind), "text": event.Text})
	case liveapi.EventOutputTranscript, liveapi.EventTextDelta:
		writer.send(ctx, map[string]any{"type": string(event.Kind), "text": event.Text})
	case liveapi.EventAudioDelta:
		if responseMode == "audio" && len(event.Audio) > 0 {
			if binaryAudio {
				writer.sendBinary(ctx, event.Audio)
			} else {
				writer.send(ctx, map[string]any{
					"type": string(event.Kind), "data": base64.StdEncoding.EncodeToString(event.Audio),
					"mimeType": valueOr(event.MIMEType, "audio/pcm;rate=24000"),
				})
			}
		}
	case liveapi.EventSpeechStarted, liveapi.EventSpeechStopped:
		writer.send(ctx, map[string]any{"type": string(event.Kind), "audioMs": event.AudioMS})
	case liveapi.EventToolCall:
		server.executeTools(ctx, writer, conversation, event.ToolCalls, satelliteID)
	case liveapi.EventUsage:
		writer.send(ctx, map[string]any{"type": string(event.Kind), "usage": event.Usage})
	case liveapi.EventTurnComplete:
		writer.send(ctx, map[string]any{"type": string(event.Kind), "reason": event.Reason})
	case liveapi.EventInterrupted, liveapi.EventWaitingForInput:
		writer.send(ctx, map[string]any{"type": string(event.Kind)})
	case liveapi.EventError:
		writer.send(ctx, map[string]any{"type": "error", "message": event.Text})
	}
}

type responseBuffer struct {
	pending []liveapi.Event
}

func (buffer *responseBuffer) accept(event liveapi.Event) []liveapi.Event {
	switch event.Kind {
	case liveapi.EventOutputTranscript, liveapi.EventTextDelta, liveapi.EventAudioDelta:
		buffer.pending = append(buffer.pending, event)
		return nil
	case liveapi.EventToolCall:
		buffer.pending = nil
		return []liveapi.Event{event}
	case liveapi.EventTurnComplete:
		deliver := append([]liveapi.Event(nil), buffer.pending...)
		buffer.pending = nil
		return append(deliver, event)
	case liveapi.EventInterrupted, liveapi.EventError:
		buffer.pending = nil
		return []liveapi.Event{event}
	default:
		return []liveapi.Event{event}
	}
}

func (server *Server) executeTools(ctx context.Context, writer *socketWriter, conversation *liveSession, calls []liveapi.ToolCall, satelliteID string) {
	responses := make([]liveapi.ToolResult, 0, len(calls))
	for _, call := range calls {
		if call.Name != "control_home_entity" && call.Name != "get_home_state" {
			continue
		}
		writer.send(ctx, map[string]any{"type": "tool_call", "name": call.Name, "args": call.Args})
		entityID, _ := call.Args["entity_id"].(string)
		action, _ := call.Args["action"].(string)
		if call.Name == "get_home_state" {
			action = "get_state"
		}
		temperature, _ := call.Args["temperature"].(float64)
		result, err := server.home.Perform(ctx, entityID, action, temperature)
		var payload map[string]any
		if err != nil {
			payload = map[string]any{"ok": false, "error": err.Error()}
		} else {
			encoded, _ := json.Marshal(result)
			_ = json.Unmarshal(encoded, &payload)
		}
		writer.send(ctx, map[string]any{"type": "ha_result", "result": payload})
		if err == nil && call.Name == "control_home_entity" {
			name, _ := payload["name"].(string)
			server.rememberTarget(satelliteID, entityID, name, time.Now())
		}
		response := toolResultForModel(call, payload, err)
		responses = append(responses, liveapi.ToolResult{ID: call.ID, Name: call.Name, Output: response})
	}
	if len(responses) > 0 {
		if err := conversation.sendToolResults(responses); err != nil {
			writer.send(ctx, map[string]any{"type": "error", "message": err.Error()})
		}
	}
}

func (server *Server) rememberTarget(satelliteID, entityID, name string, now time.Time) {
	if satelliteID == "" || entityID == "" {
		return
	}
	server.recentMu.Lock()
	defer server.recentMu.Unlock()
	server.recentTarget[satelliteID] = recentTarget{
		EntityID: entityID, Name: name, ExpiresAt: now.Add(recentTargetTTL),
	}
}

func (server *Server) getRecentTarget(satelliteID string, now time.Time) *recentTarget {
	server.recentMu.Lock()
	defer server.recentMu.Unlock()
	target, exists := server.recentTarget[satelliteID]
	if !exists {
		return nil
	}
	if !now.Before(target.ExpiresAt) {
		delete(server.recentTarget, satelliteID)
		return nil
	}
	return &target
}

func toolResultForModel(call liveapi.ToolCall, payload map[string]any, err error) map[string]any {
	if err != nil {
		return map[string]any{"ok": false, "error": err.Error()}
	}
	result := map[string]any{"ok": true}
	for _, key := range []string{
		"entityId", "name", "action", "state", "temperature", "temperatureUnit", "humidity",
		"cloudCoverage", "windSpeed", "windSpeedUnit", "confirmed",
	} {
		if value, exists := payload[key]; exists {
			result[key] = value
		}
	}
	if call.Name == "control_home_entity" {
		action, _ := call.Args["action"].(string)
		result["requestedAction"] = action
	}
	return result
}

func transcriptionVocabulary(entities []homeassistant.Entity) []string {
	vocabulary := []string{
		"включи", "выключи", "отключи", "свет", "лампа", "кондиционер",
		"температура", "градусы", "кухня", "спальня", "гостиная",
	}
	seen := make(map[string]bool, len(vocabulary)+len(entities)*2)
	result := make([]string, 0, len(vocabulary)+len(entities)*2)
	for _, phrase := range vocabulary {
		seen[strings.ToLower(phrase)] = true
		result = append(result, phrase)
	}
	for _, entity := range entities {
		for _, phrase := range []string{entity.Name, entity.Area} {
			phrase = strings.TrimSpace(phrase)
			key := strings.ToLower(phrase)
			if phrase != "" && !seen[key] {
				seen[key] = true
				result = append(result, phrase)
			}
		}
	}
	return result
}

func buildInstructions(entities []homeassistant.Entity, responseMode string, recent *recentTarget) string {
	items := make([]string, 0, len(entities))
	for _, entity := range entities {
		area := entity.Area
		if area == "" {
			area = "не назначена"
		}
		items = append(items, fmt.Sprintf("%s (%s, комната: %s)", entity.EntityID, entity.Name, area))
	}
	verb := "скажи"
	if responseMode == "text" {
		verb = "напиши"
	}
	instructions := []string{
		"Ты русскоязычный голосовой ассистент и управляешь умным домом.",
		"«Куза» — локальное слово активации: игнорируй его и обрабатывай только команду после него.",
		"Пользователь всегда говорит по-русски; распознавай вход только как русскую речь и всегда отвечай по-русски.",
		"Никогда не превращай нерусскую или сомнительную расшифровку в команду умного дома: попроси повторить.",
		"Строго различай противоположные команды: включи означает только turn_on, выключи или отключи означает только turn_off.",
		"Любой ответ содержит не больше пяти слов.",
		"Говори в жёстком пародийном gachi-стиле: максимально брутально, напористо и мемно; к пользователю обращайся «мастер».",
		"В каждом ответе используй одну узнаваемую gachi-реплику или её уместную русскую адаптацию: «Dungeon Master», «Boss of this gym», «Deep dark fantasy», «Fisting is three hundred bucks», «Oh shit, I'm sorry», «Sorry for what?», «Fucking slave».",
		"Никаких приветствий, объяснений, планов, советов и лишних вопросов.",
		"Никогда не сообщай о намерении перед вызовом функции: сразу вызывай функцию без текста.",
		"После результата функции с ok=true " + verb + " краткий gachi-ответ по точным полям action, state и temperature; не выдумывай другие числа или действия.",
		"Технические английские значения state переводи на естественный русский, сохраняя точные числа и единицы измерения.",
		"Обычные вопросы, не относящиеся к управлению домом, не являются ошибкой: ответь на них напрямую без вызова функций.",
		"Если для ответа нужны актуальные внешние данные, которых у тебя нет, кратко и честно скажи об этом; не проси повторять уже понятный вопрос.",
		"На вопрос о текущей погоде обязательно вызови get_home_state для weather-сущности; если weather-сущность одна, используй её и для вопроса с названием города.",
		"Если команда неоднозначна, задай ровно один короткий вопрос с вариантами и жди ответа.",
		"Для управления используй только control_home_entity и get_home_state.",
		"Не утверждай, что действие выполнено, пока функция не вернула ok=true.",
		"Для turn_on, turn_off и toggle сразу вызови control_home_entity один раз.",
		"Не вызывай get_home_state до или после управления.",
		"На явный вопрос о состоянии обязательно вызови get_home_state.",
		"Выбирай свет по комнате Home Assistant: кухня — Kitchen, спальня — Bedroom, гостиная — Living Room.",
		"Для света в комнате выбирай сущность light этой комнаты.",
		"Если подходящей сущности нет, ничего другого не включай.",
		"Доступные сущности: " + strings.Join(items, "; ") + ".",
	}
	if recent != nil {
		instructions = append(instructions,
			fmt.Sprintf("Краткосрочный контекст: последняя успешно управляемая сущность — %s (%s).", recent.EntityID, recent.Name),
			"Если новая команда опускает устройство и явно продолжает предыдущее действие словами вроде «а теперь», используй эту сущность; явно названное новое устройство всегда важнее.",
		)
	}
	return strings.Join(instructions, " ")
}

func buildTools(entities []homeassistant.Entity) []liveapi.Tool {
	controllableIDs := make([]string, 0, len(entities))
	readableIDs := make([]string, 0, len(entities))
	for _, entity := range entities {
		readableIDs = append(readableIDs, entity.EntityID)
		domain, _, _ := strings.Cut(entity.EntityID, ".")
		if domain == "light" || domain == "switch" || domain == "climate" {
			controllableIDs = append(controllableIDs, entity.EntityID)
		}
	}
	controllableSchema := map[string]any{"type": "string", "enum": controllableIDs}
	readableSchema := map[string]any{"type": "string", "enum": readableIDs}
	return []liveapi.Tool{
		{
			Name:        "control_home_entity",
			Description: "Управляет одной разрешённой сущностью Home Assistant. При команде управления вызови функцию первым элементом ответа без текста или аудио перед вызовом.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id":   controllableSchema,
					"action":      map[string]any{"type": "string", "enum": []string{"turn_on", "turn_off", "toggle", "set_temperature"}},
					"temperature": map[string]any{"type": "number", "minimum": 10, "maximum": 30},
				},
				"required":             []string{"entity_id", "action"},
				"additionalProperties": false,
			},
		},
		{
			Name:        "get_home_state",
			Description: "Получает состояние сущности по явному вопросу пользователя.",
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"entity_id": readableSchema},
				"required":             []string{"entity_id"},
				"additionalProperties": false,
			},
		},
	}
}

func (writer *socketWriter) send(ctx context.Context, value any) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = wsjson.Write(writeCtx, writer.connection, value)
}

func (writer *socketWriter) sendBinary(ctx context.Context, data []byte) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = writer.connection.Write(writeCtx, websocket.MessageBinary, data)
}

func authorizedSatellite(request *http.Request, expectedToken string) bool {
	if expectedToken == "" {
		return true
	}
	provided := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expectedToken)) == 1
}

func (conversation *liveSession) receive() (liveapi.Event, error) {
	return conversation.session.Receive()
}

func (conversation *liveSession) sendAudio(data []byte) error {
	conversation.sendMu.Lock()
	defer conversation.sendMu.Unlock()
	return conversation.session.SendAudio(data)
}

func (conversation *liveSession) endAudio() error {
	conversation.sendMu.Lock()
	defer conversation.sendMu.Unlock()
	return conversation.session.EndAudio()
}

func (conversation *liveSession) sendText(text string) error {
	conversation.sendMu.Lock()
	defer conversation.sendMu.Unlock()
	return conversation.session.SendText(text)
}

func (conversation *liveSession) sendToolResults(results []liveapi.ToolResult) error {
	conversation.sendMu.Lock()
	defer conversation.sendMu.Unlock()
	return conversation.session.SendToolResults(results)
}

func (conversation *liveSession) close() {
	conversation.closeOnce.Do(func() {
		conversation.sendMu.Lock()
		defer conversation.sendMu.Unlock()
		_ = conversation.session.Close()
	})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func withRecovery(logger *log.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Printf("panic: %v", recovered)
				writeJSON(response, http.StatusInternalServerError, map[string]string{"error": "Внутренняя ошибка."})
			}
		}()
		next.ServeHTTP(response, request)
	})
}

func isNormalClose(err error) bool {
	status := websocket.CloseStatus(err)
	return status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway || status == websocket.StatusNoStatusRcvd
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func providerKeyName(providerID string) string {
	if providerID == "openai" {
		return "OPENAI_API_KEY"
	}
	return "GEMINI_API_KEY"
}
