package wakeword

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type StreamDetector interface {
	ProcessPCM16(data []byte) (detected bool, score float32, err error)
	Reset() error
	Close() error
	Phrase() string
}

type MicroConfig struct {
	AssetsDir string
	Threshold float32
}

type MicroDetector struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	stderr    bytes.Buffer
	phrase    string
	mu        sync.Mutex
	closed    bool
	waitOnce  sync.Once
	waitError error
}

type microResponse struct {
	Type      string  `json:"type"`
	Phrase    string  `json:"phrase"`
	Score     float32 `json:"score"`
	Detected  bool    `json:"detected"`
	Threshold float32 `json:"threshold"`
	Error     string  `json:"error"`
}

func NewMicro(config MicroConfig) (*MicroDetector, error) {
	if config.Threshold < 0 || config.Threshold > 1 {
		return nil, errors.New("micro wake threshold должен быть в диапазоне [0, 1]")
	}
	assets, err := filepath.Abs(config.AssetsDir)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(assets, "Kuza.json")
	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("модель Куза не найдена: %w", err)
	}
	python := filepath.Join(assets, "venv", "bin", "python")
	if runtime.GOOS == "windows" {
		python = filepath.Join(assets, "venv", "Scripts", "python.exe")
	}
	if _, err := os.Stat(python); err != nil {
		return nil, fmt.Errorf("pymicro-wakeword не установлен; выполни `make setup-kuza`: %w", err)
	}

	cmd := exec.Command(python, "-u", "-c", microWorkerSource, configPath, strconv.FormatFloat(float64(config.Threshold), 'f', -1, 32))
	detector := &MicroDetector{cmd: cmd}
	cmd.Stderr = &detector.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	detector.stdin = stdin
	detector.stdout = bufio.NewReader(stdout)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("запуск microWakeWord: %w", err)
	}

	ready := make(chan error, 1)
	go func() {
		response, responseErr := detector.readResponse()
		if responseErr == nil {
			if response.Type != "ready" {
				responseErr = fmt.Errorf("ожидался ready, получено %q", response.Type)
			} else {
				detector.phrase = response.Phrase
			}
		}
		ready <- responseErr
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = detector.Close()
			return nil, err
		}
	case <-time.After(30 * time.Second):
		_ = detector.Close()
		return nil, errors.New("таймаут запуска microWakeWord")
	}
	return detector, nil
}

func (detector *MicroDetector) ProcessPCM16(data []byte) (bool, float32, error) {
	if len(data)%2 != 0 {
		return false, 0, errors.New("PCM16 chunk имеет нечётную длину")
	}
	detector.mu.Lock()
	defer detector.mu.Unlock()
	if detector.closed {
		return false, 0, errors.New("micro wake detector закрыт")
	}
	if _, err := fmt.Fprintln(detector.stdin, base64.StdEncoding.EncodeToString(data)); err != nil {
		return false, 0, detector.workerError(err)
	}
	response, err := detector.readResponse()
	if err != nil {
		return false, 0, err
	}
	return response.Detected, response.Score, nil
}

func (detector *MicroDetector) Reset() error {
	detector.mu.Lock()
	defer detector.mu.Unlock()
	if detector.closed {
		return nil
	}
	if _, err := fmt.Fprintln(detector.stdin, "RESET"); err != nil {
		return detector.workerError(err)
	}
	response, err := detector.readResponse()
	if err != nil {
		return err
	}
	if response.Type != "reset" {
		return fmt.Errorf("microWakeWord reset: неожиданное сообщение %q", response.Type)
	}
	return nil
}

func (detector *MicroDetector) Phrase() string {
	if detector.phrase == "" || strings.EqualFold(detector.phrase, "Kuza") {
		return "Куза"
	}
	return detector.phrase
}

func (detector *MicroDetector) Close() error {
	if detector == nil {
		return nil
	}
	detector.mu.Lock()
	if detector.closed {
		detector.mu.Unlock()
		return detector.wait()
	}
	detector.closed = true
	_, _ = fmt.Fprintln(detector.stdin, "CLOSE")
	_ = detector.stdin.Close()
	detector.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- detector.wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		if detector.cmd.Process != nil {
			_ = detector.cmd.Process.Kill()
		}
		return detector.wait()
	}
}

func (detector *MicroDetector) readResponse() (microResponse, error) {
	line, err := detector.stdout.ReadString('\n')
	if err != nil {
		return microResponse{}, detector.workerError(err)
	}
	var response microResponse
	if err := json.Unmarshal([]byte(line), &response); err != nil {
		return microResponse{}, fmt.Errorf("microWakeWord response %q: %w", strings.TrimSpace(line), err)
	}
	if response.Error != "" {
		return microResponse{}, errors.New(response.Error)
	}
	return response, nil
}

func (detector *MicroDetector) workerError(cause error) error {
	details := strings.TrimSpace(detector.stderr.String())
	if details == "" {
		return fmt.Errorf("microWakeWord worker: %w", cause)
	}
	return fmt.Errorf("microWakeWord worker: %w: %s", cause, details)
}

func (detector *MicroDetector) wait() error {
	detector.waitOnce.Do(func() { detector.waitError = detector.cmd.Wait() })
	return detector.waitError
}

const microWorkerSource = `
import base64
import ctypes
import json
import os
from pathlib import Path
import sys
import types

import numpy as np
from pymicro_wakeword.wakeword import TfLiteWakeWord
import pymicro_wakeword.microwakeword as microwakeword_module

_TFLITE_TYPES = {1: np.float32, 3: np.uint8, 9: np.int8}
_TFLITE_LIBRARY = next(
    iter((Path(microwakeword_module.__file__).parent / "lib").glob("*tensorflowlite_c.*")),
    None,
)
if _TFLITE_LIBRARY is None:
    raise RuntimeError("TensorFlow Lite C runtime not found")

class Interpreter(TfLiteWakeWord):
    """Small tflite-runtime compatible adapter over the bundled C API."""

    def __init__(self, model_path, num_threads=1):
        super().__init__(_TFLITE_LIBRARY)
        self.model_path = str(Path(model_path).resolve()).encode("utf-8")
        self.model = self.lib.TfLiteModelCreateFromFile(self.model_path)
        self.interpreter = self.lib.TfLiteInterpreterCreate(self.model, None)
        if not self.model or not self.interpreter:
            raise RuntimeError("TensorFlow Lite could not load model")

    def allocate_tensors(self):
        if self.lib.TfLiteInterpreterAllocateTensors(self.interpreter) != 0:
            raise RuntimeError("TensorFlow Lite tensor allocation failed")

    def _tensor(self, output=False):
        getter = (
            self.lib.TfLiteInterpreterGetOutputTensor
            if output else self.lib.TfLiteInterpreterGetInputTensor
        )
        return getter(self.interpreter, 0)

    def _details(self, output=False):
        tensor = self._tensor(output)
        dimensions = self.lib.TfLiteTensorNumDims(tensor)
        shape = np.array(
            [self.lib.TfLiteTensorDim(tensor, i) for i in range(dimensions)],
            dtype=np.int32,
        )
        tensor_type = self.lib.TfLiteTensorType(tensor)
        if tensor_type not in _TFLITE_TYPES:
            raise RuntimeError("unsupported TensorFlow Lite tensor type: %s" % tensor_type)
        return [{"index": 0, "shape": shape, "dtype": _TFLITE_TYPES[tensor_type]}]

    def get_input_details(self):
        return self._details(False)

    def get_output_details(self):
        return self._details(True)

    def resize_tensor_input(self, index, shape, strict=True):
        dimensions = (ctypes.c_int32 * len(shape))(*[int(value) for value in shape])
        status = self.lib.TfLiteInterpreterResizeInputTensor(
            self.interpreter, int(index), dimensions, len(shape)
        )
        if status != 0:
            raise RuntimeError("TensorFlow Lite input resize failed")

    def set_tensor(self, index, value):
        tensor = self._tensor(False)
        dtype = self.get_input_details()[0]["dtype"]
        data = np.ascontiguousarray(value, dtype=dtype)
        if data.nbytes != self.lib.TfLiteTensorByteSize(tensor):
            raise RuntimeError("TensorFlow Lite input size mismatch")
        status = self.lib.TfLiteTensorCopyFromBuffer(
            tensor, data.ctypes.data_as(ctypes.c_void_p), data.nbytes
        )
        if status != 0:
            raise RuntimeError("TensorFlow Lite rejected input")

    def invoke(self):
        if self.lib.TfLiteInterpreterInvoke(self.interpreter) != 0:
            raise RuntimeError("TensorFlow Lite inference failed")

    def get_tensor(self, index):
        tensor = self._tensor(True)
        details = self.get_output_details()[0]
        output = np.empty(details["shape"], dtype=details["dtype"])
        status = self.lib.TfLiteTensorCopyToBuffer(
            tensor, output.ctypes.data_as(ctypes.c_void_p), output.nbytes
        )
        if status != 0:
            raise RuntimeError("TensorFlow Lite rejected output")
        return output

    def close(self):
        if getattr(self, "interpreter", None):
            self.lib.TfLiteInterpreterDelete(self.interpreter)
            self.interpreter = None
        if getattr(self, "model", None):
            self.lib.TfLiteModelDelete(self.model)
            self.model = None

    def __del__(self):
        self.close()

# openWakeWord imports tflite_runtime lazily. Supply the same small API on
# platforms such as Apple Silicon where that Python wheel is unavailable.
runtime_package = types.ModuleType("tflite_runtime")
interpreter_module = types.ModuleType("tflite_runtime.interpreter")
interpreter_module.Interpreter = Interpreter
runtime_package.interpreter = interpreter_module
sys.modules["tflite_runtime"] = runtime_package
sys.modules["tflite_runtime.interpreter"] = interpreter_module

from openwakeword import Model

config_path = sys.argv[1]
threshold = float(sys.argv[2])
assets_dir = os.path.dirname(config_path)
with open(config_path, "r", encoding="utf-8") as config_file:
    config = json.load(config_file)
phrase = config["wake_word"]
cutoff = threshold if threshold > 0 else float(config["micro"]["probability_cutoff"])
model_path = os.path.join(assets_dir, config["model"])
model_name = Path(model_path).stem
detector = Model(
    wakeword_models=[model_path],
    inference_framework="tflite",
    melspec_model_path=os.path.join(assets_dir, "melspectrogram.tflite"),
    embedding_model_path=os.path.join(assets_dir, "embedding_model.tflite"),
)

def emit(value):
    print(json.dumps(value, ensure_ascii=False), flush=True)

emit({"type": "ready", "phrase": phrase, "threshold": cutoff})
for raw_line in sys.stdin:
    line = raw_line.strip()
    try:
        if line == "CLOSE":
            break
        if line == "RESET":
            detector.reset()
            emit({"type": "reset"})
            continue
        audio = base64.b64decode(line)
        samples = np.frombuffer(audio, dtype="<i2")
        prediction = detector.predict(samples)
        score = float(prediction[model_name])
        emit({"type": "prediction", "score": score, "detected": score > cutoff})
    except Exception as error:
        emit({"type": "error", "error": str(error)})
for interpreter in detector.models.values():
    if hasattr(interpreter, "close"):
        interpreter.close()
`
