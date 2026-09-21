package teely

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type LANConfig struct {
	Enabled      bool   `json:"enabled,omitempty"`
	Address      string `json:"address,omitempty"`
	Port         int    `json:"port,omitempty"`
	Suffix       string `json:"suffix,omitempty"`
	Username     string `json:"username,omitempty"`
	PasswordHash string `json:"password_hash,omitempty"`
}

func (l LANConfig) hostname(app AppConfig) string { return app.ID + "-" + l.Suffix + ".local" }
func (l LANConfig) appURL(app AppConfig) string {
	return "https://" + net.JoinHostPort(l.hostname(app), strconv.Itoa(l.Port))
}

var lanLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var lanHost = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`)
var lanUser = regexp.MustCompile(`^[a-zA-Z0-9_.@-]{1,64}$`)
var lanHash = regexp.MustCompile(`^\$2[aby]\$(1[0-6])\$[./A-Za-z0-9]{53}$`)

func validateLAN(cfg *Config) error {
	l := cfg.LAN
	if !l.Enabled {
		return nil
	}
	ip := net.ParseIP(l.Address)
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() {
		return errors.New("LAN address must be a private IPv4 address")
	}
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("LAN access requires Teely to listen on a literal loopback address")
	}
	if l.Port < 1024 || l.Port > 65535 || l.Port == 2019 || strconv.Itoa(l.Port) == port {
		return errors.New("LAN HTTPS port must be 1024-65535 and not a management port")
	}
	if !lanLabel.MatchString(l.Suffix) {
		return errors.New("LAN machine name must be a lowercase DNS label")
	}
	if !lanUser.MatchString(l.Username) || !lanHash.MatchString(l.PasswordHash) {
		return errors.New("LAN access needs a username and password (at least 12 characters)")
	}
	for _, a := range cfg.Apps {
		if a.Port == l.Port {
			return fmt.Errorf("LAN port is already assigned to %s", a.Name)
		}
		if a.ShareLAN && (!lanLabel.MatchString(a.ID+"-"+l.Suffix) || !lanHost.MatchString(a.Hostname) || !strings.HasSuffix(a.Hostname, ".localhost") || strings.EqualFold(a.Hostname, cfg.AdminHostname)) {
			return fmt.Errorf("%s needs a unique app .localhost hostname and an app ID/machine name of at most 63 characters for LAN sharing", a.Name)
		}
	}
	return nil
}

func lanAddresses() []string {
	var out []string
	interfaces, _ := net.Interfaces()
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || strings.HasPrefix(iface.Name, "utun") {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, a := range addresses {
			ip, _, _ := net.ParseCIDR(a.String())
			if ip != nil && ip.To4() != nil && ip.IsPrivate() {
				out = append(out, ip.String())
			}
		}
	}
	sort.Strings(out)
	return out
}

func writePrivateFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".teely-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func writeLANRoutes(b *strings.Builder, cfg *Config) {
	if !cfg.LAN.Enabled || !lanAddressAvailable(cfg.LAN.Address) {
		return
	}
	for _, a := range cfg.Apps {
		if !a.ShareLAN {
			continue
		}
		fmt.Fprintf(b, "%s {\n\tbind %s\n", cfg.LAN.appURL(a), cfg.LAN.Address)
		writeTeelyTLS(b)
		// Preserve the shared authority so Teely authenticates before app startup.
		fmt.Fprintf(b, "\treverse_proxy %s {\n\t\theader_up Host %s\n\t\theader_up %s {http.request.remote.host}\n\t}\n}\n\n", cfg.ListenAddress, net.JoinHostPort(cfg.LAN.hostname(a), strconv.Itoa(cfg.LAN.Port)), lanClientIPHeader)
	}
}

func writeLocalAccessGuard(b *strings.Builder) {
	// macOS allows unprivileged wildcard 443 listeners, but not IP-specific ones.
	// Use the actual socket peer, never X-Forwarded-For, to protect local routes.
	b.WriteString("\tbind 0.0.0.0 ::\n\t@teelyRemote not remote_ip 127.0.0.0/8 ::1\n\tabort @teelyRemote\n")
}

type bonjourState struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	errors    map[string]string
	signature string
}

var bonjourCommand = exec.CommandContext

var lanAddressAvailable = func(address string) bool {
	for _, candidate := range lanAddresses() {
		if candidate == address {
			return true
		}
	}
	return false
}

func (b *bonjourState) stop() {
	if b.cancel != nil {
		b.cancel()
		b.wg.Wait()
		b.cancel = nil
	}
	b.signature = ""
}

// Called with Manager.mu held. Workers only lock their own status mutex.
func (m *Manager) reconcileBonjourLocked() {
	if m.bonjour == nil {
		return
	}
	b := m.bonjour
	var hosts []string
	m.lanAvailable = m.config.LAN.Enabled && lanAddressAvailable(m.config.LAN.Address)
	if m.lanAvailable {
		for _, a := range m.config.Apps {
			if a.ShareLAN {
				hosts = append(hosts, m.config.LAN.hostname(a))
			}
		}
	}
	sort.Strings(hosts)
	l := m.config.LAN
	sig := fmt.Sprintf("%s:%d:%s", l.Address, l.Port, strings.Join(hosts, ","))
	if b.signature == sig {
		return
	}
	b.stop()
	b.mu.Lock()
	b.errors = map[string]string{}
	b.mu.Unlock()
	b.signature = sig
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	for _, host := range hosts {
		b.wg.Add(1)
		go func(host string) {
			defer b.wg.Done()
			for ctx.Err() == nil {
				cmd := bonjourCommand(ctx, "/usr/bin/dns-sd", "-P", "Teely "+strings.TrimSuffix(host, ".local"), "_https._tcp", "local", strconv.Itoa(l.Port), host, l.Address, "path=/")
				output := newLogBuffer(2048)
				cmd.Stdout, cmd.Stderr = output, output
				err := cmd.Start()
				if err == nil {
					b.mu.Lock()
					delete(b.errors, host)
					b.mu.Unlock()
					err = cmd.Wait()
				}
				if ctx.Err() != nil {
					return
				}
				b.mu.Lock()
				b.errors[host] = fmt.Sprintf("Bonjour could not advertise %s: %v. Retrying.", host, err)
				b.mu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Second):
				}
			}
		}(host)
	}
}

func (m *Manager) StartLAN() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.bonjour == nil {
		m.bonjour = &bonjourState{}
	}
	m.reconcileBonjourLocked()
}

// Reuse the existing idle tick: no discovery polling or reload while unchanged.
func (m *Manager) checkLANNetworkLocked() {
	if m.bonjour == nil {
		return
	}
	available := m.config.LAN.Enabled && lanAddressAvailable(m.config.LAN.Address)
	if available == m.lanAvailable {
		return
	}
	if err := m.syncCaddyLocked(); err != nil {
		m.bonjour.mu.Lock()
		m.bonjour.errors["network"] = "Could not update LAN routes after the network changed: " + err.Error()
		m.bonjour.mu.Unlock()
		return
	}
	m.reconcileBonjourLocked()
	m.dashboardEvents.publish()
}

func (m *Manager) lanError() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.config.LAN.Enabled {
		return ""
	}
	found := false
	for _, a := range lanAddresses() {
		found = found || a == m.config.LAN.Address
	}
	if !found {
		return "The configured LAN address is not available on this network. Update LAN Access when your address changes."
	}
	if m.bonjour != nil {
		m.bonjour.mu.Lock()
		defer m.bonjour.mu.Unlock()
		for _, err := range m.bonjour.errors {
			return err
		}
	}
	return ""
}

func (m *Manager) handleLANSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	l := LANConfig{Enabled: r.FormValue("enabled") == "on", Address: strings.TrimSpace(r.FormValue("address")), Suffix: strings.TrimSpace(r.FormValue("suffix")), Username: strings.TrimSpace(r.FormValue("username"))}
	l.Port, _ = strconv.Atoi(r.FormValue("lan_port"))
	password := r.FormValue("password")
	var saveErr error
	m.mu.Lock()
	if r.FormValue("disable") == "yes" {
		l = m.config.LAN
		l.Enabled = false
	}
	l.PasswordHash = m.config.LAN.PasswordHash
	if password != "" && l.Enabled {
		if len(password) < 12 || len(password) > 72 || strings.TrimSpace(password) != password || strings.ContainsAny(password, "\r\n\x00") {
			saveErr = errors.New("Use a password of 12-72 bytes without surrounding whitespace or line breaks")
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			cmd := exec.CommandContext(ctx, m.config.Caddy.BinaryPath, "hash-password", "--algorithm", "bcrypt")
			cmd.Stdin = strings.NewReader(password + "\n")
			hash, err := cmd.Output()
			cancel()
			if err != nil {
				saveErr = errors.New("Could not hash the password with Caddy")
			} else {
				l.PasswordHash = strings.TrimSpace(string(hash))
			}
		}
	}
	next := cloneConfig(m.config)
	next.LAN = l
	if saveErr == nil {
		saveErr = validateLAN(next)
	}
	if saveErr == nil && l.Enabled {
		found := false
		for _, a := range lanAddresses() {
			found = found || a == l.Address
		}
		if !found {
			saveErr = errors.New("Choose an available LAN address on this Mac")
		}
		if _, err := os.Stat("/usr/bin/dns-sd"); err != nil {
			saveErr = errors.New("Bonjour requires macOS dns-sd")
		}
	}
	if saveErr == nil {
		saveErr = m.commitConfigLocked(next)
	}
	m.mu.Unlock()
	if saveErr != nil {
		http.Redirect(w, r, "/?lan=1&error="+url.QueryEscape(saveErr.Error())+"#lan-access", 303)
		return
	}
	http.Redirect(w, r, "/?notice="+url.QueryEscape("LAN access settings saved.")+"#lan-access", 303)
}
