// the supervisor's file verbs: the sandbox's own filesystem, asked over the same socket as a
// command
//
// Part of the supervise package. A worker runs on the host, beside the sandbox, and must not
// answer a file question from the host's filesystem: for an engine with an image root, `/etc`
// and `/tmp` in the sandbox are not the machine's. The supervisor is the process inside those
// namespaces, so it is the one that can answer.
package supervise

import (
	"errors"
	"os"
)

// isFileVerb names the requests the file half of the protocol answers.
func isFileVerb(name string) bool {
	switch name {
	case "read", "write", "list", "stat", "mkdir", "remove", "rename":
		return true
	}
	return false
}

// fileRequest answers one file verb and sends exactly one event: a "file" answer or an "error".
// It is synchronous on purpose. A read or a stat is cheap, and a connection that asked for one
// has nothing else to do until it hears the answer.
func (s *Server) fileRequest(connection *conn, request Request) {
	switch request.Type {
	case "read":
		s.readFile(connection, request)
	case "write":
		s.writeFile(connection, request)
	case "list":
		s.listDir(connection, request)
	case "stat":
		s.statPath(connection, request)
	case "mkdir":
		s.mkdir(connection, request)
	case "remove":
		s.remove(connection, request)
	case "rename":
		s.rename(connection, request)
	default:
		connection.send(Event{Type: "error", ID: request.ID, Error: "unknown request " + request.Type})
	}
}

// readFile answers a path's bytes, or that it is not there. Any other failure is an error: a
// permission denied is not the same answer as a missing file.
func (s *Server) readFile(connection *conn, request Request) {
	data, err := os.ReadFile(request.Path)
	if errors.Is(err, os.ErrNotExist) {
		connection.send(Event{Type: "file", ID: request.ID, Missing: true})
		return
	}
	if err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	connection.send(Event{Type: "file", ID: request.ID, Content: data})
}

// writeFile creates or replaces a path, or appends to it. It does not create parents: the
// tool layer already made them, and a writer that silently invents a tree is a writer that can
// put a file where the caller did not name one.
func (s *Server) writeFile(connection *conn, request Request) {
	flags := os.O_CREATE | os.O_WRONLY
	if request.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	file, err := os.OpenFile(request.Path, flags, 0o644)
	if err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	_, writeErr := file.Write(request.Content)
	closeErr := file.Close()
	if writeErr != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: writeErr.Error()})
		return
	}
	if closeErr != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: closeErr.Error()})
		return
	}
	connection.send(Event{Type: "file", ID: request.ID})
}

// listDir answers one directory's entries. A missing directory is an empty list: the Files
// contract has no separate word for it, and a caller that needed to tell them apart can stat.
func (s *Server) listDir(connection *conn, request Request) {
	entries, err := os.ReadDir(request.Path)
	if errors.Is(err, os.ErrNotExist) {
		connection.send(Event{Type: "file", ID: request.ID, Entries: []FileEntry{}})
		return
	}
	if err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	answer := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			// The entry went away between the read and the stat. It is still a valid listing of
			// what was there, and the name is the part a caller needs.
			answer = append(answer, FileEntry{Name: entry.Name(), Directory: entry.IsDir()})
			continue
		}
		answer = append(answer, FileEntry{
			Name:      entry.Name(),
			Directory: info.IsDir(),
			Size:      info.Size(),
			MtimeMs:   info.ModTime().UnixMilli(),
		})
	}
	connection.send(Event{Type: "file", ID: request.ID, Entries: answer})
}

// statPath answers what one path is, or that it is not there.
func (s *Server) statPath(connection *conn, request Request) {
	info, err := os.Stat(request.Path)
	if errors.Is(err, os.ErrNotExist) {
		connection.send(Event{Type: "file", ID: request.ID, Missing: true})
		return
	}
	if err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	connection.send(Event{Type: "file", ID: request.ID, Info: &FileInfo{
		Directory: info.IsDir(),
		Size:      info.Size(),
		MtimeMs:   info.ModTime().UnixMilli(),
	}})
}

// mkdir makes a directory and every missing parent: that is the one shape the tool layer asks
// for, and a separate non-recursive verb would be a second answer to the same question.
func (s *Server) mkdir(connection *conn, request Request) {
	if err := os.MkdirAll(request.Path, 0o755); err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	connection.send(Event{Type: "file", ID: request.ID})
}

// remove deletes a path, recursively when asked. Force makes a path that is not there the
// wanted state rather than an error.
func (s *Server) remove(connection *conn, request Request) {
	var err error
	if request.Recursive {
		err = os.RemoveAll(request.Path)
	} else {
		err = os.Remove(request.Path)
	}
	if request.Force && errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	connection.send(Event{Type: "file", ID: request.ID})
}

// rename moves a path within the sandbox's filesystem.
func (s *Server) rename(connection *conn, request Request) {
	if err := os.Rename(request.Path, request.To); err != nil {
		connection.send(Event{Type: "error", ID: request.ID, Error: err.Error()})
		return
	}
	connection.send(Event{Type: "file", ID: request.ID})
}
