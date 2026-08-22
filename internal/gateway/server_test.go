package gateway

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

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
	instructions := buildInstructions(nil, "audio", nil)
	for _, phrase := range []string{"только как русскую речь", "нерусскую или сомнительную", "turn_on", "turn_off", "ровно значение confirmation", "gachi-стиле", "Обычные вопросы"} {
		if !strings.Contains(instructions, phrase) {
			t.Fatalf("instructions do not contain %q", phrase)
		}
	}
}

func TestRecentTargetIsScopedAndExpires(t *testing.T) {
	server := &Server{recentTarget: make(map[string]recentTarget)}
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	server.rememberTarget("living-room", "light.hall", "Зал главный свет", now)

	target := server.getRecentTarget("living-room", now.Add(5*time.Minute))
	if target == nil || target.EntityID != "light.hall" {
		t.Fatalf("recent target missing: %#v", target)
	}
	if other := server.getRecentTarget("kitchen", now.Add(5*time.Minute)); other != nil {
		t.Fatalf("target leaked across satellites: %#v", other)
	}
	if expired := server.getRecentTarget("living-room", now.Add(11*time.Minute)); expired != nil {
		t.Fatalf("expired target returned: %#v", expired)
	}
}

func TestInstructionsIncludeRecentTarget(t *testing.T) {
	instructions := buildInstructions(nil, "audio", &recentTarget{EntityID: "light.hall", Name: "Зал главный свет"})
	for _, phrase := range []string{"light.hall", "Зал главный свет", "а теперь"} {
		if !strings.Contains(instructions, phrase) {
			t.Fatalf("instructions do not contain recent context %q", phrase)
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
	if result["confirmation"] != "Кондиционер включён, босс." {
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

func TestBuildToolsMakesWeatherReadOnly(t *testing.T) {
	entities := []homeassistant.Entity{
		{EntityID: "light.kitchen"},
		{EntityID: "weather.forecast_home"},
	}
	tools := buildTools(entities)
	controlProperties := tools[0].Parameters["properties"].(map[string]any)
	controlIDs := controlProperties["entity_id"].(map[string]any)["enum"].([]string)
	if slices.Contains(controlIDs, "weather.forecast_home") {
		t.Fatalf("weather leaked into control tool: %#v", controlIDs)
	}
	stateProperties := tools[1].Parameters["properties"].(map[string]any)
	stateIDs := stateProperties["entity_id"].(map[string]any)["enum"].([]string)
	if !slices.Contains(stateIDs, "weather.forecast_home") {
		t.Fatalf("weather missing from state tool: %#v", stateIDs)
	}
}

func TestToolResultForModelUsesExactWeatherConfirmation(t *testing.T) {
	call := liveapi.ToolCall{
		Name: "get_home_state",
		Args: map[string]any{"entity_id": "weather.forecast_home"},
	}
	payload := map[string]any{
		"ok": true, "entityId": "weather.forecast_home", "state": "clear-night",
		"temperature": 14.4, "temperatureUnit": "°C", "humidity": 87.0,
	}
	result := toolResultForModel(call, payload, nil)
	if result["confirmation"] != "Сейчас ясно, 14.4 °C, босс." {
		t.Fatalf("unexpected weather confirmation: %#v", result)
	}
}

func TestToolResultForModelLeavesOrdinaryStateToModel(t *testing.T) {
	call := liveapi.ToolCall{
		Name: "get_home_state",
		Args: map[string]any{"entity_id": "light.hall"},
	}
	result := toolResultForModel(call, map[string]any{"ok": true, "state": "on"}, nil)
	if _, exists := result["confirmation"]; exists {
		t.Fatalf("ordinary state should not have a hardcoded confirmation: %#v", result)
	}
	if result["state"] != "on" {
		t.Fatalf("ordinary state missing: %#v", result)
	}
}

func TestResponseBufferDropsPreambleBeforeToolCall(t *testing.T) {
	buffer := &responseBuffer{}
	if delivered := buffer.accept(liveapi.Event{Kind: liveapi.EventOutputTranscript, Text: "Секунду, включаю"}); len(delivered) != 0 {
		t.Fatalf("preamble delivered before tool decision: %#v", delivered)
	}
	if delivered := buffer.accept(liveapi.Event{Kind: liveapi.EventAudioDelta, Audio: []byte{1, 2}}); len(delivered) != 0 {
		t.Fatalf("audio preamble delivered before tool decision: %#v", delivered)
	}
	delivered := buffer.accept(liveapi.Event{Kind: liveapi.EventToolCall})
	if len(delivered) != 1 || delivered[0].Kind != liveapi.EventToolCall {
		t.Fatalf("tool call not delivered: %#v", delivered)
	}
	if len(buffer.pending) != 0 {
		t.Fatalf("preamble remains buffered: %#v", buffer.pending)
	}
}

func TestResponseBufferReleasesOrdinaryAnswerAtTurnComplete(t *testing.T) {
	buffer := &responseBuffer{}
	buffer.accept(liveapi.Event{Kind: liveapi.EventTextDelta, Text: "Ответ"})
	delivered := buffer.accept(liveapi.Event{Kind: liveapi.EventTurnComplete})
	if len(delivered) != 2 || delivered[0].Text != "Ответ" || delivered[1].Kind != liveapi.EventTurnComplete {
		t.Fatalf("ordinary response was not released in order: %#v", delivered)
	}
}
