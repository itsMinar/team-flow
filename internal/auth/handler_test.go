package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisterRejectsOversizedPassword(t *testing.T) {
	handler := NewHandler(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, password := range []string{strings.Repeat("a", 72) + "1", strings.Repeat("\u00e9", 36) + "1"} {
		body, err := json.Marshal(registerRequest{
			Email:            "owner@example.com",
			Password:         password,
			FirstName:        "Test",
			LastName:         "Owner",
			OrganizationName: "Example",
		})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewReader(body))
		response := httptest.NewRecorder()
		handler.Register(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", response.Code)
		}
		var payload struct {
			Error struct {
				Code    string            `json:"code"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Error.Code != "VALIDATION_ERROR" || payload.Error.Details["password"] == "" {
			t.Fatalf("expected password validation error, got %s", response.Body.String())
		}
	}
}
