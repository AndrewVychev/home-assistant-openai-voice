package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"google.golang.org/genai"
	"homevoice/internal/config"
	"homevoice/internal/homeassistant"
)

type Server struct {
	config config.Config
	home   *homeassistant.Client
	logger *log.Logger
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
	session *genai.Session
	mu      sync.Mutex
}

func New(cfg config.Config, logger *log.Logger) *Server {
	return &Server{
		config: cfg,
		home:   homeassistant.New(cfg.HAURL, cfg.HAToken),
		logger: logger,
	}
}

func (server *Server) Handler(projectRoot string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", server.health)
	mux.HandleFunc("POST /api/ha/entity", server.homeAction)
	mux.HandleFunc("GET /gemini-live", server.geminiLive)
	mux.Handle("/vendor/tfjs/", http.StripPrefix("/vendor/tfjs/", http.FileServer(http.Dir(filepath.Join(projectRoot, "node_modules/@tensorflow/tfjs/dist")))))
	mux.Handle("/vendor/speech-commands/", http.StripPrefix("/vendor/speech-commands/", http.FileServer(http.Dir(filepath.Join(projectRoot, "node_modules/@tensorflow-models/speech-commands/dist")))))
	mux.Handle("/", http.FileServer(http.Dir(filepath.Join(projectRoot, "public"))))
	return withRecovery(server.logger, mux)
}

func (server *Server) health(response http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), 5*time.Second)
	defer cancel()

	haReady := server.home.Configured() && server.home.Health(ctx)
	googleReady := strings.TrimSpace(server.config.GeminiAPIKey) != ""
	status := http.StatusOK
	if !haReady || !googleReady {
		status = http.StatusServiceUnavailable
	}
	writeJSON(response, status, map[string]any{
		"ok": status == http.StatusOK,
		"homeAssistant": map[string]any{
			"url":           server.config.HAURL,
			"reachable":     haReady,
			"authenticated": haReady,
		},
		"google":  map[string]bool{"configured": googleReady},
		"model":   server.config.GeminiModel,
		"pricing": nil,
	})
}

func (server *Server) homeAction(response http.ResponseWriter, request *http.Request) {
	var body struct {
		EntityID    string  `json:"entity_id"`
		Action      string  `json:"action"`
		Temperature float64 `json:"temperature"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 16<<10))
	if err := decoder.Decode(&body); err != nil {
		writeJSON(response, http.StatusBadRequest, map[string]string{"error": "Некорректный JSON."})
		return
	}
	if body.Action == "" {
		body.Action = "get_state"
	}
	result, err := server.home.Perform(request.Context(), body.EntityID, body.Action, body.Temperature)
	if err != nil {
		writeJSON(response, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) geminiLive(response http.ResponseWriter, request *http.Request) {
	connection, err := websocket.Accept(response, request, &websocket.AcceptOptions{
		OriginPatterns: []string{"localhost:*", "127.0.0.1:*"},
	})
	if err != nil {
		server.logger.Printf("browser websocket: %v", err)
		return
	}
	connection.SetReadLimit(2 << 20)
	writer := &socketWriter{connection: connection}
	defer connection.Close(websocket.StatusNormalClosure, "session ended")

	if strings.TrimSpace(server.config.GeminiAPIKey) == "" {
		writer.send(request.Context(), map[string]any{"type": "error", "message": "Добавьте GEMINI_API_KEY в .env"})
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

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  server.config.GeminiAPIKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		writer.send(ctx, map[string]any{"type": "error", "message": err.Error()})
		return
	}
	session, err := client.Live.Connect(ctx, server.config.GeminiModel, liveConfig(entities, responseMode))
	if err != nil {
		writer.send(ctx, map[string]any{"type": "error", "message": err.Error()})
		return
	}
	live := &liveSession{session: session}
	defer live.close()
	writer.send(ctx, map[string]any{"type": "ready", "model": server.config.GeminiModel, "responseMode": responseMode})

	readErrors := make(chan error, 1)
	go func() {
		readErrors <- server.readBrowser(ctx, connection, live)
		live.close()
	}()

	for {
		message, receiveErr := live.receive()
		if receiveErr != nil {
			select {
			case browserErr := <-readErrors:
				if browserErr != nil && !isNormalClose(browserErr) {
					server.logger.Printf("browser receive: %v", browserErr)
				}
			default:
				if !isNormalClose(receiveErr) && !errors.Is(receiveErr, context.Canceled) {
					writer.send(ctx, map[string]any{"type": "error", "message": "Gemini Live: " + receiveErr.Error()})
				}
			}
			return
		}
		server.relayMessage(ctx, writer, live, message, responseMode)
	}
}

func (server *Server) readBrowser(ctx context.Context, connection *websocket.Conn, live *liveSession) error {
	for {
		var event clientEvent
		if err := wsjson.Read(ctx, connection, &event); err != nil {
			return err
		}
		switch event.Type {
		case "audio":
			data, err := base64.StdEncoding.DecodeString(event.Data)
			if err != nil {
				return fmt.Errorf("decode browser audio: %w", err)
			}
			if err := live.sendRealtime(genai.LiveRealtimeInput{Audio: &genai.Blob{Data: data, MIMEType: "audio/pcm;rate=16000"}}); err != nil {
				return err
			}
		case "audio_stream_end":
			if err := live.sendRealtime(genai.LiveRealtimeInput{AudioStreamEnd: true}); err != nil {
				return err
			}
		case "text":
			if strings.TrimSpace(event.Text) != "" {
				complete := true
				if err := live.sendContent(genai.LiveClientContentInput{
					Turns:        []*genai.Content{genai.NewContentFromText(event.Text, genai.RoleUser)},
					TurnComplete: &complete,
				}); err != nil {
					return err
				}
			}
		}
	}
}

func (server *Server) relayMessage(ctx context.Context, writer *socketWriter, live *liveSession, message *genai.LiveServerMessage, responseMode string) {
	content := message.ServerContent
	if content != nil {
		if content.InputTranscription != nil && content.InputTranscription.Text != "" {
			writer.send(ctx, map[string]any{"type": "input_transcript", "text": content.InputTranscription.Text})
		}
		if content.OutputTranscription != nil && content.OutputTranscription.Text != "" {
			writer.send(ctx, map[string]any{"type": "output_transcript", "text": content.OutputTranscription.Text})
		}
		if content.ModelTurn != nil {
			for _, part := range content.ModelTurn.Parts {
				if part.Text != "" {
					writer.send(ctx, map[string]any{"type": "text_delta", "text": part.Text})
				}
				if responseMode == "audio" && part.InlineData != nil && len(part.InlineData.Data) > 0 {
					writer.send(ctx, map[string]any{
						"type":     "audio_delta",
						"data":     base64.StdEncoding.EncodeToString(part.InlineData.Data),
						"mimeType": valueOr(part.InlineData.MIMEType, "audio/pcm;rate=24000"),
					})
				}
			}
		}
		if content.Interrupted {
			writer.send(ctx, map[string]string{"type": "interrupted"})
		}
		if content.WaitingForInput {
			writer.send(ctx, map[string]string{"type": "waiting_for_input"})
		}
		if content.TurnComplete {
			writer.send(ctx, map[string]any{"type": "turn_complete", "reason": content.TurnCompleteReason})
		}
	}
	if message.UsageMetadata != nil {
		writer.send(ctx, map[string]any{"type": "usage", "usage": message.UsageMetadata})
	}
	if message.ToolCall != nil && len(message.ToolCall.FunctionCalls) > 0 {
		server.executeTools(ctx, writer, live, message.ToolCall.FunctionCalls)
	}
}

func (server *Server) executeTools(ctx context.Context, writer *socketWriter, live *liveSession, calls []*genai.FunctionCall) {
	responses := make([]*genai.FunctionResponse, 0, len(calls))
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
		response := map[string]any{"output": payload}
		if err != nil {
			response = map[string]any{"error": err.Error()}
		}
		responses = append(responses, &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: response})
	}
	if len(responses) > 0 {
		if err := live.sendTools(genai.LiveToolResponseInput{FunctionResponses: responses}); err != nil {
			writer.send(ctx, map[string]any{"type": "error", "message": err.Error()})
		}
	}
}

func liveConfig(entities []homeassistant.Entity, responseMode string) *genai.LiveConnectConfig {
	return &genai.LiveConnectConfig{
		ResponseModalities:       []genai.Modality{genai.ModalityAudio},
		MaxOutputTokens:          64,
		ThinkingConfig:           &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelMinimal},
		SystemInstruction:        genai.NewContentFromText(buildInstructions(entities, responseMode), genai.RoleUser),
		Tools:                    buildTools(entities),
		InputAudioTranscription:  &genai.AudioTranscriptionConfig{},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{},
	}
}

func buildInstructions(entities []homeassistant.Entity, responseMode string) string {
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
	return strings.Join([]string{
		"Ты голосовой ассистент умного дома.",
		"Всегда отвечай по-русски.",
		"Любой ответ содержит не больше пяти слов.",
		"Никаких приветствий, объяснений, планов, советов и лишних вопросов.",
		"После успешного действия " + verb + " только краткий итог.",
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
	}, " ")
}

func buildTools(entities []homeassistant.Entity) []*genai.Tool {
	entityIDs := make([]string, 0, len(entities))
	for _, entity := range entities {
		entityIDs = append(entityIDs, entity.EntityID)
	}
	entitySchema := map[string]any{"type": "string", "enum": entityIDs}
	return []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{
		{
			Name:        "control_home_entity",
			Description: "Управляет одной разрешённой сущностью Home Assistant.",
			ParametersJsonSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"entity_id":   entitySchema,
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
			ParametersJsonSchema: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"entity_id": entitySchema},
				"required":             []string{"entity_id"},
				"additionalProperties": false,
			},
		},
	}}}
}

func (writer *socketWriter) send(ctx context.Context, value any) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = wsjson.Write(writeCtx, writer.connection, value)
}

func (live *liveSession) receive() (*genai.LiveServerMessage, error) {
	return live.session.Receive()
}

func (live *liveSession) sendRealtime(input genai.LiveRealtimeInput) error {
	live.mu.Lock()
	defer live.mu.Unlock()
	return live.session.SendRealtimeInput(input)
}

func (live *liveSession) sendContent(input genai.LiveClientContentInput) error {
	live.mu.Lock()
	defer live.mu.Unlock()
	return live.session.SendClientContent(input)
}

func (live *liveSession) sendTools(input genai.LiveToolResponseInput) error {
	live.mu.Lock()
	defer live.mu.Unlock()
	return live.session.SendToolResponse(input)
}

func (live *liveSession) close() {
	live.mu.Lock()
	defer live.mu.Unlock()
	_ = live.session.Close()
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
