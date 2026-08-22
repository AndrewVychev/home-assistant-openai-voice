package gateway

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"homevoice/internal/homeassistant"
	liveapi "homevoice/internal/live"
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
	for _, phrase := range []string{"только как русскую речь", "нерусскую или сомнительную", "turn_on", "turn_off", "ровно значение confirmation", "Обычные вопросы"} {
		if !strings.Contains(instructions, phrase) {
			t.Fatalf("instructions do not contain %q", phrase)
		}
	}
}

func TestToolResultForModelUsesExactConfirmationAndDropsRecoveryStatus(t *testing.T) {
	call := liveapi.ToolCall{
		Name: "control_home_entity",
		Args: map[string]any{
			"entity_id": "climate.living_room_room_air_conditioner",
			"action":    "turn_on",
		},
	}
	payload := map[string]any{
		"ok": true, "entityId": "climate.living_room_room_air_conditioner",
		"name": "Room air conditioner", "state": "auto", "confirmed": true,
		"recoveredFromStatus": float64(500),
	}

	result := toolResultForModel(call, payload, nil)
	if result["confirmation"] != "Кондиционер включён." {
		t.Fatalf("unexpected confirmation: %#v", result["confirmation"])
	}
	if _, exists := result["recoveredFromStatus"]; exists {
		t.Fatalf("technical recovery status leaked to model: %#v", result)
	}
	if _, exists := result["output"]; exists {
		t.Fatalf("tool result is still wrapped in output: %#v", result)
	}
}

func TestToolResultForModelReturnsExplicitError(t *testing.T) {
	result := toolResultForModel(liveapi.ToolCall{}, nil, errors.New("boom"))
	if result["ok"] != false || result["error"] != "boom" {
		t.Fatalf("unexpected error result: %#v", result)
	}
}
