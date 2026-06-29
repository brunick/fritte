package server

import (
	_ "embed"
	"encoding/json"
	"html/template"
	"net/http"
	"strings"
	"time"

	"fritte/internal/fritz"
	"fritte/internal/metrics"
)

//go:embed dashboard.html
var dashboardHTML string

type Server struct {
	scraper *fritz.Scraper
	tmpl    *template.Template
}

func New(scraper *fritz.Scraper) (*Server, error) {
	tmpl, err := template.New("dashboard").Parse(dashboardHTML)
	if err != nil {
		return nil, err
	}
	return &Server{scraper: scraper, tmpl: tmpl}, nil
}

func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/metrics", metrics.Handler(s.scraper))
	mux.HandleFunc("/api/snapshot", s.handleSnapshot)
	mux.HandleFunc("/api/", s.handleEndpoint)
}

type cardView struct {
	Name  string
	OK    bool
	Error string
	Time  string
	JSON  string
}

type dashboardView struct {
	Generated  string
	Halted     bool
	HaltReason string
	Cards      []cardView
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	all := s.scraper.All()
	names := s.scraper.SnapshotNames()
	cards := make([]cardView, 0, len(names))
	for _, name := range names {
		snap := all[name]
		if !snap.Ok {
			cards = append(cards, cardView{Name: name, OK: false, Error: snap.Error, Time: snap.Time.Format(time.RFC3339)})
			continue
		}
		cards = append(cards, cardView{
			Name: name,
			OK:   true,
			Time: snap.Time.Format(time.RFC3339),
			JSON: prettyJSON(snap.Data),
		})
	}
	view := dashboardView{
		Generated: time.Now().Format(time.RFC3339),
		Cards:     cards,
	}
	view.Halted, view.HaltReason = s.scraper.Halted()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.Execute(w, view); err != nil {
		http.Error(w, "template: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.scraper.All())
}

func (s *Server) handleEndpoint(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/")
	snap, ok := s.scraper.Snapshot(name)
	if !ok {
		http.Error(w, "endpoint "+name+" nicht gefunden", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

func prettyJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}
