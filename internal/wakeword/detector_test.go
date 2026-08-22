package wakeword

import (
	"os"
	"testing"
)

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
	detector, err := New(Config{AssetsDir: assets, Threshold: 0.50, VADThreshold: 0.25})
	if err != nil {
		t.Fatal(err)
	}
	defer detector.Close()
	for frame := 0; frame < 30; frame++ {
		detected, score, err := detector.ProcessPCM16(make([]byte, 1280*2))
		if err != nil {
			t.Fatal(err)
		}
		if detected || score != 0 {
			t.Fatalf("silence must be suppressed, detected=%v score=%f", detected, score)
		}
	}
}
