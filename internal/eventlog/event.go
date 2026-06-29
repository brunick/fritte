package eventlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Entry ist ein einzelner Eventlog-Eintrag der FRITZ!Box.
type Entry struct {
	Time   string `json:"time"`
	Group  string `json:"group"`
	ID     int    `json:"id"`
	Msg    string `json:"msg"`
	Date   string `json:"date"`
	NoHelp bool   `json:"nohelp"`
}

// ParseEntries liest die JSON-Antwort von /api/v0/dino/eventlog ein.
func ParseEntries(raw []byte) ([]Entry, error) {
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("eventlog parse: %w", err)
	}
	return entries, nil
}

// Hash erzeugt einen stabilen Hash zur Duplikat-Erkennung.
func (e Entry) Hash() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%d|%s|%s|%t", e.Time, e.Group, e.ID, e.Msg, e.Date, e.NoHelp)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// SyslogMessage formatiert den Eintrag als RFC 3164 Syslog-Nachricht.
// severity: info=6; facility user=1.
func (e Entry) SyslogMessage(hostname, tag string, meta BoxMeta) string {
	pri := 1*8 + 6 // user.info

	msg := strings.ReplaceAll(e.Msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")

	timestamp := e.rfc3164Timestamp()
	if timestamp == "" {
		timestamp = time.Now().Format("Jan _2 15:04:05")
	}

	metaPart := ""
	if meta.Hostname != "" {
		metaPart += fmt.Sprintf(" host=\"%s\"", meta.Hostname)
	}
	if meta.Model != "" {
		metaPart += fmt.Sprintf(" model=\"%s\"", meta.Model)
	}
	if meta.FWVersion != "" {
		metaPart += fmt.Sprintf(" fwversion=\"%s\"", meta.FWVersion)
	}
	metaPart += fmt.Sprintf(" group=\"%s\" event_id=\"%d\"", e.Group, e.ID)
	metaPart = strings.TrimPrefix(metaPart, " ")

	return fmt.Sprintf("<%d>%s %s %s: [FRITZ!Box %s] %s", pri, timestamp, hostname, tag, metaPart, msg)
}

// rfc3164Timestamp parst date+time aus dem Eventlog und formatiert sie als
// RFC 3164 Timestamp ("Jun 29 17:06:53"). date hat das Format "dd.mm.yy".
func (e Entry) rfc3164Timestamp() string {
	t, err := time.Parse("02.01.06 15:04:05", e.Date+" "+e.Time)
	if err != nil {
		return ""
	}
	return t.Format("Jan _2 15:04:05")
}

// BoxMeta enthaelt Informationen ueber die FRITZ!Box, die in jede Syslog-Nachricht
// eingefuegt werden sollen.
type BoxMeta struct {
	Hostname  string
	Model     string
	FWVersion string
}
