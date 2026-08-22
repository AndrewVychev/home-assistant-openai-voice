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
