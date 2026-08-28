package certs

// DNS-01 providers.
//
// Cloudflare is fully automatic. The manual provider prints the record and
// waits for the operator, so the same issuance code path works with ANY DNS
// host — the only difference is who creates the TXT record.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ── Cloudflare ───────────────────────────────────────────────

// CloudflareProvider publishes TXT records through the Cloudflare API.
//
// The token needs exactly one permission: Zone > DNS > Edit, scoped to the
// zone(s) you want certificates for. The panel says so in the UI, because a
// Global API Key would give the panel control of the entire account.
type CloudflareProvider struct {
	Token  string
	Client *http.Client
	// zoneCache avoids re-resolving the zone for every name in one order.
	zoneCache map[string]string
}

func NewCloudflareProvider(token string) *CloudflareProvider {
	return &CloudflareProvider{
		Token:     strings.TrimSpace(token),
		Client:    &http.Client{Timeout: 30 * time.Second},
		zoneCache: map[string]string{},
	}
}

func (c *CloudflareProvider) Name() string { return "Cloudflare" }

// cfAPIBase is a variable, not a constant, so tests can point the provider
// at a fake server instead of the real Cloudflare API.
var cfAPIBase = "https://api.cloudflare.com/client/v4"

type cfResp struct {
	Success bool            `json:"success"`
	Errors  []cfErr         `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (r cfResp) err() error {
	if r.Success {
		return nil
	}
	if len(r.Errors) == 0 {
		return fmt.Errorf("Cloudflare rejected the request")
	}
	msgs := make([]string, 0, len(r.Errors))
	for _, e := range r.Errors {
		msgs = append(msgs, fmt.Sprintf("%s (code %d)", e.Message, e.Code))
	}
	return fmt.Errorf("Cloudflare: %s", strings.Join(msgs, "; "))
}

func (c *CloudflareProvider) do(ctx context.Context, method, path string,
	body interface{}) (*cfResp, error) {

	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfAPIBase+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the Cloudflare API: %w", err)
	}
	defer res.Body.Close()

	var out cfResp
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("unreadable Cloudflare response (HTTP %d)", res.StatusCode)
	}
	// 403 with no error body usually means the token lacks DNS:Edit.
	if res.StatusCode == http.StatusForbidden && len(out.Errors) == 0 {
		return nil, fmt.Errorf("Cloudflare refused the token — it needs the Zone:DNS:Edit permission")
	}
	return &out, out.err()
}

// VerifyToken checks the token before it is stored, so a typo is reported in
// the form rather than surfacing three minutes into an issuance.
func (c *CloudflareProvider) VerifyToken(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil)
	return err
}

// zoneID finds the zone that owns a name.
//
// It walks up the labels because the zone is not always the last two:
// "a.b.example.co.uk" lives in "example.co.uk", and a delegated subdomain
// may itself be a zone. Asking Cloudflare for each candidate is the only
// reliable way; a public-suffix guess gets this wrong.
func (c *CloudflareProvider) zoneID(ctx context.Context, name string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	if id, ok := c.zoneCache[name]; ok {
		return id, nil
	}
	labels := strings.Split(name, ".")
	for i := 0; i < len(labels)-1; i++ {
		cand := strings.Join(labels[i:], ".")
		res, err := c.do(ctx, http.MethodGet, "/zones?name="+cand, nil)
		if err != nil {
			return "", err
		}
		var zones []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(res.Result, &zones); err != nil {
			return "", err
		}
		if len(zones) > 0 {
			c.zoneCache[name] = zones[0].ID
			return zones[0].ID, nil
		}
	}
	return "", fmt.Errorf("no Cloudflare zone found for %s — is the domain in this account, and does the token cover it?", name)
}

func (c *CloudflareProvider) Present(ctx context.Context, name, value string) error {
	zone, err := c.zoneID(ctx, name)
	if err != nil {
		return err
	}
	// Remove a stale record with the same name first. A previous failed run
	// can leave one behind, and two conflicting TXT values make the CA see
	// the wrong one.
	if err := c.removeMatching(ctx, zone, name, ""); err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, "/zones/"+zone+"/dns_records", map[string]interface{}{
		"type": "TXT", "name": name, "content": value,
		// 60s is Cloudflare's minimum and keeps propagation fast.
		"ttl": 60,
	})
	return err
}

func (c *CloudflareProvider) CleanUp(ctx context.Context, name, value string) error {
	zone, err := c.zoneID(ctx, name)
	if err != nil {
		return err
	}
	return c.removeMatching(ctx, zone, name, value)
}

// removeMatching deletes TXT records for name. An empty value deletes all of
// them; otherwise only the exact match is removed.
func (c *CloudflareProvider) removeMatching(ctx context.Context, zone, name, value string) error {
	res, err := c.do(ctx, http.MethodGet,
		"/zones/"+zone+"/dns_records?type=TXT&name="+name, nil)
	if err != nil {
		return err
	}
	var recs []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(res.Result, &recs); err != nil {
		return err
	}
	for _, r := range recs {
		if value != "" && r.Content != value {
			continue
		}
		if _, err := c.do(ctx, http.MethodDelete,
			"/zones/"+zone+"/dns_records/"+r.ID, nil); err != nil {
			return err
		}
	}
	return nil
}

// ── Manual ───────────────────────────────────────────────────

// ManualProvider hands the record to a human and waits.
//
// This is what makes the feature usable for someone whose DNS is not on
// Cloudflare. The issuance code does not change at all: it still calls
// Present and then waits for propagation, which is exactly the right
// behaviour when a person is typing the record into a web console.
type ManualProvider struct {
	// Instruct is called with the record that must be created. It should
	// block until the operator says they have done it (the CLI waits for
	// Enter; the web flow resolves a channel).
	Instruct func(name, value string) error
	// Cleanup is called afterwards so the UI can tell the operator the
	// record may be deleted. Optional.
	Cleanup func(name, value string)
}

func (m *ManualProvider) Name() string { return "manual" }

func (m *ManualProvider) Present(ctx context.Context, name, value string) error {
	if m.Instruct == nil {
		return fmt.Errorf("the manual DNS flow has no way to reach the operator")
	}
	return m.Instruct(name, value)
}

func (m *ManualProvider) CleanUp(ctx context.Context, name, value string) error {
	if m.Cleanup != nil {
		m.Cleanup(name, value)
	}
	return nil
}
