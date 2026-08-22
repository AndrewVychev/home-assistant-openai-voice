package homeassistant

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestPerformRecoversWhenServiceReturns500ButStateChanged(t *testing.T) {
	var enabled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(response, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/states/light.kitchen":
			state := "off"
			if enabled.Load() {
				state = "on"
			}
			response.Header().Set("Content-Type", "application/json")
			_, _ = response.Write([]byte(`{"entity_id":"light.kitchen","state":"` + state + `","attributes":{"friendly_name":"Kitchen Main"}}`))
		case request.Method == http.MethodPost && request.URL.Path == "/api/services/light/turn_on":
			enabled.Store(true)
			http.Error(response, "integration timed out", http.StatusInternalServerError)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	result, err := New(server.URL, "test-token").Perform(t.Context(), "light.kitchen", "turn_on", 0)
	if err != nil {
		t.Fatalf("expected changed state to recover service error: %v", err)
	}
	if !result.OK || !result.Confirmed || result.State != "on" || result.RecoveredFromStatus != 500 {
		t.Fatalf("unexpected result: %#v", result)
	}
}
