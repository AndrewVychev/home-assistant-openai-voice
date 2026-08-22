package satellite

import (
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	captureRate  = 16_000
	playbackRate = 24_000
	pcmBytes     = 2
)

type Audio struct {
	context  *malgo.AllocatedContext
	capture  *malgo.Device
	playback *malgo.Device
	input    chan []byte
	queue    *pcmQueue
	enabled  atomic.Bool
	closed   atomic.Bool
}

func NewAudio() (*Audio, error) {
	audio := &Audio{
		input: make(chan []byte, 64),
		queue: newPCMQueue(playbackRate * pcmBytes * 5),
	}
	context, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, err
	}
	audio.context = context

	playbackConfig := malgo.DefaultDeviceConfig(malgo.Playback)
	playbackConfig.Playback.Format = malgo.FormatS16
	playbackConfig.Playback.Channels = 1
	playbackConfig.SampleRate = playbackRate
	playbackConfig.PeriodSizeInMilliseconds = 20
	playback, err := malgo.InitDevice(context.Context, playbackConfig, malgo.DeviceCallbacks{
		Data: func(output, _ []byte, _ uint32) {
			audio.queue.readInto(output)
		},
	})
	if err != nil {
		audio.releaseContext()
		return nil, err
	}
	audio.playback = playback

	captureConfig := malgo.DefaultDeviceConfig(malgo.Capture)
	captureConfig.Capture.Format = malgo.FormatS16
	captureConfig.Capture.Channels = 1
	captureConfig.SampleRate = captureRate
	captureConfig.PeriodSizeInMilliseconds = 20
	capture, err := malgo.InitDevice(context.Context, captureConfig, malgo.DeviceCallbacks{
		Data: func(_, input []byte, _ uint32) {
			if !audio.enabled.Load() || audio.closed.Load() || len(input) == 0 {
				return
			}
			chunk := append([]byte(nil), input...)
			select {
			case audio.input <- chunk:
			default:
			}
		},
	})
	if err != nil {
		playback.Uninit()
		audio.releaseContext()
		return nil, err
	}
	audio.capture = capture
	return audio, nil
}

func (audio *Audio) Start() error {
	if audio.playback == nil || audio.capture == nil {
		return errors.New("аудиоустройства не инициализированы")
	}
	if err := audio.playback.Start(); err != nil {
		return err
	}
	if err := audio.capture.Start(); err != nil {
		_ = audio.playback.Stop()
		return err
	}
	audio.enabled.Store(true)
	return nil
}

func (audio *Audio) Input() <-chan []byte {
	return audio.input
}

func (audio *Audio) Listen(enabled bool) {
	audio.enabled.Store(enabled)
}

func (audio *Audio) Play(data []byte) {
	audio.queue.write(data)
}

func (audio *Audio) PlayWakeChime() {
	const duration = 280 * time.Millisecond
	samples := int(float64(playbackRate) * duration.Seconds())
	data := make([]byte, samples*pcmBytes)
	for index := 0; index < samples; index++ {
		frequency := 660.0
		if index > samples/2 {
			frequency = 880
		}
		envelope := math.Sin(math.Pi * float64(index) / float64(samples))
		value := int16(math.Sin(2*math.Pi*frequency*float64(index)/playbackRate) * envelope * 3500)
		binary.LittleEndian.PutUint16(data[index*2:], uint16(value))
	}
	audio.Play(data)
}

func (audio *Audio) WaitPlayback(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for !audio.queue.empty() && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
}

func (audio *Audio) Close() {
	if audio == nil || audio.closed.Swap(true) {
		return
	}
	audio.enabled.Store(false)
	if audio.capture != nil {
		_ = audio.capture.Stop()
		audio.capture.Uninit()
	}
	if audio.playback != nil {
		_ = audio.playback.Stop()
		audio.playback.Uninit()
	}
	audio.releaseContext()
}

func (audio *Audio) releaseContext() {
	if audio.context != nil {
		_ = audio.context.Uninit()
		audio.context.Free()
		audio.context = nil
	}
}

type pcmQueue struct {
	mu   sync.Mutex
	data []byte
	max  int
}

func newPCMQueue(max int) *pcmQueue {
	return &pcmQueue{max: max}
}

func (queue *pcmQueue) write(data []byte) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(data) >= queue.max {
		queue.data = append(queue.data[:0], data[len(data)-queue.max:]...)
		return
	}
	if overflow := len(queue.data) + len(data) - queue.max; overflow > 0 {
		copy(queue.data, queue.data[overflow:])
		queue.data = queue.data[:len(queue.data)-overflow]
	}
	queue.data = append(queue.data, data...)
}

func (queue *pcmQueue) readInto(output []byte) {
	clear(output)
	queue.mu.Lock()
	defer queue.mu.Unlock()
	count := min(len(output), len(queue.data))
	copy(output, queue.data[:count])
	copy(queue.data, queue.data[count:])
	queue.data = queue.data[:len(queue.data)-count]
}

func (queue *pcmQueue) empty() bool {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	return len(queue.data) == 0
}
