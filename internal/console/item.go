package console

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/kevinhamon/meshctl/internal/mesh"
)

// maxItemBytes caps what the drill-down returns.
const maxItemBytes = 256 << 10

// Item is the text behind one pending item: a request file, or a parked
// confirmation round's summary. Read-only.
type Item struct {
	Title string `json:"title"`
	Path  string `json:"path"`
	Text  string `json:"text"`
}

var errNoItem = errors.New("no such item")

// ItemFor finds the file behind an item of a pending line. Only kinds that
// name a file are supported: intake and read-back (the request) and
// kb-branches (the confirmation round's summary).
func ItemFor(dir, agent, kind, id string, now time.Time) (Item, error) {
	a, err := mesh.FindAgent(dir, agent)
	if err != nil {
		return Item{}, err
	}
	switch kind {
	case "intake":
		reqs, _ := mesh.ListRequests(a.InboxPath())
		for _, r := range reqs {
			if r.ID == id {
				return readItem("Request "+id+" to "+agent, r.Path())
			}
		}
	case "read-back":
		sent, _ := mesh.ListSent(dir, agent, true)
		for _, r := range sent {
			if r.ID == id {
				return readItem("Answer to "+agent+"'s request "+id, r.Path())
			}
		}
	case "kb-branches":
		for _, p := range mesh.ParkedKBBranches(dir, a, now) {
			if p.Branch == id {
				return readItem("Confirmation round for "+id, p.SummaryPath)
			}
		}
	}
	return Item{}, errNoItem
}

func readItem(title, path string) (Item, error) {
	f, err := os.Open(path)
	if err != nil {
		return Item{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxItemBytes))
	if err != nil {
		return Item{}, err
	}
	return Item{Title: title, Path: path, Text: string(b)}, nil
}

func (s *Server) item(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	it, err := ItemFor(s.Engine.dir, q.Get("agent"), q.Get("kind"), q.Get("id"), time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(it)
}
