package organizations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/itsMinar/team-flow/internal/api"
	"github.com/itsMinar/team-flow/internal/auth"
	"github.com/itsMinar/team-flow/internal/config"
	"github.com/itsMinar/team-flow/internal/health"
	"github.com/itsMinar/team-flow/internal/organizations"
	"github.com/itsMinar/team-flow/internal/projects"
	"github.com/itsMinar/team-flow/internal/tasks"
	"github.com/itsMinar/team-flow/internal/teams"
)

// newTaskTestServer wires the real router with database-backed services so the
// task routes are exercised end to end, including authentication, the JSON
// envelope, and error mapping.
func newTaskTestServer(t *testing.T) (*httptest.Server, *pgxpool.Pool) {
	t.Helper()
	pool := testPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwt := auth.NewJWTService("http-test-secret", "teamflow", 15*time.Minute)
	orgSvc := organizations.NewService(pool, logger)
	cfg := &config.Config{}
	cfg.HTTP.MaxBodyBytes = 1 << 20

	router := api.NewRouter(api.Dependencies{
		Config:       cfg,
		Logger:       logger,
		Health:       health.NewHandler(logger, map[string]health.Checker{}),
		AuthHandler:  auth.NewHandler(auth.NewService(pool, jwt, 720*time.Hour, logger), logger),
		AuthMW:       auth.NewMiddleware(jwt, logger),
		OrgHandler:   organizations.NewHandler(orgSvc, logger),
		OrgMW:        organizations.NewMiddleware(orgSvc, logger),
		TeamsHandler: teams.NewHandler(teams.NewService(pool, orgSvc, logger), logger),
		Projects:     projects.NewHandler(projects.NewService(pool, orgSvc), logger),
		Tasks:        tasks.NewHandler(tasks.NewService(pool, orgSvc), logger),
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv, pool
}

type taskHTTPResponse struct {
	Data       json.RawMessage `json:"data"`
	Pagination struct {
		Total int64 `json:"total"`
	} `json:"pagination"`
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

type taskBody struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

func decodeTask(t *testing.T, resp taskHTTPResponse) taskBody {
	t.Helper()
	var body taskBody
	if err := json.Unmarshal(resp.Data, &body); err != nil {
		t.Fatalf("decode task %s: %v", resp.Data, err)
	}
	return body
}

func doTaskRequest(t *testing.T, srv *httptest.Server, method, path, token, body string) (int, taskHTTPResponse) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded taskHTTPResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, decoded
}

func TestTaskRoutesOverHTTP(t *testing.T) {
	srv, pool := newTaskTestServer(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orgSvc := organizations.NewService(pool, logger)

	// Register through the auth service so the access token is signed exactly
	// as production tokens are, then exercise the task endpoints over HTTP.
	jwt := auth.NewJWTService("http-test-secret", "teamflow", 15*time.Minute)
	registered, err := auth.NewService(pool, jwt, 720*time.Hour, logger).Register(ctx, auth.RegisterInput{
		Email: "http-task@example.com", Password: "StrongPassword123",
		FirstName: "Http", LastName: "Tester", OrganizationName: "HTTP Tasks Org",
	}, auth.RequestMeta{})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token := registered.Tokens.AccessToken
	mine, err := orgSvc.ListMyOrganizations(ctx, registered.User.ID)
	if err != nil || len(mine) == 0 {
		t.Fatalf("list organizations: %v", err)
	}
	orgID, userID := mine[0].ID, registered.User.ID

	project, err := projects.NewService(pool, orgSvc).Create(ctx, userID, orgID, projects.CreateInput{Name: "HTTP Project"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	// The API refuses task routes without a bearer token.
	base := "/api/v1/organizations/" + orgID.String()
	if status, _ := doTaskRequest(t, srv, http.MethodGet, base+"/tasks", "", ""); status != http.StatusUnauthorized {
		t.Fatalf("GET %s/tasks without a token = %d, want 401", base, status)
	}

	projectPath := base + "/projects/" + project.ID.String() + "/tasks"

	status, resp := doTaskRequest(t, srv, http.MethodPost, projectPath, token,
		`{"title":"Wire the task API","priority":"high","due_date":"2026-12-24"}`)
	created := decodeTask(t, resp)
	if status != http.StatusCreated || created.Title != "Wire the task API" || created.Status != "todo" {
		t.Fatalf("create task = %d, %+v", status, created)
	}

	status, listed := doTaskRequest(t, srv, http.MethodGet, projectPath+"?page_size=10", token, "")
	if status != http.StatusOK || listed.Pagination.Total != 1 {
		t.Fatalf("list tasks = %d, %+v", status, listed)
	}
	var items []taskBody
	if err := json.Unmarshal(listed.Data, &items); err != nil || len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("list tasks data = %s, %v", listed.Data, err)
	}

	status, updated := doTaskRequest(t, srv, http.MethodPatch, base+"/tasks/"+created.ID, token, `{"status":"in_progress"}`)
	if status != http.StatusOK || decodeTask(t, updated).Status != "in_progress" {
		t.Fatalf("update task = %d, %s", status, updated.Data)
	}

	status, activity := doTaskRequest(t, srv, http.MethodGet, base+"/tasks/"+created.ID+"/activity", token, "")
	if status != http.StatusOK || activity.Pagination.Total != 2 {
		t.Fatalf("task activity = %d, %+v", status, activity.Pagination)
	}

	// An unknown task in the caller's own organization is a safe 404.
	status, missing := doTaskRequest(t, srv, http.MethodGet, base+"/tasks/00000000-0000-0000-0000-000000000000", token, "")
	if status != http.StatusNotFound || missing.Error.Code != "TASK_NOT_FOUND" {
		t.Fatalf("missing task = %d, %+v", status, missing)
	}

	status, _ = doTaskRequest(t, srv, http.MethodDelete, base+"/tasks/"+created.ID, token, "")
	if status != http.StatusOK {
		t.Fatalf("delete task = %d", status)
	}
	status, _ = doTaskRequest(t, srv, http.MethodGet, base+"/tasks/"+created.ID, token, "")
	if status != http.StatusNotFound {
		t.Fatalf("get deleted task = %d, want 404", status)
	}
}
