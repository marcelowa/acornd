// the event stream: one client, one conversation, and the proof of life
//
// Part of the httpapi package. The layer table is in ADR 0002: this is the transport edge,
// and it forwards frames it does not interpret.
package httpapi

import (
	"github.com/marcelowa/acornd/gateway/internal/control"
	"github.com/marcelowa/acornd/gateway/internal/protocol"
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
	subscription, err := ctl.Subscribe(r.Context(), caller, r.URL.Query().Get("conversation"))
	if err != nil {
		// The stream is already open, so a status line here would be a second header that no
		// client reads. Say it on the stream and close: the client reconnects, which is what
		// an EventSource does with a closed connection, and it works once the reason is gone.
		_ = writeEvent(w, "state", map[string]string{"error": err.Error()})
		flusher.Flush()
		return
	}
	defer ctl.Unsubscribe(subscription.ID)

	if !s.sendOpening(w, flusher, ctl, caller, subscription.Conversation, subscription.Initial) {
		return
	}
	s.forward(w, flusher, r, ctl, caller, subscription)
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
	if err := writeEvent(w, "state", s.stateOf(ctl, caller, conversationID)); err != nil {
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
	subscription control.Subscription,
) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for s.forwardOnce(w, flusher, r.Context().Done(), ticker.C, ctl, caller, subscription) {
	}
}

// forwardOnce writes whatever one wakeup carries: a keepalive, a queued event, or a fresh state
// or view. False means the client is gone.
func (s *Server) forwardOnce(
	w http.ResponseWriter,
	flusher http.Flusher,
	done <-chan struct{},
	ping <-chan time.Time,
	ctl *control.Control,
	caller control.Caller,
	subscription control.Subscription,
) bool {
	select {
	case <-done:
		return false
	case <-ping:
		return sendEvent(w, flusher, "ping", map[string]int64{"at": time.Now().UnixMilli()})
	case frame := <-subscription.Frames:
		return sendEvent(w, flusher, "frame", frame)
	// The state moved, and the client's copy is stale. It is answered with the state itself,
	// built now rather than when the signal was raised: the signal can wait, the state cannot
	// be old.
	case <-subscription.State:
		return s.sendState(w, flusher, ctl, caller, subscription.Conversation)
	case <-subscription.View:
		return sendView(w, flusher, ctl, subscription.Conversation)
	}
}

// sendState builds the state now and writes it. The signal can wait; the state cannot be old.
func (s *Server) sendState(
	w http.ResponseWriter,
	flusher http.Flusher,
	ctl *control.Control,
	caller control.Caller,
	conversationID string,
) bool {
	return sendEvent(w, flusher, "state", s.stateOf(ctl, caller, conversationID))
}

// sendView reads the newest complete view when the signal fires. The signal carries no value
// because one read is enough for any number of skipped frames: a client that fell behind loses
// the middle of a run, never the frame that says the run ended — which is the one it cannot get
// from anywhere else. Nothing cached yet is not a failure: the first view will wake the client.
func sendView(
	w http.ResponseWriter,
	flusher http.Flusher,
	ctl *control.Control,
	conversationID string,
) bool {
	frame, found := ctl.CachedView(conversationID)
	return !found || sendEvent(w, flusher, "frame", frame)
}

// sendEvent writes one event and flushes it: every case in the stream loop does the same two
// things, and false means the client is gone.
func sendEvent(w http.ResponseWriter, flusher http.Flusher, name string, value any) bool {
	if err := writeEvent(w, name, value); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
