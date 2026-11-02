package panel

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	cottenprobe "cottendns-go/probe"
	bolt "go.etcd.io/bbolt"
	masterprobe "masterdnsvpn-go/probe"
)

const altZone = "t.example.net"

func addAltZone(t *testing.T, s *Store) Settings {
	t.Helper()
	cfg, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.TunnelZones = []string{cfg.BaseZone, altZone}
	cfg.PrimaryZone = cfg.BaseZone
	if err = s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	cfg, _ = s.Settings()
	return cfg
}

func zonesPtr(z ...string) *[]string { return &z }

// subscriptionDomains decodes every profile in a subscription and returns the
// server domain each engine connects to.
func subscriptionDomains(t *testing.T, body []byte) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, line := range strings.Fields(string(body)) {
		scheme, encoded, _ := strings.Cut(line, "://")
		raw, err := base64.RawURLEncoding.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var v struct {
			Profile struct {
				Server struct{ Domain string }
			}
		}
		if err = json.Unmarshal(raw, &v); err != nil {
			t.Fatal(err)
		}
		out[scheme] = append(out[scheme], v.Profile.Server.Domain)
	}
	return out
}

func TestAccountTunnelZonesValidationAndPersistence(t *testing.T) {
	s := testStore(t)
	cfg := addAltZone(t, s)
	if _, err := s.Create(ClientInput{Name: "unknown", Enabled: true, TunnelZones: zonesPtr("x.example.com")}); err == nil {
		t.Fatal("accepted a domain that is not configured")
	}
	c, err := s.Create(ClientInput{Name: "restricted", Engines: "all", Enabled: true, TunnelZones: zonesPtr(" T.Example.NET. ", altZone)})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.TunnelZones.list(); !reflect.DeepEqual(got, []string{altZone}) {
		t.Fatal("domain list not normalized", got)
	}
	stored, _ := s.Get(c.ID)
	if stored != c {
		t.Fatal("stored account differs")
	}
	// Omitted preserves (older API callers, Telegram and the enable toggle).
	in := accountInput(stored)
	in.Name = "renamed"
	if stored, err = s.Edit(c.ID, in); err != nil || stored.TunnelZones.list()[0] != altZone {
		t.Fatal("edit without tunnel_zones changed the restriction", err, stored.TunnelZones)
	}
	in = accountInput(stored)
	in.TunnelZones = zonesPtr("nope.example.org")
	if _, err = s.Edit(c.ID, in); err == nil {
		t.Fatal("edit accepted an unconfigured domain")
	}
	in.TunnelZones = zonesPtr()
	if stored, err = s.Edit(c.ID, in); err != nil || stored.TunnelZones != "" {
		t.Fatal("empty list did not restore all domains", err)
	}
	if !reflect.DeepEqual(cfg.accountZones(stored), []string{stored.Zone, cfg.accountZone(stored, altZone)}) {
		t.Fatal("unrestricted account lost a domain")
	}
	// JSON keeps an array shape and old records without the field still load.
	raw, _ := json.Marshal(Client{ID: "x", TunnelZones: newZoneList([]string{"a.example.org", altZone})})
	if !bytes.Contains(raw, []byte(`"tunnel_zones":["a.example.org","t.example.net"]`)) {
		t.Fatal(string(raw))
	}
	raw, _ = json.Marshal(Client{ID: "x"})
	if bytes.Contains(raw, []byte("tunnel_zones")) {
		t.Fatal("unrestricted accounts must omit tunnel_zones", string(raw))
	}
	var legacy Client
	if err = json.Unmarshal([]byte(`{"id":"x","zone":"a.soar.example.org"}`), &legacy); err != nil || legacy.TunnelZones != "" {
		t.Fatal("legacy record", err)
	}
	if json.Unmarshal([]byte(`{"tunnel_zones":["a,b.example.org"]}`), &legacy) == nil {
		t.Fatal("accepted a comma inside a domain")
	}
}

func TestAccountTunnelZonesSubscriptionsAndAPI(t *testing.T) {
	b, s, h := testBrowser(t)
	b.login()
	cfg := addAltZone(t, s)
	if err := h.manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	var created Client
	if err := json.Unmarshal(b.request("/api/clients", "POST", map[string]any{"name": "only alt", "engines": "all", "enabled": true, "tunnel_zones": []string{altZone}}, 201), &created); err != nil {
		t.Fatal(err)
	}
	open, err := s.Create(ClientInput{Name: "everything", Engines: "all", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	b.request("/api/clients", "POST", map[string]any{"name": "bad", "enabled": true, "tunnel_zones": []string{"evil.example.com"}}, 400)

	var list struct {
		Clients []struct {
			ID          string   `json:"id"`
			TunnelZones []string `json:"tunnel_zones"`
			ConnectZone string   `json:"connect_zone"`
		} `json:"clients"`
		TunnelZones []string `json:"tunnel_zones"`
	}
	if err = json.Unmarshal(b.request("/api/clients", "GET", nil, 200), &list); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(list.TunnelZones, cfg.tunnelZones()) {
		t.Fatal("client list does not expose configured domains", list.TunnelZones)
	}
	for _, v := range list.Clients {
		switch v.ID {
		case created.ID:
			if !reflect.DeepEqual(v.TunnelZones, []string{altZone}) || !strings.HasSuffix(v.ConnectZone, "."+altZone) {
				t.Fatal("restricted account view", v)
			}
		case open.ID:
			if v.TunnelZones != nil || v.ConnectZone != open.Zone {
				t.Fatal("unrestricted account view", v)
			}
		}
	}

	sub := func(id string) map[string][]string {
		token, err := s.SubscriptionToken(id, false)
		if err != nil {
			t.Fatal(err)
		}
		return subscriptionDomains(t, b.request("/sub/"+token+"?format=plain", "GET", nil, 200))
	}
	restricted := sub(created.ID)
	for _, scheme := range []string{"soar", "cottendns", "masterdns"} {
		if len(restricted[scheme]) != 1 || !strings.HasSuffix(restricted[scheme][0], "."+altZone) {
			t.Fatalf("restricted %s profiles: %v", scheme, restricted[scheme])
		}
		for _, domain := range restricted[scheme] {
			if strings.HasSuffix(domain, "."+cfg.BaseZone) {
				t.Fatal("restricted subscription leaked the excluded domain")
			}
		}
	}
	if all := sub(open.ID); len(all["soar"]) != 2 || len(all["cottendns"]) != 2 || len(all["masterdns"]) != 2 {
		t.Fatal("unrestricted subscription lost a domain", all)
	}
	// Direct links use the first allowed domain even when it is not the primary.
	var links struct{ Links map[string]string }
	json.Unmarshal(b.request("/api/clients/"+created.ID+"/link", "GET", nil, 200), &links)
	for engine, link := range links.Links {
		for scheme, domains := range subscriptionDomains(t, []byte(link)) {
			if !strings.HasSuffix(domains[0], "."+altZone) {
				t.Fatal("direct link uses an excluded domain", engine, scheme, domains)
			}
		}
	}

	// Editing through the API changes access; omitting the field preserves it.
	edited, _ := s.Get(created.ID)
	b.request("/api/clients/"+created.ID, "PUT", map[string]any{"name": "only base", "engines": "all", "enabled": true, "revision": edited.Revision, "tunnel_zones": []string{cfg.BaseZone}}, 200)
	if got := sub(created.ID); len(got["soar"]) != 1 || !strings.HasSuffix(got["soar"][0], "."+cfg.BaseZone) {
		t.Fatal("edit did not move the account to the base domain", got)
	}
	edited, _ = s.Get(created.ID)
	b.request("/api/clients/"+created.ID, "PUT", map[string]any{"name": "toggle", "engines": "all", "enabled": false, "revision": edited.Revision}, 200)
	if edited, _ = s.Get(created.ID); edited.TunnelZones.list()[0] != cfg.BaseZone {
		t.Fatal("toggle without tunnel_zones dropped the restriction")
	}
}

func TestRemovingTunnelDomainsRespectsAccountRestrictions(t *testing.T) {
	b, s, h := testBrowser(t)
	b.login()
	cfg := addAltZone(t, s)
	third := "t.example.com"
	b.request("/api/tunnel-domains", "PUT", map[string]any{"zones": []string{cfg.BaseZone, altZone, third}, "primary": cfg.BaseZone}, 200)
	only, err := s.Create(ClientInput{Name: "only alt", Engines: "all", Enabled: true, TunnelZones: zonesPtr(altZone)})
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Create(ClientInput{Name: "alt and third", Engines: "all", Enabled: true, TunnelZones: zonesPtr(altZone, third)})
	if err != nil {
		t.Fatal(err)
	}
	open, _ := s.Create(ClientInput{Name: "open", Engines: "all", Enabled: true})
	// Removing the only domain of a restricted account is refused everywhere.
	raw := b.request("/api/tunnel-domains", "PUT", map[string]any{"zones": []string{cfg.BaseZone, third}, "primary": cfg.BaseZone}, 400)
	if !strings.Contains(string(raw), "only alt") {
		t.Fatal("error does not name the stranded account", string(raw))
	}
	after, _ := s.Settings()
	if !reflect.DeepEqual(after.tunnelZones(), []string{cfg.BaseZone, altZone, third}) {
		t.Fatal("refused change was partially saved")
	}
	// Removing one of several domains prunes it from the restricted accounts.
	b.request("/api/tunnel-domains", "PUT", map[string]any{"zones": []string{cfg.BaseZone, altZone}, "primary": cfg.BaseZone}, 200)
	got, _ := s.Get(two.ID)
	if !reflect.DeepEqual(got.TunnelZones.list(), []string{altZone}) || got.Revision != two.Revision+1 {
		t.Fatal("removed domain was not pruned", got.TunnelZones, got.Revision)
	}
	if g, _ := s.Get(only.ID); g != only {
		t.Fatal("unaffected restricted account changed")
	}
	if g, _ := s.Get(open.ID); g != open {
		t.Fatal("unrestricted account changed")
	}
	// Restored or hand-edited data with only stale domains is never widened.
	stale := only
	stale.TunnelZones = newZoneList([]string{"gone.example.org"})
	if err = s.db.Update(func(tx *bolt.Tx) error { return putJSON(tx.Bucket(clients), []byte(stale.ID), stale) }); err != nil {
		t.Fatal(err)
	}
	if err = h.manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	h.manager.mu.RLock()
	_, running := h.manager.accounts[stale.ID]
	h.manager.mu.RUnlock()
	if running {
		t.Fatal("account without an available domain was started")
	}
	token, _ := s.SubscriptionToken(stale.ID, false)
	b.request("/sub/"+token, "GET", nil, 403)
	if _, err = s.Profiles(stale.ID); err == nil {
		t.Fatal("profile rendered without an assigned domain")
	}
	// Backups carry the restriction.
	backup := filepath.Join(t.TempDir(), "backup.db")
	file, _ := os.Create(backup)
	err = s.Backup(file)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = InspectBackupFile(backup); err != nil {
		t.Fatal(err)
	}
	restored, err := OpenStore(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if g, _ := restored.Get(two.ID); !reflect.DeepEqual(g.TunnelZones.list(), []string{altZone}) {
		t.Fatal("backup lost the restriction")
	}
}

// Every engine must accept a restricted account on its allowed domain and
// ignore it on the excluded one, even with otherwise valid credentials.
func TestAccountTunnelZonesEnforcedByAllEngines(t *testing.T) {
	s := testStore(t)
	cfg := addAltZone(t, s)
	restricted, err := s.Create(ClientInput{Name: "alt only", Engines: "all", Enabled: true, QuotaBytes: 1 << 24, TunnelZones: zonesPtr(altZone)})
	if err != nil {
		t.Fatal(err)
	}
	open, err := s.Create(ClientInput{Name: "all domains", Engines: "all", Enabled: true, QuotaBytes: 1 << 24})
	if err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer origin.Close()
	m := testManager(t, s, func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "8.8.8.8:443" {
			a, b := net.Pipe()
			go func() { defer b.Close(); io.Copy(b, b) }()
			return a, nil
		}
		return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
	})
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	tlsConfig := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	resolver := m.pc.LocalAddr().String()

	m.mu.RLock()
	if m.zones[restricted.Zone] != nil || m.zones[cfg.accountZone(restricted, altZone)] == nil {
		m.mu.RUnlock()
		t.Fatal("restricted account routed on the wrong domains")
	}
	if m.zones[open.Zone] == nil || m.zones[cfg.accountZone(open, altZone)] == nil {
		m.mu.RUnlock()
		t.Fatal("unrestricted account is missing a domain")
	}
	m.mu.RUnlock()

	soarWorks := func(c Client, zone string, timeout time.Duration) bool {
		c.Zone = zone
		client := nativeClient(t, m, c)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		conn, err := client.DialContext(ctx, "tcp", "8.8.8.8:443")
		if err != nil {
			return false
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		go conn.Write([]byte("zone"))
		reply := make([]byte, 4)
		_, err = io.ReadFull(conn, reply)
		return err == nil && string(reply) == "zone"
	}
	cottenWorks := func(c Client, zone string, timeout time.Duration) bool {
		seed, _ := s.Seed(c.ID)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		code, err := cottenprobe.CheckHTTP(ctx, cottenprobe.Config{Domain: "cot." + zone, Key: cottenKey(seed), Resolvers: []string{resolver}, Transport: "udp", URL: "https://example.com/", TLSConfig: tlsConfig})
		return err == nil && code == 200
	}
	masterWorks := func(c Client, zone string, timeout time.Duration) bool {
		seed, _ := s.Seed(c.ID)
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		result, err := masterprobe.CheckHTTPDetailed(ctx, masterprobe.Config{Domain: "mdns-1." + zone, Key: masterDNSKey(seed), Resolvers: []string{resolver}, Transport: "udp", URL: "https://example.com/", TLSConfig: tlsConfig})
		return err == nil && result.OK && result.HTTPStatus == 200
	}
	engines := map[string]func(Client, string, time.Duration) bool{"soar": soarWorks, "cotten": cottenWorks, "masterdns": masterWorks}
	for name, works := range engines {
		if !works(restricted, cfg.accountZone(restricted, altZone), 25*time.Second) {
			t.Fatalf("%s: restricted account failed on its allowed domain", name)
		}
		if works(restricted, restricted.Zone, 4*time.Second) {
			t.Fatalf("%s: restricted account connected on an excluded domain", name)
		}
		for _, zone := range cfg.accountZones(open) {
			if !works(open, zone, 25*time.Second) {
				t.Fatalf("%s: unrestricted account failed on %s", name, zone)
			}
		}
	}

	// Lifting the restriction re-registers the account on every domain.
	in := accountInput(restricted)
	in.TunnelZones = zonesPtr()
	if _, err = s.Edit(restricted.ID, in); err != nil {
		t.Fatal(err)
	}
	if err = m.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if !soarWorks(restricted, restricted.Zone, 25*time.Second) {
		t.Fatal("account still blocked on the base domain after allowing all domains")
	}
}
