package satellite

import "testing"

func TestAppendPCMPreRollKeepsNewestAudio(t *testing.T) {
	buffer := appendPCMPreRoll(nil, []byte{1, 2, 3}, 5)
	buffer = appendPCMPreRoll(buffer, []byte{4, 5, 6}, 5)
	want := []byte{2, 3, 4, 5, 6}
	if string(buffer) != string(want) {
		t.Fatalf("pre-roll = %v, want %v", buffer, want)
	}
	buffer = appendPCMPreRoll(buffer, []byte{7, 8, 9, 10, 11, 12}, 5)
	want = []byte{8, 9, 10, 11, 12}
	if string(buffer) != string(want) {
		t.Fatalf("pre-roll after large chunk = %v, want %v", buffer, want)
	}
}

func TestRequestsClarification(t *testing.T) {
	tests := []struct {
		response string
		want     bool
	}{
		{response: "В какой комнате?", want: true},
		{response: "Уточни комнату.", want: true},
		{response: "Какой свет включить", want: true},
		{response: "Отлично.", want: false},
		{response: "Свет включён.", want: false},
		{response: "", want: false},
	}
	for _, test := range tests {
		if got := requestsClarification(test.response); got != test.want {
			t.Errorf("requestsClarification(%q) = %v, want %v", test.response, got, test.want)
		}
	}
}
