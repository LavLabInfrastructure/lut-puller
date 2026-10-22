package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadVariableConfigs(t *testing.T) {
	tempDir := t.TempDir()
	first := filepath.Join(tempDir, "first.yaml")
	second := filepath.Join(tempDir, "second.yaml")
	config := `widget:
  tenant-id: tenant
  client-id: client
  client-secret: secret
  scope: scope/.default
  username: user
  password: pass
  graph-drive-id: drive
  graph-file-id: file
  graph-worksheet-id: worksheet
`
	if err := os.WriteFile(first, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	configs, err := loadVariableConfigs([]string{first})
	if err != nil {
		t.Fatal(err)
	}
	got := configs["widget"]
	if got.TenantID != "tenant" || got.KeyCol != "key" || got.ValCol != "val" || got.OutputPath != "" {
		t.Fatalf("unexpected loaded config: %+v", got)
	}

	if _, err := loadVariableConfigs([]string{first, second}); err != nil {
		t.Fatalf("identical definitions should be accepted: %v", err)
	}
	conflict := strings.Replace(config, "graph-file-id: file", "graph-file-id: other", 1)
	if err := os.WriteFile(second, []byte(conflict), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVariableConfigs([]string{first, second}); err == nil || !strings.Contains(err.Error(), "conflicting definitions") {
		t.Fatalf("expected conflicting definitions error, got %v", err)
	}
}

func TestLoadVariableConfigsRejectsMissingField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("widget:\n  client-id: client\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVariableConfigs([]string{path}); err == nil || !strings.Contains(err.Error(), "missing required field") {
		t.Fatalf("expected missing field error, got %v", err)
	}
}

func TestBuildLUT(t *testing.T) {
	values := [][]any{
		{"E number", "Brain Bank Number"},
		{" 123 ", " 456 "},
		{"", "ignored"},
		{"789", ""},
		{"incomplete"},
	}
	got, err := buildLUT("brain", values, "E number", "Brain Bank Number")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["brain/123"] != "456" {
		t.Fatalf("unexpected LUT: %#v", got)
	}
}

func TestSaveLUT(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "lookup")
	got, err := saveLUT(map[string]string{"b": "2", "a": "1"}, path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path+".txt" {
		t.Fatalf("expected .txt extension, got %q", got)
	}
	contents, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "a=1\nb=2\n" {
		t.Fatalf("unexpected output: %q", contents)
	}
}

func TestGraphPullerRequestFlow(t *testing.T) {
	var tokenRequests, graphRequests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tenant/oauth2/v2.0/token":
			tokenRequests++
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse token form: %v", err)
			}
			if r.Form.Get("grant_type") != grantType || r.Form.Get("username") != "user" {
				t.Errorf("unexpected token form: %v", r.Form)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"token","expires_in":3600}`))
		case "/v1.0/drives/drive/items/file/workbook/worksheets/sheet/usedRange":
			graphRequests++
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Errorf("unexpected authorization header: %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"values":[["key","val"],["k","v"]]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := variableConfig{
		TenantID: "tenant", ClientID: "client", ClientSecret: "secret", Scope: "scope",
		Username: "user", Password: "pass", GraphDriveID: "drive", GraphFileID: "file",
		GraphWorksheetID: "sheet", IDPURL: server.URL, TokenEndpoint: "/oauth2/v2.0/token",
		GraphRootURL: server.URL, GraphRootEndpoint: "/v1.0",
	}
	puller := graphLutPuller{config: cfg, httpClient: server.Client()}
	first, err := puller.usedRangeValues()
	if err != nil {
		t.Fatal(err)
	}
	second, err := puller.usedRangeValues()
	if err != nil {
		t.Fatal(err)
	}
	if tokenRequests != 1 || graphRequests != 2 {
		t.Fatalf("expected one cached token and two Graph requests, got token=%d graph=%d", tokenRequests, graphRequests)
	}
	if len(first) != 2 || len(second) != 2 || time.Until(puller.tokenExpires) <= 0 {
		t.Fatalf("unexpected response or token expiry: first=%v second=%v expiry=%v", first, second, puller.tokenExpires)
	}
}
