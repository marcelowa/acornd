// Remote access: the tunnel a browser comes through, and the Access application that admits only the people
// the admin named. See docs/adr/0033-remote-access.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/marcelowa/acornd/gateway/internal/people"
	"github.com/marcelowa/acornd/gateway/internal/remote"
)

func remoteCommand(argv []string) {
	if len(argv) > 0 {
		switch argv[0] {
		case "status":
			remoteStatus(argv[1:])
			return
		case "apply":
			remoteApply(argv[1:])
			return
		case "help", "-h", "--help":
			remoteUsage(os.Stdout)
			return
		}
	}
	remoteUsage(os.Stderr)
	os.Exit(2)
}

func remoteUsage(w *os.File) {
	fmt.Fprint(w, `usage: acornd remote <command>

  status                  what this install recorded, and whether it is ready
  apply --zone DOMAIN     make Cloudflare match it, for --hostname HOST
                          --token T, or ACORND_CLOUDFLARE_TOKEN

The hostname is a label (console) or a whole name (console.acornd.app): a label is
completed with the zone, and a name in another zone is refused. The zone is the one
the record and the tunnel belong to.

The API token needs Account · Cloudflare Tunnel · Edit, Zone · DNS · Edit, and
Access: Apps and Policies · Edit plus Organizations, IdP and Groups · Edit.
Its ids are written to remote.json in the install folder, so a second apply
creates nothing and a run that died halfway resumes.
`)
}

func remoteStatus(argv []string) {
	flags := flag.NewFlagSet("remote status", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornd folder (default: ~/.acornd)")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		logFatal(err)
	}
	setup, err := remote.Load(dir)
	if err != nil {
		logFatal(err)
	}
	if !setup.Configured() {
		fmt.Println("no remote access yet: `acornd remote apply --zone DOMAIN --hostname HOST` sets it up")
		return
	}
	rows := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(rows, "hostname\t%s\n", setup.Desired.Hostname)
	fmt.Fprintf(rows, "zone\t%s\n", setup.Desired.ZoneID)
	fmt.Fprintf(rows, "account\t%s\n", setup.Desired.AccountID)
	for _, line := range []struct{ name, value string }{
		{"tunnel", setup.State.TunnelID},
		{"dns record", setup.State.DNSRecordID},
		{"identity provider", setup.State.IdentityID},
		{"access group", setup.State.GroupID},
		{"access app", setup.State.AppID},
		{"team domain", setup.State.TeamDomain},
		{"audience", setup.State.Audience},
		{"policy", setup.State.PolicyID},
	} {
		value := line.value
		if value == "" {
			value = "missing"
		}
		fmt.Fprintf(rows, "%s\t%s\n", line.name, value)
	}
	fmt.Fprintf(rows, "updated\t%s\n", setup.Updated)
	if err := rows.Flush(); err != nil {
		logFatal(err)
	}
	if !setup.State.Ready() {
		fmt.Println("\nnot ready: run `acornd remote apply` again once the missing pieces can be made")
	}
}

func remoteApply(argv []string) {
	flags := flag.NewFlagSet("remote apply", flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornd folder (default: ~/.acornd)")
	hostname := flags.String("hostname", "", "the hostname the console answers on")
	zoneName := flags.String("zone", "", "the Cloudflare zone the hostname belongs to")
	token := flags.String("token", "", "the Cloudflare API token (default: ACORND_CLOUDFLARE_TOKEN)")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	dir, err := resolveDataDir(*dataDir)
	if err != nil {
		logFatal(err)
	}
	if *hostname == "" || *zoneName == "" {
		logFatal(fmt.Errorf("remote apply needs --hostname and --zone"))
	}
	// A label is completed with the zone before anything is created, and a name in another zone is refused here
	// rather than by Cloudflare halfway through an apply (ADR 0036 §6). Desired.Validate is still the last word.
	name, err := remote.Hostname(*hostname, *zoneName)
	if err != nil {
		logFatal(err)
	}
	if line := completionLine(*hostname, *zoneName, name); line != "" {
		fmt.Println(line)
	}
	apiToken := *token
	if apiToken == "" {
		apiToken = os.Getenv("ACORND_CLOUDFLARE_TOKEN")
	}
	if apiToken == "" {
		logFatal(remote.ErrNoToken)
	}

	// A command that changes somebody else's account stops when the admin presses Ctrl-C, and the apply reads
	// the same context so a cancelled run does not keep creating things.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := applyRemote(ctx, dir, name, *zoneName, apiToken); err != nil {
		logFatal(err)
	}
}

// completionLine is what an apply says when the name it applies is not the string it was given: the person typed
// console, and the door answers on console.acornd.app, which they should read rather than discover in remote.json
// afterwards.
func completionLine(input, zone, hostname string) string {
	if strings.EqualFold(strings.TrimSpace(input), hostname) {
		return ""
	}
	return fmt.Sprintf("acornd: --hostname %s is completed with the zone %s to %s", strings.TrimSpace(input), zone, hostname)
}

// applyRemote is the whole of the work, so the command above is flags and one failure path.
func applyRemote(ctx context.Context, dir, hostname, zoneName, apiToken string) error {
	setup, admitted, err := remoteInputs(dir)
	if err != nil {
		return err
	}
	client := &remote.Client{Token: apiToken}
	zone, err := client.ZoneByName(ctx, zoneName)
	if err != nil {
		return err
	}
	desire := remote.Desired{Hostname: hostname, ZoneID: zone.ID, AccountID: zone.Account}
	state, steps, applyErr := applyDesire(ctx, dir, desire, setup, admitted, client)
	if applyErr != nil {
		return fmt.Errorf("%w (the ids it made are kept; run it again to finish)", applyErr)
	}
	for _, step := range steps {
		fmt.Printf("%-8s %s%s\n", stepVerb(step), step.Name, stepDetail(step))
	}
	fmt.Printf("\n%s now reaches this machine; %s holds the door, with the audience %s: restart acornd to open it\n", desire.Hostname, remote.File, state.Audience)
	return nil
}

// applyDesire is the work an apply does once the zone is resolved: make the account match the desire, write what
// it made, and hand back the steps it took. It is apart from the command so boot can run the same thing — an
// install whose record was deleted in a dashboard heals at the next start, not at the next command.
func applyDesire(
	ctx context.Context,
	dir string,
	desire remote.Desired,
	setup remote.Setup,
	admitted []string,
	api remote.API,
) (remote.State, []remote.Step, error) {
	state, steps, applyErr := (&remote.Provisioner{API: api}).Apply(ctx, desire, setup.State, admitted)
	// Written whatever happened: what an apply made before it failed is the ids a resume reads instead of
	// starting over, and losing them is how a second tunnel appears.
	if err := remote.Save(dir, setupAfterApply(desire, state, setup), time.Now()); err != nil {
		return state, steps, fmt.Errorf("%w (and the ids it made could not be written: %w)", applyErr, err)
	}
	return state, steps, applyErr
}

// setupAfterApply is the file an apply leaves behind: what was asked for, what exists now, and the API token
// the console already stored. The token is not the apply's to drop — the door follows the book with it, and a
// reconcile at boot would otherwise erase the credential after its first run (ADR 0033 §4).
func setupAfterApply(desire remote.Desired, state remote.State, prior remote.Setup) remote.Setup {
	return remote.Setup{Desired: desire, State: state, APIToken: prior.APIToken}
}

// stepVerb is what happened to one piece, in a word a person reads rather than decodes.
func stepVerb(step remote.Step) string {
	if step.Created {
		return "created"
	}
	if step.Moved {
		return "pointed"
	}
	return "kept"
}

func stepDetail(step remote.Step) string {
	if step.Detail == "" {
		return ""
	}
	return " (" + step.Detail + ")"
}

// remoteInputs is what an apply needs before it can talk to Cloudflare: what the install recorded, and the
// addresses the door admits.
func remoteInputs(dir string) (remote.Setup, []string, error) {
	setup, err := remote.Load(dir)
	if err != nil {
		return remote.Setup{}, nil, err
	}
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		return remote.Setup{}, nil, err
	}
	admitted, err := admittedAddresses(book)
	if err != nil {
		return remote.Setup{}, nil, err
	}
	if len(admitted) == 0 {
		// A door that admits nobody is a door nobody can open, and an Access application with an empty policy is
		// the kind of half-setup that looks finished. Said before anything is created.
		return remote.Setup{}, nil, errors.New("there is nobody to admit yet: `acornd people claim you@example.com` names the admin, and `people add` names the rest")
	}
	return setup, admitted, nil
}

// admittedAddresses is who the door lets in; the book answers, and the group is derived from it on every apply.
func admittedAddresses(book *people.Book) ([]string, error) { return book.Admitted() }
