package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen       string `yaml:"listen"`
	Upstream     string `yaml:"upstream"`
	DebugTraffic bool   `yaml:"debug_traffic"`
}

const configPath = "/config/config.yaml"
const defaultConfig = `listen: ":8831"
upstream: "http://localhost:9091"
debug_traffic: false
`
const scopeGroups = "groups"

func loadConfig(path string) Config {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("config file not found, using default config")
			data = []byte(defaultConfig)
		} else {
			log.Fatalf("failed to read config: %v", err)
		}
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Fatalf("failed to parse config: %v", err)
	}

	return cfg
}

func addScope(scope, extra string) string {
	scopes := strings.Fields(scope)

	if slices.Contains(scopes, extra) {
		return scope
	}

	scopes = append(scopes, extra)
	return strings.Join(scopes, " ")
}

func ensureScope(values url.Values, scope string) {
	s := values.Get("scope")
	values.Set("scope", addScope(s, scope))
}

func maybeModifyQuery(r *http.Request) {
	q := r.URL.Query()

	scope := q.Get("scope")
	if scope == "" {
		return
	}

	q.Set("scope", addScope(scope, scopeGroups))
	r.URL.RawQuery = q.Encode()
}

func maybeModifyTokenBody(r *http.Request) error {
	if r.Method != http.MethodPost {
		return nil
	}

	if r.URL.Path != "/api/oidc/token" {
		return nil
	}

	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		return nil
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}

	q, err := url.ParseQuery(string(bodyBytes))
	if err != nil {
		return err
	}

	scope := q.Get("scope")
	if scope == "" {
		// Preserve original body because it was consumed by ReadAll.
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
		r.Header.Set("Content-Length", strconv.Itoa(len(bodyBytes)))
		return nil
	}

	q.Set("scope", addScope(scope, scopeGroups))

	encoded := q.Encode()

	r.Body = io.NopCloser(strings.NewReader(encoded))
	r.ContentLength = int64(len(encoded))
	r.Header.Set("Content-Length", strconv.Itoa(len(encoded)))

	return nil
}

func newProxy(target string) *httputil.ReverseProxy {
	url, err := url.Parse(target)
	if err != nil {
		log.Fatalf("invalid upstream: %v", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(url)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = url.Host
	}

	return proxy
}

type loggingRoundTripper struct {
	next http.RoundTripper
}

func (l loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	dumpReq, err := httputil.DumpRequestOut(req, true)
	if err != nil {
		log.Printf("failed to dump outbound request: %v", err)
	} else {
		log.Printf("outbound request:\n%s", dumpReq)
	}

	resp, err := l.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	dumpResp, err := httputil.DumpResponse(resp, true)
	if err != nil {
		log.Printf("failed to dump upstream response: %v", err)
	} else {
		log.Printf("upstream response:\n%s", dumpResp)
	}

	return resp, nil
}

type debugRoundTripper struct{}

func (d debugRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	out := map[string]any{
		"method":         req.Method,
		"url":            req.URL.String(),
		"request_uri":    req.URL.RequestURI(),
		"host":           req.Host,
		"content_length": req.ContentLength,
		"headers":        req.Header,
		"body":           string(bodyBytes),
	}

	payload, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return nil, err
	}

	resp := &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(payload)),
		ContentLength: int64(len(payload)),
		Request:       req,
	}
	resp.Header.Set("Content-Type", "application/json")

	return resp, nil
}

func main() {
	cfg := loadConfig(configPath)

	proxy := newProxy(cfg.Upstream)
	debugProxy := newProxy(cfg.Upstream)
	debugProxy.Transport = debugRoundTripper{}

	if cfg.DebugTraffic {
		transport := proxy.Transport
		if transport == nil {
			transport = http.DefaultTransport
		}

		proxy.Transport = loggingRoundTripper{next: transport}
		debugProxy.Transport = loggingRoundTripper{next: debugProxy.Transport}
		log.Printf("debug traffic logging enabled")
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		maybeModifyQuery(r)
		if err := maybeModifyTokenBody(r); err != nil {
			log.Printf("failed to modify token body: %v", err)
			http.Error(w, "invalid token request", http.StatusBadRequest)
			return
		}

		if r.Header.Get("X-Debug") == "true" {
			debugProxy.ServeHTTP(w, r)
			return
		}

		proxy.ServeHTTP(w, r)
	})

	log.Printf("listening on %s → %s", cfg.Listen, cfg.Upstream)
	log.Fatal(http.ListenAndServe(cfg.Listen, nil))
}
