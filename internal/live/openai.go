package live

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type OpenAIProvider struct {
	apiKey string
	model  string
	voice  string
	url    string
}

func NewOpenAI(apiKey, model, voice string) *OpenAIProvider {
	return &OpenAIProvider{
		apiKey: strings.TrimSpace(apiKey),
		model:  strings.TrimSpace(model),
		voice:  strings.TrimSpace(voice),
		url:    "wss://api.openai.com/v1/realtime",
	}
}

func (provider *OpenAIProvider) ID() string       { return "openai" }
func (provider *OpenAIProvider) Model() string    { return provider.model }
func (provider *OpenAIProvider) Configured() bool { return provider.apiKey != "" }

func (provider *OpenAIProvider) Connect(ctx context.Context, config SessionConfig) (Session, error) {
	endpoint, err := url.Parse(provider.url)
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("model", provider.model)
	endpoint.RawQuery = query.Encode()
	hash := sha256.Sum256([]byte("local-home-assistant-voice"))
	connection, _, err := websocket.Dial(ctx, endpoint.String(), &websocket.DialOptions{HTTPHeader: http.Header{
		"Authorization":            []string{"Bearer " + provider.apiKey},
		"OpenAI-Safety-Identifier": []string{hex.EncodeToString(hash[:])},
	}})
	if err != nil {
		return nil, err
	}
	session := &openAISession{ctx: ctx, connection: connection}
	fail := func(cause error) (Session, error) {
		_ = session.Close()
		return nil, cause
	}
	created, err := session.readRaw()
	if err != nil {
		return fail(err)
	}
	if created.Type == "error" {
		return fail(created.asError())
	}
	if created.Type != "session.created" {
		return fail(fmt.Errorf("OpenAI Realtime: ожидался session.created, получено %s", created.Type))
	}
	if err := session.send(openAISessionUpdate(provider.model, provider.voice, config)); err != nil {
		return fail(err)
	}
	for {
		updated, readErr := session.readRaw()
		if readErr != nil {
			return fail(readErr)
		}
		if updated.Type == "error" {
			return fail(updated.asError())
		}
		if updated.Type == "session.updated" {
			break
		}
	}
	return session, nil
}

func openAISessionUpdate(model, voice string, config SessionConfig) map[string]any {
	mode := "audio"
	if config.ResponseMode == "text" {
		mode = "text"
	}
	input := map[string]any{
		"format": map[string]any{"type": "audio/pcm", "rate": 24000},
		"turn_detection": map[string]any{
			"type": "server_vad", "threshold": 0.5, "prefix_padding_ms": 300,
			"silence_duration_ms": 500, "create_response": true, "interrupt_response": true,
		},
		"transcription": map[string]any{
			"model": "gpt-4o-mini-transcribe", "language": "ru",
			"prompt": strings.Join(config.Vocabulary, ", "),
		},
	}
	audio := map[string]any{"input": input}
	if mode == "audio" {
		audio["output"] = map[string]any{
			"format": map[string]any{"type": "audio/pcm", "rate": 24000},
			"voice":  voice,
		}
	}
	tools := make([]map[string]any, 0, len(config.Tools))
	for _, tool := range config.Tools {
		tools = append(tools, map[string]any{
			"type": "function", "name": tool.Name,
			"description": tool.Description, "parameters": tool.Parameters,
		})
	}
	return map[string]any{
		"type": "session.update",
		"session": map[string]any{
			"type": "realtime", "model": model, "instructions": config.Instructions,
			"output_modalities": []string{mode}, "audio": audio,
			"max_output_tokens": 64, "tools": tools, "tool_choice": "auto",
		},
	}
}

type openAISession struct {
	ctx        context.Context
	connection *websocket.Conn
	writeMu    sync.Mutex
	queue      []Event
	resampler  pcmResampler
	seenCalls  map[string]bool
}

type openAIRawEvent struct {
	Type       string `json:"type"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Name       string `json:"name"`
	CallID     string `json:"call_id"`
	Arguments  string `json:"arguments"`
	Item       *struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		CallID    string `json:"call_id"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
	Response *struct {
		Usage  map[string]any `json:"usage"`
		Output []struct {
			Type string `json:"type"`
		} `json:"output"`
	} `json:"response"`
}

func (event openAIRawEvent) asError() error {
	if event.Error != nil && event.Error.Message != "" {
		return errors.New(event.Error.Message)
	}
	return fmt.Errorf("OpenAI Realtime error: %s", event.Type)
}

func (session *openAISession) readRaw() (openAIRawEvent, error) {
	var event openAIRawEvent
	err := wsjson.Read(session.ctx, session.connection, &event)
	return event, err
}

func (session *openAISession) Receive() (Event, error) {
	for {
		if len(session.queue) > 0 {
			event := session.queue[0]
			session.queue = session.queue[1:]
			return event, nil
		}
		raw, err := session.readRaw()
		if err != nil {
			return Event{}, err
		}
		if os.Getenv("HOMEVOICE_DEBUG_LIVE") == "1" {
			if strings.Contains(raw.Type, "function_call") || raw.Type == "response.output_item.done" {
				log.Printf("OpenAI Realtime event: %s call=%q name=%q arguments=%q item=%+v", raw.Type, raw.CallID, raw.Name, raw.Arguments, raw.Item)
			} else {
				log.Printf("OpenAI Realtime event: %s", raw.Type)
			}
		}
		switch raw.Type {
		case "conversation.item.input_audio_transcription.completed":
			return Event{Kind: EventInputTranscript, Text: raw.Transcript}, nil
		case "response.output_audio_transcript.done":
			return Event{Kind: EventOutputTranscript, Text: raw.Transcript}, nil
		case "response.output_text.delta":
			return Event{Kind: EventTextDelta, Text: raw.Delta}, nil
		case "response.output_audio.delta":
			data, decodeErr := base64.StdEncoding.DecodeString(raw.Delta)
			if decodeErr != nil {
				return Event{}, decodeErr
			}
			return Event{Kind: EventAudioDelta, Audio: data, MIMEType: "audio/pcm;rate=24000"}, nil
		case "response.function_call_arguments.done":
			event, ready, callErr := session.toolCallEvent(raw.CallID, raw.Name, raw.Arguments)
			if callErr != nil {
				return Event{}, callErr
			}
			if ready {
				return event, nil
			}
		case "response.output_item.done":
			if raw.Item != nil && raw.Item.Type == "function_call" {
				event, ready, callErr := session.toolCallEvent(raw.Item.CallID, raw.Item.Name, raw.Item.Arguments)
				if callErr != nil {
					return Event{}, callErr
				}
				if ready {
					return event, nil
				}
			}
		case "response.done":
			hasFunctionCall := false
			if raw.Response != nil {
				for _, item := range raw.Response.Output {
					if item.Type == "function_call" {
						hasFunctionCall = true
						break
					}
				}
				if raw.Response.Usage != nil {
					session.queue = append(session.queue, Event{Kind: EventUsage, Usage: raw.Response.Usage})
				}
			}
			if !hasFunctionCall {
				session.queue = append(session.queue, Event{Kind: EventTurnComplete})
			}
		case "error":
			return Event{Kind: EventError, Text: raw.asError().Error()}, nil
		}
	}
}

func (session *openAISession) toolCallEvent(callID, name, arguments string) (Event, bool, error) {
	if callID == "" || name == "" || strings.TrimSpace(arguments) == "" {
		return Event{}, false, nil
	}
	if session.seenCalls == nil {
		session.seenCalls = make(map[string]bool)
	}
	if session.seenCalls[callID] {
		return Event{}, false, nil
	}
	args := make(map[string]any)
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return Event{}, false, fmt.Errorf("OpenAI tool arguments: %w", err)
	}
	session.seenCalls[callID] = true
	return Event{Kind: EventToolCall, ToolCalls: []ToolCall{{ID: callID, Name: name, Args: args}}}, true, nil
}

func (session *openAISession) send(value any) error {
	session.writeMu.Lock()
	defer session.writeMu.Unlock()
	return wsjson.Write(session.ctx, session.connection, value)
}

func (session *openAISession) SendAudio(pcm16k []byte) error {
	pcm24k, err := session.resampler.Process(pcm16k)
	if err != nil {
		return err
	}
	if len(pcm24k) == 0 {
		return nil
	}
	return session.send(map[string]any{
		"type": "input_audio_buffer.append", "audio": base64.StdEncoding.EncodeToString(pcm24k),
	})
}

func (session *openAISession) EndAudio() error {
	return session.send(map[string]any{"type": "input_audio_buffer.commit"})
}

func (session *openAISession) SendText(text string) error {
	if err := session.send(map[string]any{
		"type": "conversation.item.create",
		"item": map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]any{{"type": "input_text", "text": text}},
		},
	}); err != nil {
		return err
	}
	return session.send(map[string]any{"type": "response.create"})
}

func (session *openAISession) SendToolResults(results []ToolResult) error {
	for _, result := range results {
		output, err := json.Marshal(result.Output)
		if err != nil {
			return err
		}
		if err := session.send(map[string]any{
			"type": "conversation.item.create",
			"item": map[string]any{
				"type": "function_call_output", "call_id": result.ID, "output": string(output),
			},
		}); err != nil {
			return err
		}
	}
	return session.send(map[string]any{"type": "response.create"})
}

func (session *openAISession) Close() error {
	return session.connection.Close(websocket.StatusNormalClosure, "session ended")
}

type pcmResampler struct {
	previous int16
	havePrev bool
	phase    float64
}

func (resampler *pcmResampler) Process(data []byte) ([]byte, error) {
	if len(data)%2 != 0 {
		return nil, errors.New("PCM16 chunk has an odd byte count")
	}
	if len(data) == 0 {
		return nil, nil
	}
	samples := make([]int16, len(data)/2)
	for index := range samples {
		samples[index] = int16(binary.LittleEndian.Uint16(data[index*2:]))
	}
	if !resampler.havePrev {
		resampler.previous = samples[0]
		resampler.havePrev = true
		samples = samples[1:]
	}
	combined := make([]int16, 1+len(samples))
	combined[0] = resampler.previous
	copy(combined[1:], samples)
	if len(combined) < 2 {
		return nil, nil
	}
	output := make([]int16, 0, int(math.Ceil(float64(len(combined))*1.5)))
	for resampler.phase < float64(len(combined)-1) {
		left := int(resampler.phase)
		fraction := resampler.phase - float64(left)
		value := float64(combined[left])*(1-fraction) + float64(combined[left+1])*fraction
		output = append(output, int16(math.Round(value)))
		resampler.phase += 2.0 / 3.0
	}
	resampler.phase -= float64(len(combined) - 1)
	resampler.previous = combined[len(combined)-1]
	encoded := make([]byte, len(output)*2)
	for index, value := range output {
		binary.LittleEndian.PutUint16(encoded[index*2:], uint16(value))
	}
	return encoded, nil
}
