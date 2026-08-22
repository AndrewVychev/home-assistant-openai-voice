package satellite

import "testing"

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
