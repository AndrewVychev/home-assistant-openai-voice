package live

import (
	"context"
	"strings"
	"sync"

	"google.golang.org/genai"
)

type GeminiProvider struct {
	apiKey string
	model  string
}

func NewGemini(apiKey, model string) *GeminiProvider {
	return &GeminiProvider{apiKey: strings.TrimSpace(apiKey), model: strings.TrimSpace(model)}
}

func (provider *GeminiProvider) ID() string       { return "gemini" }
func (provider *GeminiProvider) Model() string    { return provider.model }
func (provider *GeminiProvider) Configured() bool { return provider.apiKey != "" }

func (provider *GeminiProvider) Connect(ctx context.Context, config SessionConfig) (Session, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  provider.apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, err
	}
	temperature := float32(0)
	session, err := client.Live.Connect(ctx, provider.model, &genai.LiveConnectConfig{
		ResponseModalities: []genai.Modality{genai.ModalityAudio},
		Temperature:        &temperature,
		MaxOutputTokens:    64,
		ThinkingConfig:     &genai.ThinkingConfig{ThinkingLevel: genai.ThinkingLevelMinimal},
		SpeechConfig:       &genai.SpeechConfig{LanguageCode: "ru-RU"},
		SystemInstruction:  genai.NewContentFromText(config.Instructions, genai.RoleUser),
		Tools:              geminiTools(config.Tools),
		InputAudioTranscription: &genai.AudioTranscriptionConfig{
			LanguageCodes:    []string{"ru-RU"},
			CustomVocabulary: config.Vocabulary,
		},
		OutputAudioTranscription: &genai.AudioTranscriptionConfig{LanguageCodes: []string{"ru-RU"}},
	})
	if err != nil {
		return nil, err
	}
	return &geminiSession{session: session}, nil
}

func geminiTools(tools []Tool) []*genai.Tool {
	declarations := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		declarations = append(declarations, &genai.FunctionDeclaration{
			Name:                 tool.Name,
			Description:          tool.Description,
			ParametersJsonSchema: tool.Parameters,
		})
	}
	if len(declarations) == 0 {
		return nil
	}
	return []*genai.Tool{{FunctionDeclarations: declarations}}
}

type geminiSession struct {
	session *genai.Session
	mu      sync.Mutex
	queue   []Event
}

func (session *geminiSession) Receive() (Event, error) {
	for {
		if len(session.queue) > 0 {
			event := session.queue[0]
			session.queue = session.queue[1:]
			return event, nil
		}
		message, err := session.session.Receive()
		if err != nil {
			return Event{}, err
		}
		session.enqueue(message)
	}
}

func (session *geminiSession) enqueue(message *genai.LiveServerMessage) {
	content := message.ServerContent
	if content != nil {
		if content.InputTranscription != nil && content.InputTranscription.Text != "" {
			session.queue = append(session.queue, Event{Kind: EventInputTranscript, Text: content.InputTranscription.Text})
		}
		if content.OutputTranscription != nil && content.OutputTranscription.Text != "" {
			session.queue = append(session.queue, Event{Kind: EventOutputTranscript, Text: content.OutputTranscription.Text})
		}
		if content.ModelTurn != nil {
			for _, part := range content.ModelTurn.Parts {
				if part.Text != "" {
					session.queue = append(session.queue, Event{Kind: EventTextDelta, Text: part.Text})
				}
				if part.InlineData != nil && len(part.InlineData.Data) > 0 {
					session.queue = append(session.queue, Event{
						Kind: EventAudioDelta, Audio: part.InlineData.Data,
						MIMEType: valueOr(part.InlineData.MIMEType, "audio/pcm;rate=24000"),
					})
				}
			}
		}
		if content.Interrupted {
			session.queue = append(session.queue, Event{Kind: EventInterrupted})
		}
		if content.WaitingForInput {
			session.queue = append(session.queue, Event{Kind: EventWaitingForInput})
		}
		if content.TurnComplete {
			session.queue = append(session.queue, Event{Kind: EventTurnComplete, Reason: string(content.TurnCompleteReason)})
		}
	}
	if message.UsageMetadata != nil {
		session.queue = append(session.queue, Event{Kind: EventUsage, Usage: message.UsageMetadata})
	}
	if message.ToolCall != nil && len(message.ToolCall.FunctionCalls) > 0 {
		calls := make([]ToolCall, 0, len(message.ToolCall.FunctionCalls))
		for _, call := range message.ToolCall.FunctionCalls {
			calls = append(calls, ToolCall{ID: call.ID, Name: call.Name, Args: call.Args})
		}
		session.queue = append(session.queue, Event{Kind: EventToolCall, ToolCalls: calls})
	}
}

func (session *geminiSession) SendAudio(data []byte) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.session.SendRealtimeInput(genai.LiveRealtimeInput{
		Audio: &genai.Blob{Data: data, MIMEType: "audio/pcm;rate=16000"},
	})
}

func (session *geminiSession) EndAudio() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.session.SendRealtimeInput(genai.LiveRealtimeInput{AudioStreamEnd: true})
}

func (session *geminiSession) SendText(text string) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	complete := true
	return session.session.SendClientContent(genai.LiveClientContentInput{
		Turns:        []*genai.Content{genai.NewContentFromText(text, genai.RoleUser)},
		TurnComplete: &complete,
	})
}

func (session *geminiSession) SendToolResults(results []ToolResult) error {
	responses := make([]*genai.FunctionResponse, 0, len(results))
	for _, result := range results {
		responses = append(responses, &genai.FunctionResponse{
			ID: result.ID, Name: result.Name, Response: result.Output,
		})
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.session.SendToolResponse(genai.LiveToolResponseInput{FunctionResponses: responses})
}

func (session *geminiSession) Close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.session.Close()
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
