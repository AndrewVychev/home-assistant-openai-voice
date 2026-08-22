package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"homevoice/internal/guard"
)

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

type Entity struct {
	EntityID string `json:"entity_id"`
	Name     string `json:"name"`
	Area     string `json:"area,omitempty"`
}

type Result struct {
	OK                  bool    `json:"ok"`
	EntityID            string  `json:"entityId"`
	Name                string  `json:"name"`
	Action              string  `json:"action,omitempty"`
	State               string  `json:"state"`
	Temperature         float64 `json:"temperature,omitempty"`
	TemperatureUnit     string  `json:"temperatureUnit,omitempty"`
	Humidity            float64 `json:"humidity,omitempty"`
	CloudCoverage       float64 `json:"cloudCoverage,omitempty"`
	WindSpeed           float64 `json:"windSpeed,omitempty"`
	WindSpeedUnit       string  `json:"windSpeedUnit,omitempty"`
	Confirmed           bool    `json:"confirmed,omitempty"`
	RecoveredFromStatus int     `json:"recoveredFromStatus,omitempty"`
}

type state struct {
	EntityID   string         `json:"entity_id"`
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes"`
}

type entityRegistryEntry struct {
	EntityID string `json:"entity_id"`
	AreaID   string `json:"area_id"`
	DeviceID string `json:"device_id"`
}

type deviceRegistryEntry struct {
	ID     string `json:"id"`
	AreaID string `json:"area_id"`
}

type areaRegistryEntry struct {
	AreaID string `json:"area_id"`
	Name   string `json:"name"`
}

func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (client *Client) Configured() bool {
	return client.token != ""
}

func (client *Client) Health(ctx context.Context) bool {
	status, _, err := client.request(ctx, http.MethodGet, "/api/", nil)
	return err == nil && status >= 200 && status < 300
}

func (client *Client) ControllableEntities(ctx context.Context) ([]Entity, error) {
	status, body, err := client.request(ctx, http.MethodGet, "/api/states", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("Home Assistant states ответил %d", status)
	}
	var states []state
	if err := json.Unmarshal(body, &states); err != nil {
		return nil, fmt.Errorf("decode Home Assistant states: %w", err)
	}

	areas, err := client.areaLookup(ctx)
	if err != nil {
		areas = map[string]string{}
	}
	entities := make([]Entity, 0, len(states))
	for _, item := range states {
		domain, _, _ := strings.Cut(item.EntityID, ".")
		if domain != "light" && domain != "switch" && domain != "climate" && domain != "weather" {
			continue
		}
		if strings.HasPrefix(item.EntityID, "switch.shelly") {
			continue
		}
		name, _ := item.Attributes["friendly_name"].(string)
		if name == "" {
			name = item.EntityID
		}
		entities = append(entities, Entity{EntityID: item.EntityID, Name: name, Area: areas[item.EntityID]})
	}
	sort.Slice(entities, func(i, j int) bool { return entities[i].EntityID < entities[j].EntityID })
	if len(entities) > 100 {
		entities = entities[:100]
	}
	return entities, nil
}

func (client *Client) Perform(ctx context.Context, entityID, actionName string, temperature float64) (Result, error) {
	action, err := guard.Validate(entityID, actionName, temperature)
	if err != nil {
		return Result{}, err
	}
	current, err := client.readState(ctx, entityID)
	if err != nil {
		return Result{}, err
	}
	if actionName == "get_state" {
		return resultFromState(current, actionName, false, 0), nil
	}

	serviceData := map[string]any{"entity_id": entityID}
	if actionName == "set_temperature" {
		serviceData["temperature"] = temperature
	}
	status, serviceBody, requestErr := client.request(
		ctx,
		http.MethodPost,
		fmt.Sprintf("/api/services/%s/%s", action.Domain, action.Name),
		serviceData,
	)

	latest := current
	confirmed := false
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			case <-time.After(400 * time.Millisecond):
			}
		}
		if next, stateErr := client.readState(ctx, entityID); stateErr == nil {
			latest = next
		}
		confirmed = guard.Matches(action, guardState(latest), guardState(current))
		if confirmed {
			break
		}
	}

	if (requestErr != nil || status < 200 || status >= 300) && !confirmed {
		if requestErr != nil {
			return Result{}, requestErr
		}
		return Result{}, fmt.Errorf("Home Assistant ответил %d: %s", status, truncate(string(serviceBody), 300))
	}
	recovered := 0
	if status < 200 || status >= 300 {
		recovered = status
	}
	return resultFromState(latest, actionName, confirmed, recovered), nil
}

func (client *Client) readState(ctx context.Context, entityID string) (state, error) {
	status, body, err := client.request(ctx, http.MethodGet, "/api/states/"+url.PathEscape(entityID), nil)
	if err != nil {
		return state{}, err
	}
	if status < 200 || status >= 300 {
		return state{}, fmt.Errorf("Home Assistant state ответил %d", status)
	}
	var value state
	if err := json.Unmarshal(body, &value); err != nil {
		return state{}, err
	}
	return value, nil
}

func (client *Client) request(ctx context.Context, method, pathname string, payload any) (int, []byte, error) {
	if client.token == "" {
		return 0, nil, errors.New("добавьте HA_TOKEN в .env")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+pathname, body)
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Authorization", "Bearer "+client.token)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	return response.StatusCode, data, err
}

func (client *Client) areaLookup(ctx context.Context) (map[string]string, error) {
	websocketURL, err := url.Parse(client.baseURL)
	if err != nil {
		return nil, err
	}
	if websocketURL.Scheme == "https" {
		websocketURL.Scheme = "wss"
	} else {
		websocketURL.Scheme = "ws"
	}
	websocketURL.Path = "/api/websocket"

	connection, _, err := websocket.Dial(ctx, websocketURL.String(), nil)
	if err != nil {
		return nil, err
	}
	defer connection.Close(websocket.StatusNormalClosure, "done")

	var hello struct {
		Type string `json:"type"`
	}
	if err := wsjson.Read(ctx, connection, &hello); err != nil {
		return nil, err
	}
	if hello.Type != "auth_required" {
		return nil, fmt.Errorf("unexpected Home Assistant websocket greeting: %s", hello.Type)
	}
	if err := wsjson.Write(ctx, connection, map[string]any{"type": "auth", "access_token": client.token}); err != nil {
		return nil, err
	}
	var auth struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if err := wsjson.Read(ctx, connection, &auth); err != nil {
		return nil, err
	}
	if auth.Type != "auth_ok" {
		return nil, fmt.Errorf("Home Assistant websocket auth failed: %s", auth.Message)
	}

	requests := []map[string]any{
		{"id": 1, "type": "config/entity_registry/list"},
		{"id": 2, "type": "config/device_registry/list"},
		{"id": 3, "type": "config/area_registry/list"},
	}
	for _, request := range requests {
		if err := wsjson.Write(ctx, connection, request); err != nil {
			return nil, err
		}
	}

	var entities []entityRegistryEntry
	var devices []deviceRegistryEntry
	var areas []areaRegistryEntry
	for received := 0; received < 3; received++ {
		var response struct {
			ID      int             `json:"id"`
			Success bool            `json:"success"`
			Result  json.RawMessage `json:"result"`
		}
		if err := wsjson.Read(ctx, connection, &response); err != nil {
			return nil, err
		}
		if !response.Success {
			return nil, fmt.Errorf("Home Assistant registry %d failed", response.ID)
		}
		switch response.ID {
		case 1:
			err = json.Unmarshal(response.Result, &entities)
		case 2:
			err = json.Unmarshal(response.Result, &devices)
		case 3:
			err = json.Unmarshal(response.Result, &areas)
		}
		if err != nil {
			return nil, err
		}
	}

	deviceAreas := make(map[string]string, len(devices))
	for _, device := range devices {
		deviceAreas[device.ID] = device.AreaID
	}
	areaNames := make(map[string]string, len(areas))
	for _, area := range areas {
		areaNames[area.AreaID] = area.Name
	}
	lookup := make(map[string]string, len(entities))
	for _, entity := range entities {
		areaID := entity.AreaID
		if areaID == "" {
			areaID = deviceAreas[entity.DeviceID]
		}
		lookup[entity.EntityID] = areaNames[areaID]
	}
	return lookup, nil
}

func resultFromState(value state, action string, confirmed bool, recovered int) Result {
	name, _ := value.Attributes["friendly_name"].(string)
	if name == "" {
		name = value.EntityID
	}
	temperature := number(value.Attributes["current_temperature"])
	if _, exists := value.Attributes["current_temperature"]; !exists {
		temperature = number(value.Attributes["temperature"])
	}
	return Result{
		OK:                  true,
		EntityID:            value.EntityID,
		Name:                name,
		Action:              action,
		State:               value.State,
		Temperature:         temperature,
		TemperatureUnit:     stringValue(value.Attributes["temperature_unit"]),
		Humidity:            number(value.Attributes["humidity"]),
		CloudCoverage:       number(value.Attributes["cloud_coverage"]),
		WindSpeed:           number(value.Attributes["wind_speed"]),
		WindSpeedUnit:       stringValue(value.Attributes["wind_speed_unit"]),
		Confirmed:           confirmed,
		RecoveredFromStatus: recovered,
	}
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func guardState(value state) guard.State {
	return guard.State{Value: value.State, Temperature: number(value.Attributes["temperature"])}
}

func number(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case json.Number:
		result, _ := typed.Float64()
		return result
	default:
		return 0
	}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
