// Package wgconf parses wg-quick style WireGuard config files (as exported by
// KeepSolid's "Manual configuration") and converts them to wireguard-go UAPI.
package wgconf

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

type Peer struct {
	PublicKey           string
	PresharedKey        string
	Endpoint            string
	AllowedIPs          []netip.Prefix
	PersistentKeepalive int
}

type Config struct {
	PrivateKey string
	Addresses  []netip.Addr
	DNS        []netip.Addr
	MTU        int
	Peers      []Peer
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

func Parse(r io.Reader) (*Config, error) {
	cfg := &Config{MTU: 1420}
	section := ""
	var peer *Peer

	sc := bufio.NewScanner(r)
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if section == "peer" {
				cfg.Peers = append(cfg.Peers, Peer{})
				peer = &cfg.Peers[len(cfg.Peers)-1]
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key = value", lineNo)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		var err error
		switch section {
		case "interface":
			err = cfg.setInterface(key, val)
		case "peer":
			err = peer.set(key, val)
		default:
			err = fmt.Errorf("key %q outside of a section", key)
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if cfg.PrivateKey == "" {
		return nil, fmt.Errorf("missing [Interface] PrivateKey")
	}
	if len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("missing [Interface] Address")
	}
	if len(cfg.Peers) == 0 {
		return nil, fmt.Errorf("missing [Peer] section")
	}
	return cfg, nil
}

func (c *Config) setInterface(key, val string) error {
	switch key {
	case "privatekey":
		c.PrivateKey = val
	case "address":
		for _, s := range splitList(val) {
			if p, err := netip.ParsePrefix(s); err == nil {
				c.Addresses = append(c.Addresses, p.Addr())
				continue
			}
			a, err := netip.ParseAddr(s)
			if err != nil {
				return fmt.Errorf("bad Address %q", s)
			}
			c.Addresses = append(c.Addresses, a)
		}
	case "dns":
		for _, s := range splitList(val) {
			a, err := netip.ParseAddr(s)
			if err != nil {
				// Search domains are allowed in wg-quick DNS; we have no use for them.
				continue
			}
			c.DNS = append(c.DNS, a)
		}
	case "mtu":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("bad MTU %q", val)
		}
		c.MTU = n
	}
	// ListenPort, Table, PostUp etc. are irrelevant for a userspace tunnel.
	return nil
}

func (p *Peer) set(key, val string) error {
	switch key {
	case "publickey":
		p.PublicKey = val
	case "presharedkey":
		p.PresharedKey = val
	case "endpoint":
		p.Endpoint = val
	case "allowedips":
		for _, s := range splitList(val) {
			pfx, err := netip.ParsePrefix(s)
			if err != nil {
				return fmt.Errorf("bad AllowedIPs %q", s)
			}
			p.AllowedIPs = append(p.AllowedIPs, pfx)
		}
	case "persistentkeepalive":
		n, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("bad PersistentKeepalive %q", val)
		}
		p.PersistentKeepalive = n
	}
	return nil
}

// UAPI renders the config in wireguard-go's IpcSet format. Peer endpoint
// hostnames are resolved here, since UAPI only accepts IP:port.
func (c *Config) UAPI() (string, error) {
	var b strings.Builder
	k, err := keyHex(c.PrivateKey)
	if err != nil {
		return "", fmt.Errorf("PrivateKey: %w", err)
	}
	fmt.Fprintf(&b, "private_key=%s\n", k)

	for i, p := range c.Peers {
		k, err := keyHex(p.PublicKey)
		if err != nil {
			return "", fmt.Errorf("peer %d PublicKey: %w", i, err)
		}
		fmt.Fprintf(&b, "public_key=%s\n", k)
		if p.PresharedKey != "" {
			k, err := keyHex(p.PresharedKey)
			if err != nil {
				return "", fmt.Errorf("peer %d PresharedKey: %w", i, err)
			}
			fmt.Fprintf(&b, "preshared_key=%s\n", k)
		}
		if p.Endpoint != "" {
			ep, err := resolveEndpoint(p.Endpoint)
			if err != nil {
				return "", fmt.Errorf("peer %d Endpoint: %w", i, err)
			}
			fmt.Fprintf(&b, "endpoint=%s\n", ep)
		}
		if p.PersistentKeepalive > 0 {
			fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.PersistentKeepalive)
		}
		for _, a := range p.AllowedIPs {
			fmt.Fprintf(&b, "allowed_ip=%s\n", a)
		}
	}
	return b.String(), nil
}

func keyHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

func resolveEndpoint(ep string) (string, error) {
	if ap, err := netip.ParseAddrPort(ep); err == nil {
		return ap.String(), nil
	}
	host, port, err := net.SplitHostPort(ep)
	if err != nil {
		return "", err
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", err
	}
	// Prefer IPv4: hotel/airport Wi-Fi often has broken IPv6.
	pick := ips[0]
	for _, ip := range ips {
		if ip.To4() != nil {
			pick = ip
			break
		}
	}
	return net.JoinHostPort(pick.String(), port), nil
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
