package gateway

import (
	"slices"
	"strings"
	"testing"

	"homevoice/internal/guard"
	"homevoice/internal/homeassistant"
)

func TestTranscriptionVocabularyContainsRussianCommandsAndEntities(t *testing.T) {
	entities := []homeassistant.Entity{{EntityID: "climate.living_room", Name: "Кондиционер", Area: "Гостиная"}}
	vocabulary := transcriptionVocabulary(entities)
	for _, phrase := range []string{"включи", "выключи", "кондиционер", "гостиная"} {
		if !slices.Contains(vocabulary, phrase) {
			t.Fatalf("custom vocabulary does not contain %q: %#v", phrase, vocabulary)
		}
	}
}

func TestInstructionsRejectUncertainForeignTranscript(t *testing.T) {
	instructions := buildInstructions(nil, "audio")
	for _, phrase := range []string{"только как русскую речь", "нерусскую или сомнительную", "turn_on", "turn_off"} {
		if !strings.Contains(instructions, phrase) {
			t.Fatalf("instructions do not contain %q", phrase)
		}
	}
}

func TestLiveSessionAccumulatesClarificationTranscriptUntilToolCall(t *testing.T) {
	conversation := &liveSession{}
	conversation.appendInputTranscript("Включи свет.")
	conversation.appendInputTranscript("Кухня")
	conversation.appendInputTranscript("Основной")

	want := "Включи свет.\nКухня\nОсновной"
	if got := conversation.inputTranscript(); got != want {
		t.Fatalf("input transcript = %q, want %q", got, want)
	}
	if err := guard.ValidateTranscriptAction(conversation.inputTranscript(), "turn_on"); err != nil {
		t.Fatalf("accumulated clarification must preserve original action: %v", err)
	}
	conversation.clearInputTranscript()
	if got := conversation.inputTranscript(); got != "" {
		t.Fatalf("input transcript after clear = %q", got)
	}
}
