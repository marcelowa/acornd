package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/identity"
	"github.com/marcelowa/acornd/gateway/internal/update"
)

// updateCommand checks for a release and installs it.
//
// Two paths, one behaviour: when a gateway is running, the update goes through its API, so
// active runs are drained and the process replaces itself with the new build. When nothing is
// listening there is nothing to drain and nothing to replace, so the same apply happens on disk
// and takes effect at the next start — the pending marker the update tier already uses.
func updateCommand(argv []string) {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornd folder (default: ~/.acornd)")
	host := flags.String("host", "127.0.0.1", "the running gateway's interface")
	port := flags.Int("port", 7777, "the running gateway's port")
	assumeYes := flags.Bool("yes", false, "install without asking")
	checkOnly := flags.Bool("check", false, "check only; install nothing")
	stopRuns := flags.Bool("stop-runs", false, "stop active runs instead of waiting for them to finish")
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, `usage: acornd update [flags]

  --check       report what is available and stop
  --yes         install without asking
  --stop-runs   stop active runs instead of waiting for them
  --host HOST   where a running gateway listens (default 127.0.0.1)
  --port PORT   its port (default 7777)
  --data-dir DIR

With nothing running, the update is written to disk and takes effect at the next start.
`)
	}
	flagArgs, positional := splitFlags(argv, map[string]bool{"data-dir": true, "host": true, "port": true})
	if err := flags.Parse(flagArgs); err != nil {
		os.Exit(2)
	}
	if len(positional) > 0 {
		flags.Usage()
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		logFatal(err)
	}
	address := net.JoinHostPort(*host, strconv.Itoa(*port))
	if gatewayAnswers(dir, address) {
		runUpdateThroughGateway(dir, address, *checkOnly, *assumeYes, *stopRuns)
		return
	}
	runUpdateOffline(dir, *checkOnly, *assumeYes)
}

// gatewayAnswers is the shortest question that says whether the update can be drained and
// restarted for real: the status endpoint answers without doing anything.
func gatewayAnswers(dir, address string) bool {
	request, err := http.NewRequest(http.MethodGet, "http://"+address+"/api/update/status", nil)
	if err != nil {
		return false
	}
	prove(request, dir)
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}

type updateStatus struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Notes     string `json:"notes"`
	Available bool   `json:"available"`
}

func runUpdateThroughGateway(dir, address string, checkOnly, assumeYes, stopRuns bool) {
	status, err := gatewayUpdate(dir, address, "/api/update/check", nil)
	if err != nil {
		logFatal(err)
	}
	if !status.Available {
		fmt.Printf("acornd is up to date (%s)\n", status.Current)
		return
	}
	printUpdateOffer(status)
	if checkOnly || !approved(status.Latest, assumeYes) {
		return
	}
	applyUpdate(dir, address, status.Latest, stopRuns)
}

// approved is the question, and the answer. An unattended run says yes by asking for --yes.
func approved(latest string, assumeYes bool) bool {
	if assumeYes {
		return true
	}
	agreed, err := confirmYes(fmt.Sprintf("install %s? [Y/n] ", latest))
	if err != nil {
		logFatal(err)
	}
	if !agreed {
		fmt.Println("nothing was changed")
	}
	return agreed
}

// applyUpdate asks the gateway to install one release, with what to do about work in flight.
func applyUpdate(dir, address, tag string, stopRuns bool) {
	drain := "wait"
	if stopRuns {
		drain = "stop"
	}
	_, err := gatewayUpdate(dir, address, "/api/update/apply", map[string]string{"tag": tag, "drain": drain})
	if err != nil {
		if errors.Is(err, errRunsActive) && !stopRuns {
			logFatal(fmt.Errorf("%w (rerun with --stop-runs to stop them)", err))
		}
		logFatal(err)
	}
	fmt.Printf("installed %s; the gateway is restarting into it\n", tag)
}

// errRunsActive is the gateway's 409: work is in flight and the default is to wait for it.
var errRunsActive = errors.New("runs are active")

// gatewayUpdate is one JSON call to the running gateway, with the answer decoded into the same
// status the interface reads.
func gatewayUpdate(dir, address, path string, body any) (updateStatus, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return updateStatus{}, err
		}
		payload = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(http.MethodPost, "http://"+address+path, payload)
	if err != nil {
		return updateStatus{}, err
	}
	if body != nil {
		request.Header.Set("content-type", "application/json")
	}
	prove(request, dir)
	// A check reaches the network, so it waits longer than a status call would.
	client := &http.Client{Timeout: 60 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return updateStatus{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusConflict {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return updateStatus{}, fmt.Errorf("%w: %s", errRunsActive, strings.TrimSpace(string(message)))
	}
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return updateStatus{}, fmt.Errorf("%s: %s", path, strings.TrimSpace(string(message)))
	}
	var status updateStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return updateStatus{}, err
	}
	return status, nil
}

// runUpdateOffline is the same decision with nobody to restart: the apply writes the new build
// and the pending marker, and the next start runs the migrate that goes with it.
func runUpdateOffline(dir string, checkOnly, assumeYes bool) {
	manager := update.New(dir, "")
	if err := manager.Open(); err != nil {
		logFatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := manager.Check(ctx); err != nil {
		logFatal(err)
	}
	status := manager.Status()
	if !status.Available {
		fmt.Printf("acornd is up to date (%s)\n", status.Current)
		return
	}
	printUpdateOffer(updateStatus{Current: status.Current, Latest: status.Latest, Notes: status.Notes, Available: true})
	if checkOnly {
		return
	}
	if !assumeYes {
		agreed, err := confirmYes(fmt.Sprintf("install %s? [Y/n] ", status.Latest))
		if err != nil {
			logFatal(err)
		}
		if !agreed {
			fmt.Println("nothing was changed")
			return
		}
	}
	if err := manager.Apply(ctx, update.ApplyOptions{Tag: status.Latest, Drain: "stop"}); err != nil {
		logFatal(err)
	}
	fmt.Printf("installed %s; it takes effect the next time acornd starts\n", status.Latest)
}

// printUpdateOffer is the same three lines on both paths: what you have, what is there, and the
// first of what changed.
func printUpdateOffer(status updateStatus) {
	fmt.Printf("acornd %s → %s\n", status.Current, status.Latest)
	for _, line := range firstLines(status.Notes, 5) {
		fmt.Printf("  %s\n", line)
	}
}

// firstLines keeps the offer readable: the notes are a release's whole story, and the answer to
// this prompt needs a headline, not a changelog. Each line is cut to one screen width, because a
// paragraph wrapped by the terminal is worse than a sentence and an ellipsis.
func firstLines(notes string, limit int) []string {
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(notes))
	for scanner.Scan() && len(lines) < limit {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "- ")
		if runes := []rune(line); len(runes) > 110 {
			line = strings.TrimSpace(string(runes[:109])) + "…"
		}
		lines = append(lines, line)
	}
	return lines
}

// confirmYes is the other prompt in this program: it defaults to yes, because someone who ran
// `acornd update` already asked for the update.
func confirmYes(prompt string) (bool, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(stdinForTest)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	}
	return false, nil
}

// prove adds the install's token when it has one. The command line is the admin, and a console with
// people refuses a request with no proof; an install with no token file is the single-admin install
// ADR 0020 describes, and it expects no header at all.
func prove(request *http.Request, dir string) {
	if dir == "" {
		return
	}
	token, err := identity.ReadToken(dir)
	if err != nil {
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
}
