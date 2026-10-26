// The command line for the book of people: who may use this install, and what they may do. It is the
// owner's door — claiming is here rather than in the API because the token already proves ownership, so
// there is no first-come race and nothing to self-promote (ADR 0032 §3).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/acornfall/acornfall/gateway/internal/people"
)

func peopleCommand(argv []string) {
	if len(argv) > 0 {
		switch argv[0] {
		case "list":
			peopleList(argv[1:])
			return
		case "add":
			peopleAdd(argv[1:])
			return
		case "claim":
			peopleClaim(argv[1:])
			return
		case "revoke":
			peopleRevoke(argv[1:])
			return
		case "models":
			peopleModels(argv[1:])
			return
		case "help", "-h", "--help":
			peopleUsage(os.Stdout)
			return
		}
	}
	peopleUsage(os.Stderr)
	os.Exit(2)
}

func peopleUsage(w *os.File) {
	fmt.Fprint(w, `usage: acornfall people <command>

  list                      everyone who may use this install, and their state
  claim <email>             name this install's owner, once; --name "Their Name"
  add <email>               invite someone; they become active on their first sign-in
  revoke <email>            take access away; their record stays
  models <email> [a/b ...]  what they may run; with no models, every model the install has

acornfall people <command> -h    explains that command's flags

The list is people.json in the install folder, and it is the only allowlist: the door's list is
derived from it, so revoking somebody here revokes them everywhere.
`)
}

// openBook is the book this install keeps, created on first write so an install with nobody named has no
// file to explain.
func openBook(dataDir string) (*people.Book, error) {
	dir, err := resolveDataDir(dataDir)
	if err != nil {
		return nil, err
	}
	return people.Open(filepath.Join(dir, people.File))
}

func peopleList(argv []string) {
	request := parse("people list", argv, false, false)
	list, err := request.book.List()
	if err != nil {
		logFatal(err)
	}
	if len(list) == 0 {
		fmt.Println("nobody yet: `acornfall people claim you@example.com` names this install's owner")
		return
	}
	rows := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(rows, "EMAIL\tNAME\tROLE\tSTATE\tLAST SEEN")
	for _, person := range list {
		name := person.Name
		if name == "" {
			name = "-"
		}
		seen := person.LastSeen
		if seen == "" {
			seen = "never"
		}
		fmt.Fprintf(rows, "%s\t%s\t%s\t%s\t%s\n", person.Email, name, person.Role, person.State, seen)
	}
	if err := rows.Flush(); err != nil {
		logFatal(err)
	}
}

func peopleClaim(argv []string) {
	request := parse("people claim", argv, true, true)
	owner, err := request.book.Claim(request.address, request.name)
	if err != nil {
		logFatal(err)
	}
	fmt.Printf("%s owns this install\n", owner.Email)
}

func peopleAdd(argv []string) {
	request := parse("people add", argv, true, true)
	added, err := request.book.Add(request.address, request.name)
	if err != nil {
		logFatal(err)
	}
	fmt.Printf("%s may sign in from now on; they become active the first time they do\n", added.Email)
}

// peopleModels prints or sets what somebody may run. A model is "provider/modelId", and no models at all is
// every model this install has, which is where a person starts.
func peopleModels(argv []string) {
	request := parseModels("people models", argv)
	person, found, err := request.book.Find(request.address)
	if err != nil {
		logFatal(err)
	}
	if !found {
		logFatal(people.ErrUnknown{Email: request.address})
	}
	if len(request.models) == 0 {
		if len(person.Models) == 0 {
			fmt.Printf("%s may run any model this install has\n", person.Email)
			return
		}
		fmt.Printf("%s may run %s\n", person.Email, person.ModelsText())
		return
	}
	updated, err := request.book.SetModels(request.address, request.models)
	if err != nil {
		logFatal(err)
	}
	fmt.Printf("%s may run %s\n", updated.Email, updated.ModelsText())
}

func peopleRevoke(argv []string) {
	request := parse("people revoke", argv, true, false)
	if err := request.book.Revoke(request.address); err != nil {
		logFatal(err)
	}
	fmt.Printf("%s may not sign in any more; their record and workspace are untouched\n", request.address)
}

// request is what a verb acts on: the book, the address, and the name to record. Every verb takes the
// install folder, so it is parsed once here rather than four times; a verb that names an address requires
// exactly one, because naming nobody is a usage error rather than a claim about everybody.
type request struct {
	book    *people.Book
	address string
	name    string
}

func parse(verb string, argv []string, withAddress, withName bool) request {
	flags := flag.NewFlagSet(verb, flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder (default: ~/.acornfall)")
	// Only the verbs that record a name offer the flag: a flag on a verb that ignores it is a flag that
	// does nothing (ADR 0027).
	name := ""
	if withName {
		name = *flags.String("name", "", "what to call them")
	}
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	if withAddress && flags.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "acornfall: %s needs exactly one email address\n", verb)
		os.Exit(2)
	}
	book, err := openBook(*dataDir)
	if err != nil {
		logFatal(err)
	}
	parsed := request{book: book, name: name}
	if withAddress {
		parsed.address = flags.Arg(0)
	}
	return parsed
}

// modelsRequest is a verb that takes an address and then any number of models.
type modelsRequest struct {
	book    *people.Book
	address string
	models  []string
}

func parseModels(verb string, argv []string) modelsRequest {
	flags := flag.NewFlagSet(verb, flag.ContinueOnError)
	dataDir := flags.String("data-dir", "", "the acornfall folder (default: ~/.acornfall)")
	if err := flags.Parse(argv); err != nil {
		os.Exit(2)
	}
	if flags.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "acornfall: %s needs one email address, and any models after it\n", verb)
		os.Exit(2)
	}
	book, err := openBook(*dataDir)
	if err != nil {
		logFatal(err)
	}
	return modelsRequest{book: book, address: flags.Arg(0), models: flags.Args()[1:]}
}
