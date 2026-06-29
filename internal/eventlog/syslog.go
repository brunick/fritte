package eventlog

import (
	"fmt"
	"log"
	"net"
	"strings"
	"time"
)

// Sender konfiguriert den Syslog-Versand.
type Sender struct {
	Host     string
	Port     string
	Protocol string // "udp" oder "tcp"
	Tag      string
	Timeout  time.Duration
}

// NewSender erzeugt einen Sender mit Defaults.
func NewSender(host, port, protocol string) *Sender {
	if host == "" {
		return nil
	}
	if port == "" {
		port = "514"
	}
	if protocol != "tcp" {
		protocol = "udp"
	}
	return &Sender{
		Host:     host,
		Port:     port,
		Protocol: protocol,
		Tag:      "fritte",
		Timeout:  5 * time.Second,
	}
}

// Send verschickt eine Nachricht an den Syslog-Server.
func (s *Sender) Send(msg string) error {
	if s == nil {
		return fmt.Errorf("syslog sender nicht konfiguriert")
	}
	addr := net.JoinHostPort(s.Host, s.Port)

	if s.Protocol == "udp" {
		conn, err := net.Dial("udp", addr)
		if err != nil {
			return fmt.Errorf("syslog udp dial %s: %w", addr, err)
		}
		defer conn.Close()
		conn.SetWriteDeadline(time.Now().Add(s.Timeout))
		if _, err := conn.Write([]byte(msg)); err != nil {
			return fmt.Errorf("syslog udp write: %w", err)
		}
		return nil
	}

	conn, err := net.DialTimeout("tcp", addr, s.Timeout)
	if err != nil {
		return fmt.Errorf("syslog tcp dial %s: %w", addr, err)
	}
	defer conn.Close()
	conn.SetWriteDeadline(time.Now().Add(s.Timeout))
	// RFC 5424: jede Nachricht wird mit \n abgeschlossen.
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	if _, err := conn.Write([]byte(msg)); err != nil {
		return fmt.Errorf("syslog tcp write: %w", err)
	}
	return nil
}

// Dispatch sendet alle noch nicht versandten Eintraege und gibt die
// Datenbank-IDs der erfolgreich uebertragenen Eintraege zurueck.
func (s *Sender) Dispatch(entries []EntryWithID, hostname string, meta BoxMeta) []int64 {
	if s == nil {
		return nil
	}
	var sent []int64
	for _, e := range entries {
		msg := e.Entry.SyslogMessage(hostname, s.Tag, meta)
		if err := s.Send(msg); err != nil {
			log.Printf("syslog send failed (id=%d): %v", e.DBID, err)
			continue
		}
		sent = append(sent, e.DBID)
	}
	return sent
}
