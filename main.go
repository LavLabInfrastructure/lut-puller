package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	idpURL            = "https://login.microsoftonline.com"
	tokenEndpoint     = "/oauth2/v2.0/token"
	graphRootURL      = "https://graph.microsoft.com"
	graphRootEndpoint = "/v1.0"
	defaultOutputPath = "./lut.txt"
	grantType         = "client_credentials"
	requestTimeout    = 10 * time.Second
)

type variableConfig struct {
	TenantID          string `yaml:"tenant-id"`
	ClientID          string `yaml:"client-id"`
	ClientSecret      string `yaml:"client-secret"`
	Scope             string `yaml:"scope"`
	Username          string `yaml:"username"`
	Password          string `yaml:"password"`
	GraphDriveID      string `yaml:"graph-drive-id"`
	GraphFileID       string `yaml:"graph-file-id"`
	GraphWorksheetID  string `yaml:"graph-worksheet-id"`
	KeyCol            any    `yaml:"key-col"`
	ValCol            any    `yaml:"val-col"`
	IDPURL            string `yaml:"idp-url"`
	TokenEndpoint     string `yaml:"token-endpoint"`
	GraphRootURL      string `yaml:"graph-root-url"`
	GraphRootEndpoint string `yaml:"graph-root-endpoint"`
	OutputPath        string `yaml:"output-path"`
}

type graphLutPuller struct {
	config       variableConfig
	httpClient   *http.Client
	accessToken  string
	tokenExpires time.Time
}

func normalizeKey(key string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
}

func normalizeConfig(raw map[string]any, variable, source string) (variableConfig, error) {
	values := make(map[string]any, len(raw))
	for key, value := range raw {
		values[normalizeKey(key)] = value
	}
	allowed := map[string]bool{
		"tenant_id": true, "client_id": true, "client_secret": true, "scope": true,
		"username": true, "password": true, "graph_drive_id": true, "graph_file_id": true,
		"graph_worksheet_id": true, "key_col": true, "val_col": true, "idp_url": true,
		"token_endpoint": true, "graph_root_url": true, "graph_root_endpoint": true,
		"output_path": true,
	}
	unknown := make([]string, 0)
	for key := range values {
		if !allowed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		log.Printf("warning: unknown field(s) %v for variable %q in %s; ignoring", unknown, variable, source)
	}

	var cfg variableConfig
	required := []struct {
		key string
		dst *string
	}{
		{"tenant_id", &cfg.TenantID}, {"client_id", &cfg.ClientID},
		{"client_secret", &cfg.ClientSecret}, {"scope", &cfg.Scope},
		{"username", &cfg.Username}, {"password", &cfg.Password},
		{"graph_drive_id", &cfg.GraphDriveID}, {"graph_file_id", &cfg.GraphFileID},
		{"graph_worksheet_id", &cfg.GraphWorksheetID},
	}
	for _, field := range required {
		value, exists := values[field.key]
		if !exists || value == nil {
			return cfg, fmt.Errorf("missing required field %q for variable %q in %s", strings.ReplaceAll(field.key, "_", "-"), variable, source)
		}
		text, err := scalarString(value)
		if err != nil {
			return cfg, fmt.Errorf("field %q for variable %q in %s must be a string or number: %w", strings.ReplaceAll(field.key, "_", "-"), variable, source, err)
		}
		if strings.TrimSpace(text) == "" {
			return cfg, fmt.Errorf("field %q for variable %q in %s cannot be empty", strings.ReplaceAll(field.key, "_", "-"), variable, source)
		}
		*field.dst = strings.TrimSpace(text)
	}

	cfg.KeyCol = "key"
	cfg.ValCol = "val"
	optional := []struct {
		key string
		dst *string
	}{
		{"idp_url", &cfg.IDPURL}, {"token_endpoint", &cfg.TokenEndpoint},
		{"graph_root_url", &cfg.GraphRootURL}, {"graph_root_endpoint", &cfg.GraphRootEndpoint},
		{"output_path", &cfg.OutputPath},
	}
	defaults := map[string]string{
		"idp_url": idpURL, "token_endpoint": tokenEndpoint,
		"graph_root_url": graphRootURL, "graph_root_endpoint": graphRootEndpoint,
	}
	for _, field := range optional {
		value, present := values[field.key]
		if !present || value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
			*field.dst = defaults[field.key]
			continue
		}
		text, err := scalarString(value)
		if err != nil {
			return cfg, fmt.Errorf("field %q for variable %q in %s must be a scalar: %w", strings.ReplaceAll(field.key, "_", "-"), variable, source, err)
		}
		*field.dst = strings.TrimSpace(text)
	}
	for _, field := range []struct {
		key string
		dst *any
	}{{"key_col", &cfg.KeyCol}, {"val_col", &cfg.ValCol}} {
		if value, ok := values[field.key]; ok && value != nil {
			if text, isString := value.(string); isString {
				if strings.TrimSpace(text) == "" {
					continue
				}
				*field.dst = strings.TrimSpace(text)
			} else if _, err := scalarString(value); err == nil {
				*field.dst = value
			} else {
				return cfg, fmt.Errorf("field %q for variable %q in %s must be a string or integer", strings.ReplaceAll(field.key, "_", "-"), variable, source)
			}
		}
	}
	return cfg, nil
}

func scalarString(value any) (string, error) {
	switch value := value.(type) {
	case string:
		return value, nil
	case int:
		return strconv.Itoa(value), nil
	case int64:
		return strconv.FormatInt(value, 10), nil
	case uint64:
		return strconv.FormatUint(value, 10), nil
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), nil
	case bool:
		return strconv.FormatBool(value), nil
	default:
		return "", fmt.Errorf("got %T", value)
	}
}

func loadVariableConfigs(paths []string) (map[string]variableConfig, error) {
	if len(paths) == 0 {
		return nil, errors.New("at least one YAML configuration file must be provided")
	}
	configs := make(map[string]variableConfig)
	origins := make(map[string]string)
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("configuration file not found: %s", path)
			}
			return nil, fmt.Errorf("reading configuration %s: %w", path, err)
		}
		var data map[string]any
		if err := yaml.Unmarshal(contents, &data); err != nil {
			return nil, fmt.Errorf("parsing YAML %s: %w", path, err)
		}
		if data == nil {
			continue
		}
		for name, raw := range data {
			mapping, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("configuration for variable %q in %s must be a mapping", name, path)
			}
			cfg, err := normalizeConfig(mapping, name, path)
			if err != nil {
				return nil, err
			}
			if existing, exists := configs[name]; exists {
				if existing == cfg {
					log.Printf("warning: variable %q defined multiple times (e.g. in %s and %s) with identical settings; using the first definition", name, origins[name], path)
					continue
				}
				return nil, fmt.Errorf("conflicting definitions for variable %q between %s and %s", name, origins[name], path)
			}
			configs[name] = cfg
			origins[name] = path
		}
	}
	if len(configs) == 0 {
		return nil, errors.New("no variable configurations found in the provided YAML file(s)")
	}
	return configs, nil
}

func columnIndex(column any, header []any) (int, error) {
	switch value := column.(type) {
	case int:
		return value, nil
	case int64:
		return int(value), nil
	case float64:
		return int(value), nil
	case string:
		for index, cell := range header {
			if fmt.Sprint(cell) == value {
				return index, nil
			}
		}
		return 0, fmt.Errorf("column %q not found in header: %v", value, header)
	default:
		return 0, fmt.Errorf("column selector must be a string or integer, got %T", column)
	}
}

func buildLUT(variable string, values [][]any, keyColumn, valueColumn any) (map[string]string, error) {
	lut := make(map[string]string)
	if len(values) == 0 {
		return lut, nil
	}
	keyIndex, err := columnIndex(keyColumn, values[0])
	if err != nil {
		return nil, err
	}
	valueIndex, err := columnIndex(valueColumn, values[0])
	if err != nil {
		return nil, err
	}
	for _, row := range values[1:] {
		if keyIndex < 0 || valueIndex < 0 {
			return nil, errors.New("column indices cannot be negative")
		}
		if keyIndex >= len(row) || valueIndex >= len(row) || row[keyIndex] == nil || row[valueIndex] == nil {
			continue
		}
		key := strings.TrimSpace(fmt.Sprint(row[keyIndex]))
		value := strings.TrimSpace(fmt.Sprint(row[valueIndex]))
		if key != "" && value != "" {
			lut[variable+"/"+key] = value
		}
	}
	return lut, nil
}

func (p *graphLutPuller) worksheetURL() (string, error) {
	base, err := url.Parse(strings.TrimRight(p.config.GraphRootURL, "/"))
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(p.config.GraphRootEndpoint, "/") + "/drives/" + p.config.GraphDriveID + "/items/" + p.config.GraphFileID + "/workbook/worksheets/" + p.config.GraphWorksheetID
	base.Path = strings.TrimRight(base.Path, "/") + path
	return base.String(), nil
}

func (p *graphLutPuller) getAccessToken() (string, error) {
	if p.accessToken != "" && time.Now().Before(p.tokenExpires) {
		return p.accessToken, nil
	}
	tokenURL, err := url.JoinPath(p.config.IDPURL, p.config.TenantID, p.config.TokenEndpoint)
	if err != nil {
		return "", fmt.Errorf("building token URL: %w", err)
	}
	form := url.Values{
		"grant_type": {grantType}, "scope": {p.config.Scope},
		"client_id": {p.config.ClientID}, "client_secret": {p.config.ClientSecret},
		"username": {p.config.Username}, "password": {p.config.Password},
	}
	req, err := http.NewRequest(http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting access token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("token request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if result.AccessToken == "" {
		return "", errors.New("token response did not contain access_token")
	}
	if result.ExpiresIn == 0 {
		result.ExpiresIn = 3600
	}
	p.accessToken = result.AccessToken
	p.tokenExpires = time.Now().Add(time.Duration(result.ExpiresIn-60) * time.Second)
	return p.accessToken, nil
}

func (p *graphLutPuller) usedRangeValues() ([][]any, error) {
	token, err := p.getAccessToken()
	if err != nil {
		return nil, err
	}
	worksheet, err := p.worksheetURL()
	if err != nil {
		return nil, fmt.Errorf("building worksheet URL: %w", err)
	}
	requestURL := worksheet + "/usedRange?$select=values"
	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("requesting worksheet used range: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("Graph request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		Values [][]any `json:"values"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding worksheet response: %w", err)
	}
	if result.Values == nil {
		return nil, errors.New("Graph response did not contain 'values' in usedRange")
	}
	return result.Values, nil
}

func saveLUT(lut map[string]string, path string) (string, error) {
	if path == "" {
		path = defaultOutputPath
	}
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case "":
		path += ".txt"
	case ".txt", ".properties", ".lut":
	default:
		log.Printf("warning: provided output extension %q is unsupported; saving as .txt instead", extension)
		path = strings.TrimSuffix(path, filepath.Ext(path)) + ".txt"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("creating output directory: %w", err)
	}
	keys := make([]string, 0, len(lut))
	for key := range lut {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output bytes.Buffer
	for _, key := range keys {
		fmt.Fprintf(&output, "%s=%s\n", key, lut[key])
	}
	if err := os.WriteFile(path, output.Bytes(), 0o666); err != nil {
		return "", fmt.Errorf("writing LUT: %w", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		return "", fmt.Errorf("setting output permissions: %w", err)
	}
	return path, nil
}

func run(paths []string, client *http.Client) error {
	configs, err := loadVariableConfigs(paths)
	if err != nil {
		return err
	}
	grouped := make(map[string][]string)
	for name, cfg := range configs {
		path := cfg.OutputPath
		if path == "" {
			path = defaultOutputPath
		}
		grouped[path] = append(grouped[path], name)
	}
	outputPaths := make([]string, 0, len(grouped))
	for path := range grouped {
		outputPaths = append(outputPaths, path)
	}
	sort.Strings(outputPaths)
	for _, outputPath := range outputPaths {
		combined := make(map[string]string, 1601)
		for i := 400; i <= 2000; i++ {
			key := "Prostate_G3_" + strconv.Itoa(i)
			combined[key] = strconv.Itoa(i)
		}
		names := grouped[outputPath]
		sort.Strings(names)
		for _, name := range names {
			cfg := configs[name]
			puller := graphLutPuller{config: cfg, httpClient: client}
			values, err := puller.usedRangeValues()
			if err != nil {
				return fmt.Errorf("pulling variable %q: %w", name, err)
			}
			piece, err := buildLUT(name, values, cfg.KeyCol, cfg.ValCol)
			if err != nil {
				return fmt.Errorf("building LUT for variable %q: %w", name, err)
			}
			for key, value := range piece {
				combined[key] = value
			}
		}
		path, err := saveLUT(combined, outputPath)
		if err != nil {
			return err
		}
		fmt.Printf("Wrote LUT to: %s\n", path)
	}
	return nil
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s <config.yaml> [config.yaml ...]\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	client := &http.Client{Timeout: requestTimeout}
	if err := run(flag.Args(), client); err != nil {
		log.Fatal(err)
	}
}
