package eventlog

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// ExtractMeta versucht, Box-Metadaten aus dem gescrapten box-Snapshot zu ziehen.
// Das Snapshot-Argument muss Ok=true haben; sonst wird ein leerer Wert zurueckgegeben.
func ExtractMeta(snap []byte, defaultHostname string) BoxMeta {
	if len(snap) == 0 {
		return BoxMeta{Hostname: defaultHostname}
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(snap, &obj); err != nil {
		slog.Warn("box meta parse failed", "err", err)
		return BoxMeta{Hostname: defaultHostname}
	}

	meta := BoxMeta{Hostname: defaultHostname}
	if v, ok := obj["FriendlyName"]; ok {
		meta.Hostname = stringValue(v)
	}
	if meta.Hostname == "" {
		if v, ok := obj["Name"]; ok {
			meta.Hostname = stringValue(v)
		}
	}
	if v, ok := obj["ModelName"]; ok {
		meta.Model = stringValue(v)
	}
	if meta.Model == "" {
		if v, ok := obj["Model"]; ok {
			meta.Model = stringValue(v)
		}
	}
	if v, ok := obj["FirmwareVersion"]; ok {
		meta.FWVersion = stringValue(v)
	}
	if meta.FWVersion == "" {
		if v, ok := obj["firmware_version"]; ok {
			meta.FWVersion = stringValue(v)
		}
	}
	return meta
}

func stringValue(raw []byte) string {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return strings.Trim(string(raw), "\"")
	}
	return s
}
