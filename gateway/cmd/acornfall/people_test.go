package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acornfall/acornfall/gateway/internal/people"
)

// capture runs a command with stdout swapped, which is the only way to test one whose whole job is to
// print a table a person reads.
func capture(t *testing.T, run func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	original := os.Stdout
	os.Stdout = write
	printed := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(read)
		printed <- string(data)
	}()
	defer func() { os.Stdout = original }()
	run()
	if err := write.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return <-printed
}

// An install nobody has been named for says what to do rather than printing an empty table: an empty
// table looks like a bug, and the fix is one command.
func TestTheListOfNobodySaysHowToStart(t *testing.T) {
	printed := capture(t, func() { peopleList([]string{"--data-dir", t.TempDir()}) })
	if !strings.Contains(printed, "claim") {
		t.Errorf("the empty list does not say how to name somebody: %q", printed)
	}
}

// The whole life of a person through the command line: claimed as the owner, added as a guest, listed,
// and revoked. It is one test because the order is the feature.
func TestTheCommandLineNamesPeopleAndTakesAccessAway(t *testing.T) {
	dir := t.TempDir()

	claimed := capture(t, func() { peopleClaim([]string{"--data-dir", dir, "--name", "Me", "me@example.com"}) })
	if !strings.Contains(claimed, "owns this install") {
		t.Errorf("claim said %q", claimed)
	}
	added := capture(t, func() { peopleAdd([]string{"--data-dir", dir, "--name", "Ana", "ana@example.com"}) })
	if !strings.Contains(added, "may sign in from now on") {
		t.Errorf("add said %q", added)
	}

	list := capture(t, func() { peopleList([]string{"--data-dir", dir}) })
	for _, want := range []string{"me@example.com", "ana@example.com", "owner", "person", "invited"} {
		if !strings.Contains(list, want) {
			t.Errorf("the list does not mention %q: %q", want, list)
		}
	}

	revoked := capture(t, func() { peopleRevoke([]string{"--data-dir", dir, "ana@example.com"}) })
	if !strings.Contains(revoked, "may not sign in any more") {
		t.Errorf("revoke said %q", revoked)
	}

	// And the file agrees, because the command line and the gateway read the same one.
	book, err := people.Open(filepath.Join(dir, people.File))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	records, err := book.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(records) != 2 || records[0].Email != "me@example.com" || records[1].State != people.Revoked {
		t.Errorf("the book reads %+v", records)
	}
}
