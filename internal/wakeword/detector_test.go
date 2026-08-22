package wakeword

import (
	"os"
	"testing"
)

func TestDetectorsImplementStreamContract(t *testing.T) {
	var _ StreamDetector = (*Detector)(nil)
	var _ StreamDetector = (*MicroDetector)(nil)
}

func TestRejectsInvalidThreshold(t *testing.T) {
	if _, err := New(Config{Threshold: 1.1}); err == nil {
		t.Fatal("expected invalid threshold to fail before loading assets")
	}
}

func TestRejectsOddPCMChunk(t *testing.T) {
	detector := &Detector{}
	if _, _, err := detector.ProcessPCM16([]byte{1}); err == nil {
		t.Fatal("expected odd PCM16 chunk to fail")
	}
}

func TestOfficialHeyJarvisModelSuppressesSilence(t *testing.T) {
	assets := os.Getenv("HOMEVOICE_WAKE_ASSETS")
	if assets == "" {
		t.Skip("set HOMEVOICE_WAKE_ASSETS to run ONNX integration test")
	}
	detector, err := New(Config{AssetsDir: assets, Threshold: 0.35, VADThreshold: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer detector.Close()
	// Real microphone callbacks arrive every 20 ms (320 samples). The detector
	// must aggregate four callbacks into each 80 ms openWakeWord frame.
	for chunk := 0; chunk < 120; chunk++ {
		detected, score, err := detector.ProcessPCM16(make([]byte, 320*2))
		if err != nil {
			t.Fatal(err)
		}
		if detected || score >= 0.01 {
			t.Fatalf("silence score must stay negligible, detected=%v score=%f", detected, score)
		}
	}
}

func TestMicroKuzaModelLoadsAndSuppressesSilence(t *testing.T) {
	assets := os.Getenv("HOMEVOICE_MICRO_WAKE_ASSETS")
	if assets == "" {
		t.Skip("set HOMEVOICE_MICRO_WAKE_ASSETS to run microWakeWord integration test")
	}
	detector, err := NewMicro(MicroConfig{AssetsDir: assets})
	if err != nil {
		t.Fatal(err)
	}
	defer detector.Close()
	if detector.Phrase() != "Куза" {
		t.Fatalf("phrase = %q, want Куза", detector.Phrase())
	}
	for chunk := 0; chunk < 100; chunk++ {
		detected, score, err := detector.ProcessPCM16(make([]byte, 320*2))
		if err != nil {
			t.Fatal(err)
		}
		if detected || score >= 0.85 {
			t.Fatalf("silence must stay below Kuza threshold, detected=%v score=%f", detected, score)
		}
	}
}
