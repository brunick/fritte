package fritz

import (
	"crypto/md5"
	"crypto/pbkdf2"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const loginPath = "/login_sid.lua?version=2"

// ErrLoginFailed signalisiert fehlgeschlagene Zugangsdaten. Der Scraper
// stoppt daraufhin sofort, statt weiter gegen die Blocktime anzulaufen.
var ErrLoginFailed = errors.New("login fehlgeschlagen: ungueltige Zugangsdaten")

// Authenticator beschafft und verlaengert die AVM-SID ueber das
// Challenge-Response-Verfahren (PBKDF2 ab FRITZ!OS 7.24, sonst MD5/UTF-16LE).
type Authenticator struct {
	baseURL  string
	username string
	password string
	http     *http.Client

	mu      sync.Mutex
	sid     string
	validAt time.Time
}

func NewAuthenticator(baseURL, username, password string, hc *http.Client) *Authenticator {
	return &Authenticator{baseURL: baseURL, username: username, password: password, http: hc}
}

type sessionInfo struct {
	XMLName   xml.Name `xml:"SessionInfo"`
	SID       string   `xml:"SID"`
	Challenge string   `xml:"Challenge"`
	BlockTime int      `xml:"BlockTime"`
	Rights    string   `xml:"Rights"`
}

// SID liefert eine gueltige SID und loggt sich bei Bedarf (erneut) ein.
func (a *Authenticator) SID() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sid != "" && a.sid != "0000000000000000" && time.Since(a.validAt) < 15*time.Minute {
		return a.sid, nil
	}
	return a.loginLocked()
}

// Invalidate erzwingt einen Re-Login beim naechsten SID-Aufruf (z.B. nach 403).
func (a *Authenticator) Invalidate() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sid = ""
	a.validAt = time.Time{}
}

func (a *Authenticator) loginLocked() (string, error) {
	info, err := a.fetchSessionInfo()
	if err != nil {
		return "", fmt.Errorf("session info holen: %w", err)
	}
	// Bereits gueltige SID uebernehmen (z.B. bei Wiederverwendung).
	if info.SID != "" && info.SID != "0000000000000000" && strings.TrimSpace(info.Rights) != "" {
		a.sid = info.SID
		a.validAt = time.Now()
		return a.sid, nil
	}

	response, err := computeResponse(info.Challenge, a.password)
	if err != nil {
		return "", fmt.Errorf("response berechnen: %w", err)
	}
	info, err = a.postLogin(response)
	if err != nil {
		return "", err
	}
	if info.SID == "" || info.SID == "0000000000000000" {
		return "", fmt.Errorf("%w (blocktime=%ds)", ErrLoginFailed, info.BlockTime)
	}
	a.sid = info.SID
	a.validAt = time.Now()
	return a.sid, nil
}

func (a *Authenticator) fetchSessionInfo() (*sessionInfo, error) {
	req, err := http.NewRequest(http.MethodGet, a.baseURL+loginPath, nil)
	if err != nil {
		return nil, err
	}
	res, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var info sessionInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("session info parsen: %w", err)
	}
	return &info, nil
}

func (a *Authenticator) postLogin(response string) (*sessionInfo, error) {
	form := url.Values{}
	form.Set("username", a.username)
	form.Set("response", response)
	req, err := http.NewRequest(http.MethodPost, a.baseURL+loginPath, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var info sessionInfo
	if err := xml.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("login-antwort parsen: %w", err)
	}
	return &info, nil
}

// computeResponse waehlt abhaengig vom Challenge-Präfix das passende Verfahren.
func computeResponse(challenge, password string) (string, error) {
	if strings.HasPrefix(challenge, "2$") {
		return pbkdf2Response(challenge, password)
	}
	return md5Response(challenge, password), nil
}

// pbkdf2Response implementiert das PBKDF2-Verfahren (FRITZ!OS 7.24+).
// Challenge-Format: 2$iter1$salt1$iter2$salt2  (Salzes hex-kodiert).
func pbkdf2Response(challenge, password string) (string, error) {
	parts := strings.SplitN(challenge, "$", 5)
	if len(parts) != 5 {
		return "", fmt.Errorf("ungueltige pbkdf2-challenge: %q", challenge)
	}
	iter1, err := strconv.Atoi(parts[1])
	if err != nil {
		return "", fmt.Errorf("iter1 parsen: %w", err)
	}
	salt1, err := hex.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("salt1 dekodieren: %w", err)
	}
	iter2, err := strconv.Atoi(parts[3])
	if err != nil {
		return "", fmt.Errorf("iter2 parsen: %w", err)
	}
	salt2, err := hex.DecodeString(parts[4])
	if err != nil {
		return "", fmt.Errorf("salt2 dekodieren: %w", err)
	}

	hash1, err := pbkdf2.Key(sha256.New, password, salt1, iter1, 32)
	if err != nil {
		return "", fmt.Errorf("pbkdf2 schritt 1: %w", err)
	}
	hash2, err := pbkdf2.Key(sha256.New, string(hash1), salt2, iter2, 32)
	if err != nil {
		return "", fmt.Errorf("pbkdf2 schritt 2: %w", err)
	}
	return parts[4] + "$" + hex.EncodeToString(hash2), nil
}

// md5Response implementiert das Legacy-MD5-Verfahren (UTF-16LE, Codepoint > 0xFF -> '.').
func md5Response(challenge, password string) string {
	utf16 := toUTF16LEASCII(challenge + "-" + password)
	sum := md5.Sum(utf16)
	return challenge + "-" + hex.EncodeToString(sum[:])
}

func toUTF16LEASCII(s string) []byte {
	b := make([]byte, 0, len(s)*2)
	for _, r := range s {
		if r > 0xFF {
			r = '.'
		}
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}
