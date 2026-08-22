package satellite

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestPCMPeakDBFS(t *testing.T) {
	if got := pcmPeakDBFS(make([]byte, 8)); got != -96 {
		t.Fatalf("silence = %v dBFS, want -96", got)
	}
	data := make([]byte, 2)
	binary.LittleEndian.PutUint16(data, uint16(int16(16384)))
	if got := pcmPeakDBFS(data); math.Abs(got-(-6.0206)) > 0.001 {
		t.Fatalf("half scale = %v dBFS, want about -6.02", got)
	}
}
