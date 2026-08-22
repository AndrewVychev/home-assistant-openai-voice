package live

import "context"

type EventKind string

const (
	EventInputTranscript  EventKind = "input_transcript"
	EventOutputTranscript EventKind = "output_transcript"
	EventTextDelta        EventKind = "text_delta"
	EventAudioDelta       EventKind = "audio_delta"
	EventToolCall         EventKind = "tool_call"
	EventUsage            EventKind = "usage"
	EventInterrupted      EventKind = "interrupted"
	EventWaitingForInput  EventKind = "waiting_for_input"
	EventTurnComplete     EventKind = "turn_complete"
	EventError            EventKind = "error"
)

type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

type ToolResult struct {
	ID     string
	Name   string
	Output map[string]any
}

type Event struct {
	Kind      EventKind
	Text      string
	Audio     []byte
	MIMEType  string
	ToolCalls []ToolCall
	Usage     any
	Reason    string
}

type SessionConfig struct {
	Instructions string
	ResponseMode string
	Tools        []Tool
	Vocabulary   []string
}

type Provider interface {
	ID() string
	Model() string
	Configured() bool
	Connect(ctx context.Context, config SessionConfig) (Session, error)
}

type Session interface {
	Receive() (Event, error)
	SendAudio(pcm16k []byte) error
	EndAudio() error
	SendText(text string) error
	SendToolResults(results []ToolResult) error
	Close() error
}
