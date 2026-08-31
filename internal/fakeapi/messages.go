package fakeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
)

var msgCounter atomic.Int64

func nextMessageID() string { return fmt.Sprintf("msg_fake%08d", msgCounter.Add(1)) }

func (s *Server) handleMessages(w http.ResponseWriter, body []byte) {
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	id := nextMessageID()
	inTokens := estimateTokens(body)
	outTokens := estimateTokens([]byte(s.opts.Reply))
	if !req.Stream {
		s.writeJSON(w, 200, map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": s.opts.Model,
			"content":     []map[string]string{{"type": "text", "text": s.opts.Reply}},
			"stop_reason": "end_turn", "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": inTokens, "output_tokens": outTokens},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	fl, _ := w.(http.Flusher)
	event := func(name string, v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			s.errf("Marshal event %s: %v", name, err)
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
		if fl != nil {
			fl.Flush()
		}
	}
	event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
		"id": id, "type": "message", "role": "assistant", "model": s.opts.Model, "content": []any{},
		"stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]int{"input_tokens": inTokens, "output_tokens": 1},
	}})
	event("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}})
	event("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": s.opts.Reply}})
	event("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
	event("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": outTokens}})
	event("message_stop", map[string]any{"type": "message_stop"})
}
