package gateway

import (
	"slices"
	"strings"
	"testing"

	"homevoice/internal/homeassistant"
)

func TestLiveConfigPinsRussianTranscription(t *testing.T) {
	entities := []homeassistant.Entity{{EntityID: "climate.living_room", Name: "Кондиционер", Area: "Гостиная"}}
	config := liveConfig(entities, "audio")

	if config.SpeechConfig == nil || config.SpeechConfig.LanguageCode != "ru-RU" {
		t.Fatalf("speech language = %#v, want ru-RU", config.SpeechConfig)
	}
	if config.InputAudioTranscription == nil || !slices.Equal(config.InputAudioTranscription.LanguageCodes, []string{"ru-RU"}) {
		t.Fatalf("input languages = %#v, want ru-RU", config.InputAudioTranscription)
	}
	for _, phrase := range []string{"включи", "выключи", "кондиционер", "гостиная"} {
		if !slices.Contains(config.InputAudioTranscription.CustomVocabulary, phrase) {
			t.Fatalf("custom vocabulary does not contain %q: %#v", phrase, config.InputAudioTranscription.CustomVocabulary)
		}
	}
	if config.Temperature == nil || *config.Temperature != 0 {
		t.Fatalf("temperature = %#v, want explicit zero", config.Temperature)
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
