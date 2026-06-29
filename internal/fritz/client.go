package fritz

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client spricht die (undokumentierten) /api/v0/*-Endpunkte der FRITZ!Box an.
// Das selbstsignierte Zertifikat der Box wird akzeptiert (entspricht curl -k).
type Client struct {
	baseURL string
	auth    *Authenticator
	http    *http.Client
}

func NewClient(baseURL, username, password string) *Client {
	transport := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		MaxIdleConnsPerHost: 8,
	}
	hc := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		auth:    NewAuthenticator(strings.TrimRight(baseURL, "/"), username, password, hc),
		http:    hc,
	}
}

// Login erzwingt den initialen SID-Bezug; danach passiert das automatisch.
func (c *Client) Login() error {
	_, err := c.auth.SID()
	return err
}

// Get holt einen Endpunkt als valides JSON. Bei 403 wird die SID einmal
// invalidiert und mit frischem Login erneut versucht.
func (c *Client) Get(path string) (json.RawMessage, error) {
	return c.getWithRetry(path, true)
}

func (c *Client) getWithRetry(path string, allowRetry bool) (json.RawMessage, error) {
	sid, err := c.auth.SID()
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req, sid)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusForbidden && allowRetry {
		c.auth.Invalidate()
		return c.getWithRetry(path, false)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", path, res.StatusCode)
	}

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("%s: antwort ist kein gueltiges JSON", path)
	}
	return json.RawMessage(body), nil
}

// setHeaders spiegelt die Header aus der mitgelieferten request-Datei.
func (c *Client) setHeaders(req *http.Request, sid string) {
	h := req.Header
	h.Set("Authorization", "AVM-SID "+sid)
	h.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:152.0) Gecko/20100101 Firefox/152.0")
	h.Set("Accept", "*/*")
	h.Set("Accept-Language", "de-DE,en-US;q=0.9,en;q=0.8")
	h.Set("Referer", c.baseURL+"/")
	h.Set("Content-Type", "application/json")
	h.Set("DNT", "1")
	h.Set("Sec-GPC", "1")
	h.Set("Connection", "keep-alive")
	h.Set("Sec-Fetch-Dest", "empty")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Site", "same-origin")
	h.Set("Priority", "u=4")
	h.Set("Pragma", "no-cache")
	h.Set("Cache-Control", "no-cache")
}
