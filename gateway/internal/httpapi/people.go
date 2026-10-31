// the book of people: who may use this install, and what they may do
//
// Part of the httpapi package. The book is the edge's file, and the screen that changes it is the admin's
// (ADR 0032 §3): a person manages rooms, never the install.
package httpapi

import (
	"errors"
	"net/http"

	"github.com/acornfall/acornfall/gateway/internal/people"
)

func (s *Server) peopleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/people", s.admin(s.peopleList))
	mux.HandleFunc("POST /api/people", s.admin(s.peopleAdd))
	mux.HandleFunc("POST /api/people/{email}/revoke", s.admin(s.peopleRevoke))
	mux.HandleFunc("POST /api/people/{email}/restore", s.admin(s.peopleRestore))
	mux.HandleFunc("POST /api/people/{email}/models", s.admin(s.peopleModels))
	mux.HandleFunc("POST /api/people/{email}/can_create", s.admin(s.peopleCanCreate))
}

// peopleAnswer is the book as the screen reads it, after whatever the request changed: one document, so a
// list that was refetched between a write and a read cannot show the screen a state that never existed.
func (s *Server) peopleAnswer(w http.ResponseWriter) {
	if s.gate.People == nil {
		writeJSON(w, http.StatusOK, map[string]any{"people": []people.Person{}})
		return
	}
	list, err := s.gate.People.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"people": list})
}

func (s *Server) peopleList(w http.ResponseWriter, r *http.Request) {
	s.peopleAnswer(w)
}

// peopleAdd names somebody. Their record is invited until they first sign in, and they may make rooms of
// their own unless the body says otherwise.
func (s *Server) peopleAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email     string   `json:"email"`
		Name      string   `json:"name"`
		Models    []string `json:"models"`
		CanCreate *bool    `json:"can_create"`
	}
	if !decode(w, r, &body) {
		return
	}
	if s.gate.People == nil {
		http.Error(w, "this install has no book of people", http.StatusConflict)
		return
	}
	person, err := s.gate.People.Add(body.Email, body.Name)
	if err != nil {
		http.Error(w, err.Error(), peopleStatus(err))
		return
	}
	if body.CanCreate != nil {
		if _, err := s.gate.People.SetCanCreate(person.Email, *body.CanCreate); err != nil {
			http.Error(w, err.Error(), peopleStatus(err))
			return
		}
	}
	if body.Models != nil {
		if _, err := s.gate.People.SetModels(person.Email, body.Models); err != nil {
			http.Error(w, err.Error(), peopleStatus(err))
			return
		}
	}
	go s.reconcileAfterPeople()
	s.peopleAnswer(w)
}

func (s *Server) peopleRevoke(w http.ResponseWriter, r *http.Request) {
	if s.gate.People == nil {
		http.Error(w, "this install has no book of people", http.StatusConflict)
		return
	}
	if err := s.gate.People.Revoke(r.PathValue("email")); err != nil {
		http.Error(w, err.Error(), peopleStatus(err))
		return
	}
	go s.reconcileAfterPeople()
	s.peopleAnswer(w)
}

// peopleRestore puts a revoked person back. Their rooms never lost their name, so the door and the rooms agree
// again on the next request (ADR 0034 §1).
func (s *Server) peopleRestore(w http.ResponseWriter, r *http.Request) {
	if s.gate.People == nil {
		http.Error(w, "this install has no book of people", http.StatusConflict)
		return
	}
	if _, err := s.gate.People.Restore(r.PathValue("email")); err != nil {
		http.Error(w, err.Error(), peopleStatus(err))
		return
	}
	go s.reconcileAfterPeople()
	s.peopleAnswer(w)
}

func (s *Server) peopleModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Models []string `json:"models"`
	}
	if !decode(w, r, &body) {
		return
	}
	if s.gate.People == nil {
		http.Error(w, "this install has no book of people", http.StatusConflict)
		return
	}
	if _, err := s.gate.People.SetModels(r.PathValue("email"), body.Models); err != nil {
		http.Error(w, err.Error(), peopleStatus(err))
		return
	}
	s.peopleAnswer(w)
}

func (s *Server) peopleCanCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CanCreate bool `json:"can_create"`
	}
	if !decode(w, r, &body) {
		return
	}
	if s.gate.People == nil {
		http.Error(w, "this install has no book of people", http.StatusConflict)
		return
	}
	if _, err := s.gate.People.SetCanCreate(r.PathValue("email"), body.CanCreate); err != nil {
		http.Error(w, err.Error(), peopleStatus(err))
		return
	}
	s.peopleAnswer(w)
}

// peopleStatus says what went wrong in the book's terms: an address it does not hold is not there, and a
// record that already exists is a conflict rather than a refusal of the person asking.
func peopleStatus(err error) int {
	var unknown people.ErrUnknown
	if errors.As(err, &unknown) {
		return http.StatusNotFound
	}
	if errors.Is(err, people.ErrExists) {
		return http.StatusConflict
	}
	return http.StatusConflict
}
