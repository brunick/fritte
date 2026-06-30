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

//go:embed swagger.json
var swaggerJSON string

//go:embed docs.html
var docsHTML string

// swaggerTmpl wird aus der eingebetteten swagger.json gebildet, um
// servers.url dynamisch an den eingehenden Request anzupassen.
var swaggerTmpl *template.Template

type Server struct {
	scraper *fritz.Scraper
	tmpl    *template.Template
}

func init() {
	var err error
	swaggerTmpl, err = template.New("swagger").Parse(swaggerJSON)
	if err != nil {
		panic("swagger.json template: " + err.Error())
	}
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
	mux.HandleFunc("/swagger.json", s.handleSwaggerJSON)
	mux.HandleFunc("/docs", s.handleDocs)
}

type dashboardView struct {
	Generated  string
	Halted     bool
	HaltReason string
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	view := dashboardView{Generated: time.Now().Format(time.RFC3339)}
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

func (s *Server) handleSwaggerJSON(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	serverURL := scheme + "://" + r.Host

	w.Header().Set("Content-Type", "application/json")
	if err := swaggerTmpl.Execute(w, map[string]string{"ServerURL": serverURL}); err != nil {
		http.Error(w, "swagger: "+err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(docsHTML))
}
