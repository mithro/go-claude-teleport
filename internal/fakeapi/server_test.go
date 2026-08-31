package fakeapi

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestServer(t *testing.T, o Options) (*Server, *httptest.Server) {
	t.Helper()
	s := New(o)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func TestMessagesStreaming(t *testing.T) {
	s, ts := newTestServer(t, Options{Reply: "Hello from the canned server.", Model: "claude-opus-5"})
	body := `{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"say hello"}]}`
	resp, err := http.Post(ts.URL+"/v1/messages?beta=true", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d content-type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var events []string
	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			events = append(events, strings.TrimPrefix(line, "event: "))
		}
		if strings.HasPrefix(line, "data: ") {
			var d struct {
				Type  string `json:"type"`
				Delta struct {
					Type       string `json:"type"`
					Text       string `json:"text"`
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Message struct {
					Model string `json:"model"`
					Role  string `json:"role"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &d); err != nil {
				t.Fatalf("bad data line %q: %v", line, err)
			}
			if d.Type == "content_block_delta" {
				text.WriteString(d.Delta.Text)
			}
			if d.Type == "message_start" && (d.Message.Model != "claude-opus-5" || d.Message.Role != "assistant") {
				t.Errorf("message_start = %+v", d.Message)
			}
			if d.Type == "message_delta" && d.Delta.StopReason != "end_turn" {
				t.Errorf("message_delta stop_reason = %q", d.Delta.StopReason)
			}
		}
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Errorf("events = %v, want %v", events, want)
	}
	if text.String() != "Hello from the canned server." {
		t.Errorf("text = %q", text.String())
	}
	reqs := s.Requests()
	if len(reqs) != 1 || reqs[0].Path != "/v1/messages" || !strings.Contains(string(reqs[0].Body), "say hello") || reqs[0].At.IsZero() {
		t.Errorf("recorded = %+v", reqs)
	}
}

func TestMessagesNonStreaming(t *testing.T) {
	_, ts := newTestServer(t, Options{Reply: "ok", Model: "claude-opus-5"})
	resp, err := http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Role    string `json:"role"`
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Type != "message" || m.Role != "assistant" || m.Model != "claude-opus-5" || len(m.Content) != 1 || m.Content[0].Text != "ok" || m.StopReason != "end_turn" || !strings.HasPrefix(m.ID, "msg_") || m.Usage.OutputTokens == 0 {
		t.Errorf("message = %+v", m)
	}
}

func TestOtherEndpoints(t *testing.T) {
	_, ts := newTestServer(t, Options{Reply: "ok", Model: "claude-opus-5"})
	get := func(path string) (int, string) {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/v1/models"); code != 200 || !strings.Contains(body, `"id":"claude-opus-5"`) {
		t.Errorf("/v1/models: %d %s", code, body)
	}
	if code, body := get("/api/hello"); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Errorf("/api/hello: %d %s", code, body)
	}
	resp, _ := http.Post(ts.URL+"/v1/messages/count_tokens", "application/json", strings.NewReader(`{"messages":[{"role":"user","content":"hi there"}]}`))
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"input_tokens":`) {
		t.Errorf("count_tokens: %d %s", resp.StatusCode, b)
	}
	if code, body := get("/v1/nope"); code != 404 || !strings.Contains(body, `"type":"not_found_error"`) || !strings.Contains(body, "/v1/nope") {
		t.Errorf("404: %d %s", code, body)
	}
}

func TestLogDirWritesOneFilePerRequest(t *testing.T) {
	dir := t.TempDir()
	_, ts := newTestServer(t, Options{Reply: "ok", Model: "m", LogDir: dir})
	http.Post(ts.URL+"/v1/messages", "application/json", strings.NewReader(`{"a":1}`))
	http.Get(ts.URL + "/api/hello")
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 2 {
		t.Fatalf("log files = %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	if !strings.Contains(string(raw), `"path"`) {
		t.Errorf("log file = %s", raw)
	}
}
