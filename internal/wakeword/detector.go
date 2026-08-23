package wakeword

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"

	openwakeword "github.com/stanislaw-glogowski/openwakeword_go"
	ort "github.com/yalue/onnxruntime_go"
)

const ModelName = "hey_jarvis"

type Config struct {
	AssetsDir    string
	Threshold    float32
	VADThreshold float32
}

type Detector struct {
	engine      *openwakeword.Engine
	threshold   float32
	environment bool
	pending     []byte
	mu          sync.Mutex
}

func New(config Config) (*Detector, error) {
	if config.Threshold <= 0 || config.Threshold > 1 {
		return nil, errors.New("wake threshold должен быть в диапазоне (0, 1]")
	}
	if config.VADThreshold < 0 || config.VADThreshold > 1 {
		return nil, errors.New("VAD threshold должен быть в диапазоне [0, 1]")
	}
	runtimePath, err := runtimeLibrary(config.AssetsDir)
	if err != nil {
		return nil, err
	}
	ort.SetSharedLibraryPath(runtimePath)
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("инициализация ONNX Runtime: %w", err)
	}
	detector := &Detector{threshold: config.Threshold, environment: true}
	fail := func(cause error) (*Detector, error) {
		_ = detector.Close()
		return nil, cause
	}

	models := filepath.Join(config.AssetsDir, "models")
	features, err := openwakeword.NewAudioFeatures(
		filepath.Join(models, "melspectrogram.onnx"),
		filepath.Join(models, "embedding_model.onnx"),
	)
	if err != nil {
		return fail(fmt.Errorf("wake features: %w", err))
	}
	var vad *openwakeword.VAD
	if config.VADThreshold > 0 {
		vad, err = openwakeword.NewVAD(
			filepath.Join(models, "silero_vad.onnx"),
			openwakeword.WithVADThreshold(config.VADThreshold),
		)
		if err != nil {
			_ = features.Close()
			return fail(fmt.Errorf("wake VAD: %w", err))
		}
	}
	engine, err := openwakeword.New(features, vad)
	if err != nil {
		_ = features.Close()
		if vad != nil {
			_ = vad.Close()
		}
		return fail(err)
	}
	detector.engine = engine
	if err := engine.AddModel(
		filepath.Join(models, "hey_jarvis_v0.1.onnx"),
		openwakeword.WithModelName(ModelName),
		openwakeword.WithModelThreshold(config.Threshold),
		openwakeword.WithModelPredictionHistory(30),
	); err != nil {
		return fail(fmt.Errorf("wake model: %w", err))
	}
	return detector, nil
}

func (detector *Detector) ProcessPCM16(data []byte) (detected bool, score float32, err error) {
	if len(data)%2 != 0 {
		return false, 0, errors.New("PCM16 chunk имеет нечётную длину")
	}
	detector.mu.Lock()
	defer detector.mu.Unlock()
	if detector.engine == nil {
		return false, 0, errors.New("wake detector закрыт")
	}

	// openWakeWord is trained and documented around 80 ms (1280 sample)
	// inference frames. The microphone callback normally delivers 20 ms chunks,
	// so buffer them here. This also keeps the optional VAD on the same timeline
	// as the wake-word feature extractor instead of scoring partial frames.
	detector.pending = append(detector.pending, data...)
	const frameBytes = openwakeword.FrameSamples * 2
	for len(detector.pending) >= frameBytes {
		frame := detector.pending[:frameBytes]
		samples := pcm16Samples(frame)
		scores, predictErr := detector.engine.Predict(samples)
		if predictErr != nil {
			return false, score, predictErr
		}
		if current := scores[ModelName]; current > score {
			score = current
		}
		detector.pending = detector.pending[frameBytes:]
	}
	if len(detector.pending) == 0 {
		detector.pending = detector.pending[:0]
	}
	return score >= detector.threshold, score, nil
}

func pcm16Samples(data []byte) openwakeword.Samples {
	samples := make(openwakeword.Samples, len(data)/2)
	for index := range samples {
		value := int16(binary.LittleEndian.Uint16(data[index*2:]))
		if value < 0 {
			samples[index] = float32(value) / 32768
		} else {
			samples[index] = float32(value) / 32767
		}
	}
	return samples
}

func (detector *Detector) Reset() error {
	detector.mu.Lock()
	defer detector.mu.Unlock()
	if detector.engine != nil {
		detector.engine.Reset()
		detector.pending = detector.pending[:0]
	}
	return nil
}

func (detector *Detector) Phrase() string {
	return "Хей, Джарвис"
}

func (detector *Detector) Close() error {
	if detector == nil {
		return nil
	}
	detector.mu.Lock()
	defer detector.mu.Unlock()
	var result error
	if detector.engine != nil {
		result = detector.engine.Close()
		detector.engine = nil
	}
	if detector.environment {
		result = errors.Join(result, ort.DestroyEnvironment())
		detector.environment = false
	}
	return result
}

func runtimeLibrary(assetsDir string) (string, error) {
	name := ""
	switch runtime.GOOS {
	case "darwin":
		name = "libonnxruntime.dylib"
	case "linux":
		name = "libonnxruntime.so"
	case "windows":
		name = "onnxruntime.dll"
	default:
		return "", fmt.Errorf("wake word не поддерживает %s", runtime.GOOS)
	}
	return filepath.Join(assetsDir, "runtime", name), nil
}
