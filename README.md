# fritte

Scrapt Daten einer FRITZ!Box ueber die internen `/api/v0/*`-Endpunkte und stellt sie als kleines Dashboard (HTML), JSON-API sowie Prometheus- und Syslog-Integration bereit.

**Getestet mit:** FRITZ!OS 8.25 auf einer FRITZ!Box 6660 Cable. Andere FRITZ!OS-Versionen oder Modelle koennen abweichende Endpunkte oder Antwortformate haben.

## Endpunkte

Aktuell angebunden (unter `https://fritz.box`):

### Multi-Module (`/api/v0/generic/multi?ui=...`)

- `multi_box`: `box,boxusers,connections,cpu,dect,eth_ports,landevice,nexus`
- `multi_system`: `plc,power,providerlist,uimodlogic,updatecheck,vpn,wlan_light,webdavclient,nqos`
- `multi_telecom`: `mobiled,sip,telcfg,umts,budget,ddns,dnscfg,dnsserver,emailnotify`
- `multi_inet`: `forwardrules,igdforwardrules,inetstat,ipv6,ipv6firewall,jasonii,myfritzdevice,remoteman`
- `multi_misc`: `userglobal,webui,hybridcfg,aura,trafficprio,user,tam,pcp,time,usb,filter_profile,userticket`

### Dino-Endpunkte (`/api/v0/dino/*`)

- `/api/v0/dino/configflags`
- `/api/v0/dino/eventlog`
- `/api/v0/dino/kisi/internetRuleset`
- `/api/v0/dino/kisi/netApp`
- `/api/v0/dino/timermix/KidsTimer`
- `/api/v0/dino/timermix/WLANTimer`

Weitere Endpunkte lassen sich in `internal/fritz/scraper.go` (`DefaultEndpoints`) ergaenzen.

## Authentifizierung

Der Container loggt sich selbst per Challenge-Response (`login_sid.lua?version=2`, PBKDF2 ab FRITZ!OS 7.24, sonst MD5/UTF-16LE) ein und verlaengert die SID automatisch. Zugangsdaten werden ueber Env-Variablen bereitgestellt, niemals ins Repo eingetragen.

## Konfiguration (Env)

| Variable          | Default             | Bedeutung                                  |
|-------------------|---------------------|--------------------------------------------|
| `FRITZ_HOST`      | `https://fritz.box` | Basis-URL der Box                          |
| `FRITZ_USERNAME`  | –                   | Benutzername des Box-Logins (Pflicht)      |
| `FRITZ_PASSWORD`  | –                   | Passwort des Box-Logins (Pflicht)          |
| `POLL_INTERVAL`   | `30s`               | Scraping-Intervall                          |
| `DASHBOARD_ADDR`  | `:8080`             | Listen-Adresse des Dashboard-Servers       |
| `DATABASE_URL`    | –                   | PostgreSQL-DSN fuer Eventlog-Speicherung   |
| `SYSLOG_HOST`     | –                   | Syslog-Server-Host (aktiviert Versand)     |
| `SYSLOG_PORT`     | `514`               | Syslog-Server-Port                          |
| `SYSLOG_PROTOCOL` | `udp`               | `udp` oder `tcp`                           |

## Start

Zugangsdaten ohne Eintrag ins Repo bereitstellen, z. B. per `.env` (Vorlage: `.env.example`):

```sh
cp .env.example .env
# .env editieren: FRITZ_USERNAME / FRITZ_PASSWORD eintragen
```

Dann:

```sh
docker compose up --build
```

Dashboard: <http://localhost:8080>

## Syslog / Eventlog

`/api/v0/dino/eventlog` wird **nicht** als Prometheus-Metrik ausgegeben. Stattdessen kann fritte die Eintraege:

1. in PostgreSQL speichern (Tabelle `eventlog`, inklusive `sent_to_syslog`-Flag),
2. aktiv per UDP/TCP an einen Syslog-Server weiterleiten.

Aktiviert wird das durch `DATABASE_URL` und `SYSLOG_HOST`. Duplikate werden ueber einen Hash aus `time+group+id+msg` erkannt.

## HTTP-Endpunkte

- `GET /` – HTML-Dashboard
- `GET /metrics` – Prometheus-Metriken aller gescrapten FRITZ!Box-Werte
- `GET /api/snapshot` – JSON aller aktuellen Snapshots
- `GET /api/{endpoint}` – JSON des einzelnen Endpunkts (z. B. `/api/cpu`)

## Lokaler Build (ohne Docker)

```sh
go build ./...
go run ./cmd/fritte
```