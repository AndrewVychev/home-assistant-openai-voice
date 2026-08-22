package satellite

import (
	"bytes"
	"testing"
)

func TestPCMQueueReadsAndPadsWithSilence(t *testing.T) {
	queue := newPCMQueue(8)
	queue.write([]byte{1, 2, 3})
	output := []byte{9, 9, 9, 9, 9}
	queue.readInto(output)
	if !bytes.Equal(output, []byte{1, 2, 3, 0, 0}) {
		t.Fatalf("unexpected playback: %v", output)
	}
	if !queue.empty() {
		t.Fatal("queue should be empty")
	}
}

func TestPCMQueueDropsOldestAudioOnOverflow(t *testing.T) {
	queue := newPCMQueue(4)
	queue.write([]byte{1, 2, 3})
	queue.write([]byte{4, 5, 6})
	output := make([]byte, 4)
	queue.readInto(output)
	if !bytes.Equal(output, []byte{3, 4, 5, 6}) {
		t.Fatalf("unexpected bounded playback: %v", output)
	}
}
