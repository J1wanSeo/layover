package wgconf

import (
	"strings"
	"testing"
)

const sample = `
[Interface]
PrivateKey = yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=
Address = 10.8.0.2/32, fd00::2/128
DNS = 1.1.1.1, example.local
MTU = 1380

# KeepSolid adds comments like this
[Peer]
PublicKey = xTIBA5rboUvnH4htodjb6e697QjLERt1NAB4mZqp8Dg=
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = 192.0.2.10:51820
PersistentKeepalive = 25
`

func TestParse(t *testing.T) {
	cfg, err := Parse(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Addresses) != 2 || cfg.Addresses[0].String() != "10.8.0.2" {
		t.Errorf("addresses = %v", cfg.Addresses)
	}
	if len(cfg.DNS) != 1 || cfg.DNS[0].String() != "1.1.1.1" {
		t.Errorf("dns = %v", cfg.DNS)
	}
	if cfg.MTU != 1380 {
		t.Errorf("mtu = %d", cfg.MTU)
	}
	if len(cfg.Peers) != 1 || len(cfg.Peers[0].AllowedIPs) != 2 {
		t.Fatalf("peers = %+v", cfg.Peers)
	}

	uapi, err := cfg.UAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"private_key=c809f3e5317e9575c9b5ed78b638b7ce530dabe85ddab614220241801ddf0669\n",
		"endpoint=192.0.2.10:51820\n",
		"persistent_keepalive_interval=25\n",
		"allowed_ip=0.0.0.0/0\n",
		"allowed_ip=::/0\n",
	} {
		if !strings.Contains(uapi, want) {
			t.Errorf("uapi missing %q\n%s", want, uapi)
		}
	}
}

func TestParseErrors(t *testing.T) {
	for name, in := range map[string]string{
		"no key":     "[Interface]\nAddress = 10.0.0.2/32\n[Peer]\nPublicKey = x\n",
		"no peer":    "[Interface]\nPrivateKey = a\nAddress = 10.0.0.2/32\n",
		"bad line":   "[Interface]\nPrivateKey\n",
		"bad prefix": "[Interface]\nPrivateKey = a\nAddress = 10.0.0.2/32\n[Peer]\nAllowedIPs = nope\n",
	} {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
