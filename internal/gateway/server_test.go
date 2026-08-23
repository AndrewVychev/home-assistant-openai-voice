package gateway

import (
	"errors"
	"io"
	"log"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"homevoice/internal/config"
	"homevoice/internal/homeassistant"
	liveapi "homevoice/internal/live"
)

func TestHeadlessHandlerRejectsUnknownProtocolAndHasNoWebRoot(t *testing.T) {
	server := New(config.Config{}, log.New(io.Discard, "", 0))

	unknownProtocol := httptest.NewRecorder()
	server.Handler().ServeHTTP(unknownProtocol, httptest.NewRequest("GET", "/live?protocol=2", nil))
	if unknownProtocol.Code != 400 {
		t.Fatalf("unknown protocol status = %d", unknownProtocol.Code)
	}

	root := httptest.NewRecorder()
	server.Handler().ServeHTTP(root, httptest.NewRequest("GET", "/", nil))
	if root.Code != 404 {
		t.Fatalf("headless root status = %d", root.Code)
	}
}

func TestSatelliteAuthorization(t *testing.T) {
	request := httptest.NewRequest("GET", "/live", nil)
	if !authorizedSatellite(request, "") {
		t.Fatal("empty configured token should keep local development available")
	}
	if authorizedSatellite(request, "secret") {
		t.Fatal("missing bearer token was accepted")
	}
	request.Header.Set("Authorization", "Bearer secret")
	if !authorizedSatellite(request, "secret") {
		t.Fatal("valid bearer token was rejected")
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if authorizedSatellite(request, "secret") {
		t.Fatal("invalid bearer token was accepted")
	}
}

func TestServerWakePreRollKeepsNewestAudio(t *testing.T) {
	buffer := appendServerPreRoll(nil, []byte{1, 2, 3, 4}, 6)
	buffer = appendServerPreRoll(buffer, []byte{5, 6, 7, 8}, 6)
	if !slices.Equal(buffer, []byte{3, 4, 5, 6, 7, 8}) {
		t.Fatalf("unexpected pre-roll: %v", buffer)
	}
	buffer = appendServerPreRoll(buffer, []byte{9, 10, 11, 12, 13, 14, 15, 16}, 6)
	if !slices.Equal(buffer, []byte{11, 12, 13, 14, 15, 16}) {
		t.Fatalf("oversized frame did not replace pre-roll: %v", buffer)
	}
}

func TestGatewayClarificationDetection(t *testing.T) {
	if !asksForClarification("Кухня или спальня?") {
		t.Fatal("question was not recognized as clarification")
	}
	if asksForClarification("Свет включён") {
		t.Fatal("completed action was recognized as clarification")
	}
}

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
	for _, phrase := range []string{"только как русскую речь", "нерусскую или сомнительную", "turn_on", "turn_off", "точным полям action", "gachi-стиле", "обращайся «мастер»", "Dungeon Master", "Boss of this gym", "Fisting is three hundred bucks", "Обычные вопросы"} {
		if !strings.Contains(instructions, phrase) {
			t.Fatalf("instructions do not contain %q", phrase)
		}
	}
	for _, forbidden := range []string{"никогда не говори «босс»", "не добавляй сексуальных подробностей"} {
		if strings.Contains(instructions, forbidden) {
			t.Fatalf("instructions still contain removed rule %q", forbidden)
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

func TestToolResultForModelUsesStructuredFactsAndDropsRecoveryStatus(t *testing.T) {
	call := liveapi.ToolCall{
		Name: "control_home_entity",
		Args: map[string]any{
			"entity_id": "climate.living_room_room_air_conditioner",
			"action":    "turn_on",
		},
	}
	payload := map[string]any{
		"ok": true, "entityId": "climate.living_room_room_air_conditioner",
		"name": "Room air conditioner", "action": "turn_on", "state": "auto", "confirmed": true,
		"recoveredFromStatus": float64(500),
	}

	result := toolResultForModel(call, payload, nil)
	if result["requestedAction"] != "turn_on" || result["action"] != "turn_on" || result["state"] != "auto" {
		t.Fatalf("unexpected structured result: %#v", result)
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

func TestToolResultForModelLeavesWeatherAsStructuredFacts(t *testing.T) {
	call := liveapi.ToolCall{
		Name: "get_home_state",
		Args: map[string]any{"entity_id": "weather.forecast_home"},
	}
	payload := map[string]any{
		"ok": true, "entityId": "weather.forecast_home", "state": "clear-night",
		"temperature": 14.4, "temperatureUnit": "°C", "humidity": 87.0,
	}
	result := toolResultForModel(call, payload, nil)
	if result["state"] != "clear-night" || result["temperature"] != 14.4 || result["temperatureUnit"] != "°C" {
		t.Fatalf("unexpected weather facts: %#v", result)
	}
	if _, exists := result["confirmation"]; exists {
		t.Fatalf("weather response should not be hardcoded: %#v", result)
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
