package guard

import (
	"errors"
	"fmt"
	"math"
	"regexp"
)

var entityPattern = regexp.MustCompile(`^[a-z_]+\.[a-z0-9_]+$`)

var safeActions = map[string]map[string]bool{
	"light":   {"get_state": true, "turn_on": true, "turn_off": true, "toggle": true},
	"switch":  {"get_state": true, "turn_on": true, "turn_off": true, "toggle": true},
	"climate": {"get_state": true, "turn_on": true, "turn_off": true, "set_temperature": true},
}

type Action struct {
	EntityID    string
	Domain      string
	Name        string
	Temperature float64
}

type State struct {
	Value       string
	Temperature float64
}

func Validate(entityID, action string, temperature float64) (Action, error) {
	if !entityPattern.MatchString(entityID) {
		return Action{}, errors.New("некорректный entity_id")
	}
	domain := entityID[:indexDot(entityID)]
	if !safeActions[domain][action] {
		return Action{}, fmt.Errorf("действие %s запрещено для %s", action, domain)
	}
	if action == "set_temperature" && (temperature < 10 || temperature > 30) {
		return Action{}, errors.New("допустимая температура: от 10 до 30 °C")
	}
	return Action{EntityID: entityID, Domain: domain, Name: action, Temperature: temperature}, nil
}

func Matches(action Action, current State, previous State) bool {
	if current.Value == "" || current.Value == "unknown" || current.Value == "unavailable" {
		return false
	}
	switch action.Name {
	case "turn_on":
		if action.Domain == "climate" {
			return current.Value != "off"
		}
		return current.Value == "on"
	case "turn_off":
		return current.Value == "off"
	case "set_temperature":
		return math.Abs(current.Temperature-action.Temperature) < 0.1
	case "toggle":
		if previous.Value == "on" {
			return current.Value == "off"
		}
		return current.Value == "on"
	default:
		return false
	}
}

func indexDot(value string) int {
	for index, char := range value {
		if char == '.' {
			return index
		}
	}
	return len(value)
}
