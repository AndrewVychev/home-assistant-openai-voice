package live

import (
	"encoding/binary"
	"testing"
)

func TestProvidersImplementContract(t *testing.T) {
	var _ Provider = (*GeminiProvider)(nil)
	var _ Provider = (*OpenAIProvider)(nil)
	var _ Session = (*geminiSession)(nil)
	var _ Session = (*openAISession)(nil)
}

func TestGeminiToolsPreserveGenericSchema(t *testing.T) {
	tools := geminiTools([]Tool{{
		Name: "turn_on", Description: "Включить", Parameters: map[string]any{"type": "object"},
	}})
	if len(tools) != 1 || len(tools[0].FunctionDeclarations) != 1 {
		t.Fatalf("tools = %#v", tools)
	}
	declaration := tools[0].FunctionDeclarations[0]
	parameters, ok := declaration.ParametersJsonSchema.(map[string]any)
	if !ok || declaration.Name != "turn_on" || parameters["type"] != "object" {
		t.Fatalf("declaration = %#v", declaration)
	}
}

func TestOpenAISessionUpdateUsesRealtimeAudioAndRussianTranscription(t *testing.T) {
	update := openAISessionUpdate("gpt-realtime-2.1-mini", "marin", SessionConfig{
		Instructions: "Коротко", ResponseMode: "audio", Vocabulary: []string{"включи", "выключи"},
		Tools: []Tool{{Name: "control", Parameters: map[string]any{"type": "object"}}},
	})
	session := update["session"].(map[string]any)
	if got := session["output_modalities"].([]string); len(got) != 1 || got[0] != "audio" {
		t.Fatalf("output modalities = %#v", got)
	}
	audio := session["audio"].(map[string]any)
	input := audio["input"].(map[string]any)
	format := input["format"].(map[string]any)
	if format["rate"] != 24000 {
		t.Fatalf("input rate = %#v", format["rate"])
	}
	transcription := input["transcription"].(map[string]any)
	if transcription["language"] != "ru" {
		t.Fatalf("transcription = %#v", transcription)
	}
	if _, exists := transcription["prompt"]; exists {
		t.Fatalf("device vocabulary must not be sent as transcription prompt: %#v", transcription)
	}
	if input["noise_reduction"].(map[string]any)["type"] != "far_field" {
		t.Fatalf("noise reduction = %#v", input["noise_reduction"])
	}
	if session["max_output_tokens"] != 256 {
		t.Fatalf("max output tokens = %#v", session["max_output_tokens"])
	}
	output := audio["output"].(map[string]any)
	if output["format"].(map[string]any)["rate"] != 24000 || output["voice"] != "marin" {
		t.Fatalf("output audio = %#v", output)
	}
}

func TestOpenAIRejectsTranscriptionPromptEcho(t *testing.T) {
	echo := "включи, выключи, отключи, свет, лампа, кондиционер, кухня, Kitchen Main"
	if !looksLikeTranscriptionPromptEcho(echo) {
		t.Fatal("expected vocabulary echo to be rejected")
	}
	if looksLikeTranscriptionPromptEcho("включи свет в кухне") {
		t.Fatal("normal command must not be rejected")
	}
}

func TestPCMResamplerKeepsStateAcrossChunks(t *testing.T) {
	var resampler pcmResampler
	input := make([]byte, 16_000*2)
	for index := 0; index < 16_000; index++ {
		binary.LittleEndian.PutUint16(input[index*2:], uint16(int16(index%1000)))
	}
	var output []byte
	for offset := 0; offset < len(input); offset += 640 {
		chunk, err := resampler.Process(input[offset : offset+640])
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, chunk...)
	}
	samples := len(output) / 2
	if samples < 23_998 || samples > 24_001 {
		t.Fatalf("resampled samples = %d, want about 24000", samples)
	}
}

func TestPCMResamplerRejectsOddByteCount(t *testing.T) {
	var resampler pcmResampler
	if _, err := resampler.Process([]byte{1}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestOpenAIToolCallDeduplicatesDoneEvents(t *testing.T) {
	session := &openAISession{}
	if _, ready, err := session.toolCallEvent("call-1", "control", ""); err != nil || ready {
		t.Fatalf("empty arguments: ready=%v err=%v", ready, err)
	}
	event, ready, err := session.toolCallEvent("call-1", "control", `{"action":"turn_on"}`)
	if err != nil || !ready {
		t.Fatalf("complete call: ready=%v err=%v", ready, err)
	}
	if event.ToolCalls[0].Args["action"] != "turn_on" {
		t.Fatalf("event = %#v", event)
	}
	if _, ready, err := session.toolCallEvent("call-1", "control", `{"action":"turn_on"}`); err != nil || ready {
		t.Fatalf("duplicate call: ready=%v err=%v", ready, err)
	}
}
