package fritz

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"fritte/internal/eventlog"
	"fritte/internal/store"
)

// Endpoint verknuepft einen Anzeigenamen mit dem API-Pfad.
type Endpoint struct {
	Name string
	Path string
}

// Snapshot ist das Ergebnis eines Scraper-Laufs fuer einen Endpunkt.
type Snapshot struct {
	Time  time.Time       `json:"time"`
	Ok    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Scraper pollt alle registrierten Endpunkte im festen Intervall
// und haelt den jeweils letzten Snapshot vor.
type Scraper struct {
	client    *Client
	endpoints []Endpoint
	interval  time.Duration

	mu         sync.RWMutex
	snapshots  map[string]Snapshot
	halted     bool
	haltReason string
	haltedAt   time.Time

	// Optional: Eventlog-Verarbeitung (Postgres + Syslog).
	store   *eventlog.Store
	syslog  *eventlog.Sender
	boxHost string

	// Optional: Persistierung aller Modul-Snapshots pro Modul in Postgres.
	snapstore *store.Store
}

// WithStore konfiguriert den Snapshot-Speicher fuer Modul-History.
func (s *Scraper) WithStore(st *store.Store) {
	s.snapstore = st
}

func NewScraper(client *Client, endpoints []Endpoint, interval time.Duration) *Scraper {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Scraper{
		client:    client,
		endpoints: endpoints,
		interval:  interval,
		snapshots: make(map[string]Snapshot),
		boxHost:   "fritz.box",
	}
}

// WithEventlog konfiguriert Postgres-Speicher und Syslog-Versand fuer Eventlog-Eintraege.
func (s *Scraper) WithEventlog(store *eventlog.Store, syslog *eventlog.Sender) {
	s.store = store
	s.syslog = syslog
}

// SetBoxHost ueberschreibt den Default-Hostnamen, der im Syslog verwendet wird.
func (s *Scraper) SetBoxHost(host string) {
	if host != "" {
		s.boxHost = host
	}
}

// DefaultEndpoints sind die aktuell bekannten FRITZ!Box-Endpoints.
// Weitere URLs koennen hier einfach ergaenzt werden.
func DefaultEndpoints() []Endpoint {
	// Alle bekannten ui-Module gebündelt in multi?ui=-Endpunkten.
	// Aufgeteilt in Gruppen von ca. 7-10 Modulen pro Request.
	// Dino-Endpunkte laufen einzeln, da sie nicht ueber multi?ui= abfragbar sind.
	return []Endpoint{
		// Multi-Module (ca. 7-10 Module pro Request)
		{Name: "multi_box", Path: "/api/v0/generic/multi?ui=box,boxusers,connections,cpu,dect,eth_ports,landevice,nexus"},
		{Name: "multi_system", Path: "/api/v0/generic/multi?ui=plc,power,providerlist,uimodlogic,updatecheck,vpn,wlan_light,webdavclient,nqos"},
		{Name: "multi_telecom", Path: "/api/v0/generic/multi?ui=mobiled,sip,telcfg,umts,budget,ddns,dnscfg,dnsserver,emailnotify"},
		{Name: "multi_inet", Path: "/api/v0/generic/multi?ui=forwardrules,igdforwardrules,inetstat,ipv6,ipv6firewall,jasonii,myfritzdevice,remoteman"},
		{Name: "multi_misc", Path: "/api/v0/generic/multi?ui=userglobal,webui,hybridcfg,aura,trafficprio,user,tam,pcp,time,usb,filter_profile,userticket"},

		// Dino-Endpunkte (neuer API-Zweig, nicht Teil von multi?ui=)
		{Name: "dino_configflags", Path: "/api/v0/dino/configflags"},
		{Name: "dino_eventlog", Path: "/api/v0/dino/eventlog"},
		{Name: "dino_internet_ruleset", Path: "/api/v0/dino/kisi/internetRuleset"},
		{Name: "dino_net_app", Path: "/api/v0/dino/kisi/netApp"},
		{Name: "dino_kids_timer", Path: "/api/v0/dino/timermix/KidsTimer"},
		{Name: "dino_wlan_timer", Path: "/api/v0/dino/timermix/WLANTimer"},
	}
}

// Run blockiert und pollt bis ctx abgebrochen wird. Erster Lauf sofort.
// Bei Login-Fehler wird sofort gestoppt und keine weiteren Zyklen ausgefuehrt.
func (s *Scraper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	s.scrapeAll(ctx)
	for {
		if halted, _ := s.Halted(); halted {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.scrapeAll(ctx)
		}
	}
}

func (s *Scraper) scrapeAll(ctx context.Context) {
	// Gemeinsamer Login VOR den parallelen Endpoint-Requests, damit bei
	// falschen Zugangsdaten nicht 10 Logins parallel gegen die Blocktime
	// laufen. Schlägt der Login fehl, wird sofort gestoppt.
	if err := s.client.Login(); err != nil {
		if errors.Is(err, ErrLoginFailed) {
			s.halt(err.Error())
		}
		return
	}

	var wg sync.WaitGroup
	for _, ep := range s.endpoints {
		wg.Add(1)
		go func(ep Endpoint) {
			defer wg.Done()
			s.scrapeOne(ctx, ep)
		}(ep)
	}
	wg.Wait()
}

func (s *Scraper) scrapeOne(ctx context.Context, ep Endpoint) {
	data, err := s.client.Get(ep.Path)
	snap := Snapshot{Time: time.Now()}
	if err != nil {
		snap.Ok = false
		snap.Error = err.Error()
		// Auch bei Login-Fehler waehrend eines Get (z.B. SID abgelaufen +
		// Re-Login schlaegt fehl) sofort stoppen.
		if errors.Is(err, ErrLoginFailed) {
			s.halt(err.Error())
		}
		s.mu.Lock()
		s.snapshots[ep.Name] = snap
		s.mu.Unlock()
		go s.persistSnapshot(ctx, ep.Name, snap)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// multi?ui=-Responses enthalten mehrere Module auf oberster Ebene.
	// Diese werden in eigene Snapshots aufgetrennt, damit Dashboard und
	// Prometheus-Metrics einheitlich pro Modul arbeiten koennen.
	if isMultiPath(ep.Path) {
		modules, ok := splitMultiResponse(data)
		if !ok {
			errSnap := Snapshot{
				Time:  time.Now(),
				Ok:    false,
				Error: "multi-antwort konnte nicht in module zerlegt werden",
				Data:  data,
			}
			s.snapshots[ep.Name] = errSnap
			go s.persistSnapshot(ctx, ep.Name, errSnap)
			return
		}
		now := time.Now()
		for _, mod := range modules {
			modSnap := Snapshot{Time: now, Ok: true, Data: mod.Data}
			s.snapshots[mod.Name] = modSnap
			go s.persistSnapshot(ctx, mod.Name, modSnap)
		}
		return
	}

	snap = Snapshot{Time: time.Now(), Ok: true, Data: data}
	s.snapshots[ep.Name] = snap
	go s.persistSnapshot(ctx, ep.Name, snap)

	// Eventlog wird gesondert verarbeitet: in Postgres gespeichert und an
	// Syslog weitergeleitet, statt als Prometheus-Metrik ausgegeben zu werden.
	if ep.Name == "dino_eventlog" {
		go s.processEventlog(ctx, data)
	}
}

// persistSnapshot schreibt den Snapshot asynchron in den Snapshot-Store.
// Fehler werden nur geloggt, damit die DB bei Problemen den Scraper nicht
// blockiert.
func (s *Scraper) persistSnapshot(ctx context.Context, module string, snap Snapshot) {
	if s.snapstore == nil {
		return
	}
	storeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.snapstore.Save(storeCtx, module, snap.Ok, snap.Data); err != nil {
		log.Printf("snapshot store %s: %v", module, err)
	}
}

func (s *Scraper) processEventlog(ctx context.Context, data []byte) {
	entries, err := eventlog.ParseEntries(data)
	if err != nil {
		log.Printf("eventlog parse: %v", err)
		return
	}
	if len(entries) == 0 {
		return
	}

	eventCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if s.store != nil {
		if err := s.store.SaveEntries(eventCtx, entries); err != nil {
			log.Printf("eventlog store: %v", err)
			return
		}
	}

	if s.store != nil && s.syslog != nil {
		unsent, err := s.store.UnsentEntries(eventCtx)
		if err != nil {
			log.Printf("eventlog unsent: %v", err)
			return
		}
		if len(unsent) == 0 {
			return
		}
		s.mu.RLock()
		boxSnap := s.snapshots["box"]
		s.mu.RUnlock()
		meta := eventlog.ExtractMeta(boxSnap.Data, s.boxHost)
		sent := s.syslog.Dispatch(unsent, s.boxHost, meta)
		if err := s.store.MarkSent(eventCtx, sent); err != nil {
			log.Printf("eventlog mark sent: %v", err)
		}
	}
}

func isMultiPath(path string) bool {
	return strings.Contains(path, "/api/v0/generic/multi?ui=")
}

// splitMultiResponse zerlegt ein multi?ui=JSON in seine Module.
// Es liefert ok=true, wenn das JSON ein Objekt mit mindestens einem
// Top-Level-Key ist.
type modulePart struct {
	Name string
	Data json.RawMessage
}

func splitMultiResponse(data json.RawMessage) ([]modulePart, bool) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, false
	}
	if len(obj) == 0 {
		return nil, false
	}
	parts := make([]modulePart, 0, len(obj))
	for k, v := range obj {
		parts = append(parts, modulePart{Name: k, Data: v})
	}
	return parts, true
}

// halt markiert den Scraper als gestoppt; weitere Zyklen werden nicht mehr
// ausgefuehrt. Ein Neustart erfordert einen Prozessneustart.
func (s *Scraper) halt(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.halted {
		return
	}
	s.halted = true
	s.haltReason = reason
	s.haltedAt = time.Now()
	log.Printf("scraper gestoppt: %s", reason)
}

// Halted meldet, ob der Scraper wegen eines Login-Fehlers gestoppt wurde.
func (s *Scraper) Halted() (bool, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.halted, s.haltReason
}

func (s *Scraper) Endpoints() []Endpoint {
	return s.endpoints
}

// SnapshotNames liefert eine sortierte Liste aller verfuegbaren Snapshot-Namen.
// Fuer multi?ui=-Endpunkte sind das die aufgetrennten Modulnamen.
func (s *Scraper) SnapshotNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.snapshots))
	for k := range s.snapshots {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func (s *Scraper) Snapshot(name string) (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snapshots[name]
	return snap, ok
}

// All liefert eine Kopie aller aktuellen Snapshots.
func (s *Scraper) All() map[string]Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]Snapshot, len(s.snapshots))
	for k, v := range s.snapshots {
		out[k] = v
	}
	return out
}
