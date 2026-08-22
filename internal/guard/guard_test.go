package guard

import "testing"

func TestValidateSafeLight(t *testing.T) {
	if _, err := Validate("light.kitchen", "turn_on", 0); err != nil {
		t.Fatalf("expected light control to pass: %v", err)
	}
}

func TestValidateRejectsLock(t *testing.T) {
	if _, err := Validate("lock.front_door", "turn_on", 0); err == nil {
		t.Fatal("expected lock control to be rejected")
	}
}

func TestValidateTemperature(t *testing.T) {
	if _, err := Validate("climate.living_room", "set_temperature", 31); err == nil {
		t.Fatal("expected unsafe temperature to be rejected")
	}
}

func TestClimateTurnOnMatchesActiveMode(t *testing.T) {
	action, _ := Validate("climate.living_room", "turn_on", 0)
	if !Matches(action, State{Value: "fan_only"}, State{Value: "off"}) {
		t.Fatal("fan_only should confirm climate turn_on")
	}
	if Matches(action, State{Value: "off"}, State{Value: "off"}) {
		t.Fatal("off should not confirm climate turn_on")
	}
}

func TestValidateTranscriptAction(t *testing.T) {
	tests := []struct {
		name       string
		transcript string
		action     string
		wantError  bool
	}{
		{name: "turn on", transcript: "включи кондиционер", action: "turn_on"},
		{name: "turn off", transcript: "выключи кондиционер", action: "turn_off"},
		{name: "disconnect means off", transcript: "отключи кондиционер", action: "turn_off"},
		{name: "opposite on", transcript: "выключи кондиционер", action: "turn_on", wantError: true},
		{name: "opposite off", transcript: "включи кондиционер", action: "turn_off", wantError: true},
		{name: "foreign transcript", transcript: "Eu queria um ar-condicionado", action: "turn_on", wantError: true},
		{name: "missing transcript", action: "turn_off", wantError: true},
		{name: "negated", transcript: "не включай кондиционер", action: "turn_on", wantError: true},
		{name: "ambiguous", transcript: "не включай, а выключи кондиционер", action: "turn_off", wantError: true},
		{name: "temperature unchanged", transcript: "поставь 22 градуса", action: "set_temperature"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateTranscriptAction(test.transcript, test.action)
			if (err != nil) != test.wantError {
				t.Fatalf("ValidateTranscriptAction() error = %v, wantError %v", err, test.wantError)
			}
		})
	}
}
