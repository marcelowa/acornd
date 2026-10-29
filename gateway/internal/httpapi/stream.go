// the event stream: one client, one conversation, and the proof of life
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"github.com/acornfall/acornfall/gateway/internal/control"
	"github.com/acornfall/acornfall/gateway/internal/protocol"
	"net/http"
	"time"
)

// stream is SSE. Commands are POSTs; this direction is the only streaming one,
// so a reconnect is built in and no socket library is needed.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	ctl, ok := s.withControl(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	if err := openStream(w); err != nil {
		return
	}
	// Each client names the conversation it wants, so two tabs can watch two
	// conversations without agreeing on an active one — and every state it is sent is its
	// caller's, because the landing and the draft are per person (ADR 0034 §7).
	caller := callerOf(r)
	conversationID := r.URL.Query().Get("conversation")
	id, frames, stale, initial, err := ctl.Subscribe(r.Context(), caller, conversationID)
	if err != nil {
		// The stream is already open, so a status line here would be a second header that no
		// client reads. Say it on the stream and close: the client reconnects, which is what
		// an EventSource does with a closed connection, and it works once the reason is gone.
		_ = writeEvent(w, "state", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	defer ctl.Unsubscribe(id)

	if !s.sendOpening(w, flusher, ctl, caller, conversationID, initial) {
		return
	}
	s.forward(w, flusher, r, ctl, caller, conversationID, frames, stale)
}

// openStream writes the headers and the reconnect interval. Everything after this is events:
// once the status line is out, a failure can only be said on the stream.
func openStream(w http.ResponseWriter) error {
	headers := w.Header()
	headers.Set("Content-Type", "text/event-stream")
	headers.Set("Cache-Control", "no-cache")
	headers.Set("Connection", "keep-alive")
	headers.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// How long the browser should wait before reconnecting.
	_, err := w.Write([]byte("retry: 3000\n\n"))
	return err
}

// sendOpening is the state, then the view the client has not seen yet. False means the client
// is gone.
func (s *Server) sendOpening(
	w http.ResponseWriter,
	flusher http.Flusher,
	ctl *control.Control,
	caller control.Caller,
	conversationID string,
	initial *protocol.Frame,
) bool {
	if err := writeEvent(w, "state", ctl.StateFor(caller, conversationID)); err != nil {
		return false
	}
	if initial != nil {
		if err := writeEvent(w, "frame", *initial); err != nil {
			return false
		}
	}
	flusher.Flush()
	return true
}

// forward is the loop itself: a proof of life every three seconds, frames as they arrive, and
// the state when it moves. A named event rather than an SSE comment, because a comment keeps the
// socket alive and fires nothing — a client could not tell a healthy idle stream from a dead one.
func (s *Server) forward(
	w http.ResponseWriter,
	flusher http.Flusher,
	r *http.Request,
	ctl *control.Control,
	caller control.Caller,
	conversationID string,
	frames <-chan protocol.Frame,
	stale <-chan struct{},
) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if err := writeEvent(w, "ping", map[string]int64{"at": time.Now().UnixMilli()}); err != nil {
				return
			}
			flusher.Flush()
		case frame := <-frames:
			if err := writeEvent(w, "frame", frame); err != nil {
				return
			}
			flusher.Flush()
		// The state moved, and the client's copy is stale. It is answered with the state
		// itself, built now rather than when the signal was raised: the signal can wait,
		// the state cannot be old.
		case <-stale:
			if err := writeEvent(w, "state", ctl.StateFor(caller, conversationID)); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
