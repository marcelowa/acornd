package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// The offer is a headline, not a changelog: five lines at most, each one screen wide, headings
// and blank lines skipped so the first thing read is the first thing that changed.
func TestTheUpdateOfferKeepsHeadlines(t *testing.T) {
	notes := "## What changed\n\n" +
		"- first thing\n" +
		"- second thing that is " + strings.Repeat("long ", 40) + "\n" +
		"- third thing\n" +
		"- fourth thing\n" +
		"- fifth thing\n" +
		"- sixth thing\n"
	lines := firstLines(notes, 3)
	if len(lines) != 3 {
		t.Fatalf("wanted three lines, got %d: %q", len(lines), lines)
	}
	if lines[0] != "first thing" || lines[2] != "third thing" {
		t.Fatalf("the lines are not the notes in order: %q", lines)
	}
	if utf8.RuneCountInString(lines[1]) > 110 || !strings.HasSuffix(lines[1], "…") {
		t.Fatalf("a long line should be cut, got %d characters", len(lines[1]))
	}
}

// The prompt after `acornd update` defaults to yes: the command was already the request.
func TestTheUpdatePromptDefaultsToYes(t *testing.T) {
	for input, want := range map[string]bool{
		"\n": true, "y\n": true, "Y\n": true, "yes\n": true,
		"n\n": false, "no\n": false, "later\n": false,
	} {
		original := stdinForTest
		stdinForTest = strings.NewReader(input)
		got, err := confirmYes("? ")
		stdinForTest = original
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if got != want {
			t.Errorf("%q: got %v, want %v", input, got, want)
		}
	}
}

// The gateway's side of the update conversation, without a gateway: what a 200, a 409 and an
// error status each become. The 409 is the interesting one — it is the answer that means "work is
// in flight", and the only one the caller is expected to react to rather than report.
func TestAGatewayAnswerBecomesAStatusOrAReason(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    updateStatus
		wantErr string
		runs    bool
	}{
		{
			name:   "a release is available",
			status: http.StatusOK,
			body:   `{"current":"v1.0.0","latest":"v1.1.0","notes":"- fixed","available":true}`,
			want:   updateStatus{Current: "v1.0.0", Latest: "v1.1.0", Notes: "- fixed", Available: true},
		},
		{
			name:   "nothing newer",
			status: http.StatusOK,
			body:   `{"current":"v1.0.0","available":false}`,
			want:   updateStatus{Current: "v1.0.0"},
		},
		{
			name:    "runs are active",
			status:  http.StatusConflict,
			body:    "a turn is running in general",
			wantErr: "a turn is running",
			runs:    true,
		},
		{
			name:    "the gateway refused for another reason",
			status:  http.StatusInternalServerError,
			body:    "the release check has not run yet",
			wantErr: "the release check has not run yet",
		},
		{
			name:    "an answer this build cannot read",
			status:  http.StatusOK,
			body:    "{",
			wantErr: "unexpected EOF",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/update/check" {
					t.Errorf("asked %s %s, want POST /api/update/check", r.Method, r.URL.Path)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			got, err := gatewayUpdate("", strings.TrimPrefix(server.URL, "http://"), "/api/update/check", nil)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want it to say %q", err, test.wantErr)
				}
				if errors.Is(err, errRunsActive) != test.runs {
					t.Errorf("errRunsActive = %v, want %v", errors.Is(err, errRunsActive), test.runs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Errorf("status = %+v, want %+v", got, test.want)
			}
		})
	}
}

// A body is sent as JSON with the content type that says so, because the gateway refuses anything
// else: a body that arrives as text is a body nobody reads.
func TestAnApplySendsItsBodyAsJSON(t *testing.T) {
	var seen map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("content-type"); got != "application/json" {
			t.Errorf("content-type = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
			t.Errorf("the body is not JSON: %v", err)
		}
		_, _ = w.Write([]byte(`{"current":"v1.1.0","available":false}`))
	}))
	defer server.Close()
	if _, err := gatewayUpdate("", strings.TrimPrefix(server.URL, "http://"), "/api/update/apply",
		map[string]string{"tag": "v1.1.0", "drain": "stop"}); err != nil {
		t.Fatal(err)
	}
	if seen["tag"] != "v1.1.0" || seen["drain"] != "stop" {
		t.Errorf("body = %v, want the tag and the drain", seen)
	}
}

// Nothing listening is a false answer, not an error: it is how the command decides between the
// gateway path and the offline one.
func TestNothingListeningIsNotAGateway(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	if gatewayAnswers("", address) {
		t.Errorf("%s answers, and nothing is listening there", address)
	}
}

// The two paths say the same three lines: what you have, what is there, and the first of what
// changed.
func TestTheOfferSaysWhatChanged(t *testing.T) {
	out := captureStdout(t, func() {
		printUpdateOffer(updateStatus{Current: "v1.0.0", Latest: "v1.1.0", Notes: "## Notes\n\n- a fix\n- another", Available: true})
	})
	if !strings.Contains(out, "v1.0.0 → v1.1.0") {
		t.Errorf("the offer should name both releases: %q", out)
	}
	if !strings.Contains(out, "a fix") || strings.Contains(out, "## Notes") {
		t.Errorf("the offer should keep the first changes and drop the headings: %q", out)
	}
}

// applyUpdate asks for the release with what to do about work in flight, and answers with what
// the gateway said it did.
func TestApplyUpdateAsksTheGatewayToInstall(t *testing.T) {
	var asked map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/update/apply" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&asked)
		_, _ = w.Write([]byte(`{"current":"v1.1.0","available":false}`))
	}))
	defer server.Close()
	out := captureStdout(t, func() {
		applyUpdate("", strings.TrimPrefix(server.URL, "http://"), "v1.1.0", true)
	})
	if asked["tag"] != "v1.1.0" || asked["drain"] != "stop" {
		t.Errorf("asked for %v, want tag with drain=stop", asked)
	}
	if !strings.Contains(out, "installed v1.1.0") {
		t.Errorf("the answer should say it installed: %q", out)
	}
}

// captureStdout is the one place a test reads what a command printed: these functions write to
// the real stdout, because that is what a command does.
func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	saved := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		payload, _ := io.ReadAll(reader)
		done <- string(payload)
	}()
	run()
	writer.Close()
	os.Stdout = saved
	return <-done
}
