package metrics

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"fritte/internal/fritz"
)

// Handler registriert sich unter /metrics und liefert alle verfuegbaren
// FRITZ!Box-Werte als Prometheus-Textformat aus.
func Handler(scraper *fritz.Scraper) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writeMetrics(w, scraper)
	}
}

func writeMetrics(w http.ResponseWriter, scraper *fritz.Scraper) {
	snaps := scraper.All()
	names := scraper.SnapshotNames()

	// Hilfsmetriken fuer den Scraping-Zustand.
	fmt.Fprintln(w, "# HELP fritte_scrape_success 1 wenn der letzte Scraping-Lauf fuer ein Modul erfolgreich war, sonst 0.")
	fmt.Fprintln(w, "# TYPE fritte_scrape_success gauge")
	fmt.Fprintln(w, "# HELP fritte_scrape_timestamp_seconds Zeitstempel des letzten Scraping-Laufs fuer ein Modul.")
	fmt.Fprintln(w, "# TYPE fritte_scrape_timestamp_seconds gauge")

	for _, name := range names {
		snap := snaps[name]
		if snap.Ok {
			fmt.Fprintf(w, "fritte_scrape_success{module=%q} 1\n", name)
		} else {
			fmt.Fprintf(w, "fritte_scrape_success{module=%q} 0\n", name)
		}
		fmt.Fprintf(w, "fritte_scrape_timestamp_seconds{module=%q} %.3f\n", name, float64(snap.Time.UnixNano())/1e9)
	}

	// Metriken aus den einzelnen Modulen generieren.
	// Eventlog wird ausgeschlossen, weil es als Syslog-Nachricht weitergeleitet
	// und in Postgres gespeichert wird.
	for _, name := range names {
		if name == "dino_eventlog" {
			continue
		}
		snap := snaps[name]
		if !snap.Ok {
			continue
		}
		base := metricNamePrefix(name)
		writeValueMetrics(w, base, snap.Data, nil, name)
	}
}

// writeValueMetrics wandelt einen JSON-Wert in Prometheus-Metriken um.
// prefix ist der aktuelle Metrik-Praefix, labels die bisher gesammelten Labels.
func writeValueMetrics(w http.ResponseWriter, prefix string, raw []byte, labels map[string]string, module string) {
	if isScalar(raw) {
		val, ok := scalarValue(raw)
		if !ok {
			return
		}
		// Nicht-numerische Skalare (z.B. reiner String) werden als Info-Metrik ausgegeben.
		if math.IsNaN(val) {
			fmt.Fprintf(w, "%s_info{%svalue=%q} 1\n", prefix, formatLabels(labels), string(raw))
			return
		}
		fmt.Fprintf(w, "%s{%s} %g\n", prefix, formatLabels(labels), val)
		return
	}

	// Array: Index als Label, Elemente rekursiv ausgeben.
	var arr []interface{}
	if err := json.Unmarshal(raw, &arr); err == nil {
		for i, elem := range arr {
			elemRaw, _ := json.Marshal(elem)
			childLabels := cloneLabels(labels)
			childLabels["index"] = strconv.Itoa(i)
			// Wenn ein Element ein Objekt mit UID oder name ist, sollen diese Labels direkt
			// an der Metrik erhaeltlich sein, nicht als Praefix.
			if isObject(elemRaw) {
				writeObjectMetrics(w, prefix, elemRaw, childLabels, module)
			} else {
				writeValueMetrics(w, prefix, elemRaw, childLabels, module)
			}
		}
		return
	}

	// Objekt: Felder verarbeiten.
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err == nil {
		writeObjectFields(w, prefix, obj, labels, module)
		return
	}
}

func writeObjectMetrics(w http.ResponseWriter, prefix string, raw []byte, labels map[string]string, module string) {
	var obj map[string]interface{}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return
	}
	writeObjectFields(w, prefix, obj, labels, module)
}

func writeObjectFields(w http.ResponseWriter, prefix string, obj map[string]interface{}, labels map[string]string, module string) {
	// UID/name/mac/etc. aus dem Objekt extrahieren und als Labels verwenden,
	// damit die Metriken identifizierbar bleiben.
	idLabels := cloneLabels(labels)
	for _, key := range []string{"UID", "uid", "name", "Name", "mac", "MAC"} {
		if v, ok := obj[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				idLabels[strings.ToLower(key)] = s
			}
		}
	}

	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		val := obj[key]
		valRaw, _ := json.Marshal(val)
		childPrefix := prefix + "_" + metricNamePart(key)

		// UID/name/mac nicht nochmal als eigene Metrik ausgeben, sondern nur als Label.
		if isIdentifierKey(key) {
			continue
		}

		if isScalar(valRaw) {
			sv, ok := scalarValue(valRaw)
			if !ok {
				continue
			}
			if math.IsNaN(sv) {
				// Info-Label-Wert
				infoLabels := cloneLabels(idLabels)
				infoLabels[key] = string(valRaw)
				fmt.Fprintf(w, "%s_info{%s} 1\n", prefix, formatLabels(infoLabels))
				continue
			}
			fmt.Fprintf(w, "%s{%s} %g\n", childPrefix, formatLabels(idLabels), sv)
			continue
		}

		if isObject(valRaw) {
			// Geschachteltes Objekt: entweder neuer Praefix oder als Labels behandeln,
			// wenn es nur aus bekannten ID-Feldern besteht.
			var childObj map[string]interface{}
			if err := json.Unmarshal(valRaw, &childObj); err == nil && len(childObj) > 0 {
				if allIdentifierKeys(childObj) {
					for k, v := range childObj {
						if s, ok := v.(string); ok {
							idLabels[strings.ToLower(k)] = s
						}
					}
					continue
				}
				writeObjectMetrics(w, childPrefix, valRaw, idLabels, module)
			}
			continue
		}

		if isArray(valRaw) {
			writeValueMetrics(w, childPrefix, valRaw, idLabels, module)
			continue
		}
	}
}

func isScalar(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	r := strings.TrimSpace(string(raw))
	if len(r) == 0 {
		return false
	}
	// String, Zahl, bool, null
	if (r[0] == '"' && r[len(r)-1] == '"') ||
		(r == "true") || (r == "false") || (r == "null") ||
		(r[0] == '-' || (r[0] >= '0' && r[0] <= '9')) {
		return true
	}
	return false
}

func isObject(raw []byte) bool {
	trim := strings.TrimSpace(string(raw))
	return len(trim) > 0 && trim[0] == '{'
}

func isArray(raw []byte) bool {
	trim := strings.TrimSpace(string(raw))
	return len(trim) > 0 && trim[0] == '['
}

func scalarValue(raw []byte) (float64, bool) {
	r := strings.TrimSpace(string(raw))
	if r == "null" {
		return math.NaN(), true
	}
	if r == "true" {
		return 1, true
	}
	if r == "false" {
		return 0, true
	}
	if len(r) >= 2 && r[0] == '"' && r[len(r)-1] == '"' {
		inner := r[1 : len(r)-1]
		// "1", "0", "-1", "123" etc. als Zahlen interpretieren
		if v, err := strconv.ParseFloat(inner, 64); err == nil {
			return v, true
		}
		// Status-Werte wie "connected" / "disabled"
		if v, ok := stateValue(inner); ok {
			return v, true
		}
		return math.NaN(), true
	}
	if v, err := strconv.ParseFloat(r, 64); err == nil {
		return v, true
	}
	return math.NaN(), false
}

func stateValue(s string) (float64, bool) {
	switch strings.ToLower(s) {
	case "connected", "active", "enabled", "on", "ok", "up", "yes", "1":
		return 1, true
	case "disconnected", "disabled", "inactive", "off", "error", "down", "no", "0":
		return 0, true
	}
	return math.NaN(), false
}

func isIdentifierKey(key string) bool {
	lower := strings.ToLower(key)
	return lower == "uid" || lower == "name" || lower == "mac" || lower == "id"
}

func allIdentifierKeys(obj map[string]interface{}) bool {
	if len(obj) == 0 {
		return false
	}
	for k := range obj {
		if !isIdentifierKey(k) {
			return false
		}
	}
	return true
}

func metricNamePrefix(name string) string {
	return "fritte_" + metricNamePart(name)
}

var nonMetricChar = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

func metricNamePart(s string) string {
	// Umlaute etc. ersetzen
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "ä", "ae")
	s = strings.ReplaceAll(s, "ö", "oe")
	s = strings.ReplaceAll(s, "ü", "ue")
	s = strings.ReplaceAll(s, "ß", "ss")
	s = nonMetricChar.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	return s
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", metricNamePart(k), labels[k]))
	}
	return strings.Join(parts, ",") + ","
}

func cloneLabels(src map[string]string) map[string]string {
	out := make(map[string]string, len(src)+2)
	for k, v := range src {
		out[k] = v
	}
	return out
}
