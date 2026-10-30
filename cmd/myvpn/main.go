// Command myvpn runs a userspace WireGuard tunnel and exposes it as a local
// SOCKS5 proxy, so only apps pointed at the proxy go through the VPN.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"

	"github.com/J1wanSeo/layover/internal/socks5"
	"github.com/J1wanSeo/layover/internal/wgconf"
)

const defaultListen = "127.0.0.1:1080"

// version is set at build time: -ldflags "-X main.version=..."
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "up":
		err = cmdUp(os.Args[2:])
	case "chrome":
		err = cmdChrome(os.Args[2:])
	case "list":
		err = cmdList()
	case "check":
		err = cmdCheck(os.Args[2:])
	case "version", "--version":
		fmt.Println(version)
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "myvpn:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage:
  myvpn up [-listen %s] [-v] <name|path.conf>
      start tunnel; <name> is looked up as %s/<name>.conf
  myvpn chrome [-proxy %s]
      open a separate Chrome window that uses the tunnel
  myvpn list
      list saved configs
  myvpn check <name|path.conf>
      validate a config without connecting
`, defaultListen, configDir(), defaultListen)
}

func configDir() string {
	if d := os.Getenv("MYVPN_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "myvpn")
}

func resolveConfig(arg string) string {
	if strings.HasSuffix(arg, ".conf") || strings.ContainsRune(arg, os.PathSeparator) {
		return arg
	}
	return filepath.Join(configDir(), arg+".conf")
}

func cmdList() error {
	matches, err := filepath.Glob(filepath.Join(configDir(), "*.conf"))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		fmt.Printf("no configs in %s\n", configDir())
		return nil
	}
	for _, m := range matches {
		fmt.Println(strings.TrimSuffix(filepath.Base(m), ".conf"))
	}
	return nil
}

func cmdCheck(args []string) error {
	if len(args) != 1 {
		usage()
		os.Exit(2)
	}
	path := resolveConfig(args[0])
	cfg, err := wgconf.Load(path)
	if err == nil {
		_, err = cfg.UAPI()
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	fmt.Println("ok")
	return nil
}

func cmdUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	listen := fs.String("listen", defaultListen, "SOCKS5 listen address")
	verbose := fs.Bool("v", false, "verbose WireGuard logging")
	watchStdin := fs.Bool("watch-stdin", false, "shut down when stdin closes (used by Layover.app so a crashed app doesn't leave the tunnel running)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
		os.Exit(2)
	}

	path := resolveConfig(fs.Arg(0))
	cfg, err := wgconf.Load(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	uapi, err := cfg.UAPI()
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	tunDev, tnet, err := netstack.CreateNetTUN(cfg.Addresses, cfg.DNS, cfg.MTU)
	if err != nil {
		return fmt.Errorf("create tun: %w", err)
	}
	if os.Getenv("MYVPN_NO_TUNE") == "" {
		if err := tuneTCP(tnet); err != nil {
			fmt.Fprintf(os.Stderr, "warning: TCP tuning skipped: %v\n", err)
		}
	}
	level := device.LogLevelError
	if *verbose {
		level = device.LogLevelVerbose
	}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), device.NewLogger(level, "wg: "))
	defer dev.Close()
	if err := dev.IpcSet(uapi); err != nil {
		return fmt.Errorf("configure wireguard: %w", err)
	}
	if err := dev.Up(); err != nil {
		return fmt.Errorf("bring up wireguard: %w", err)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer ln.Close()

	logger := log.New(os.Stderr, "", log.LstdFlags)
	if !*verbose {
		logger = nil
	}
	srv := &socks5.Server{Dial: tnet.DialContext, Logger: logger}
	go func() {
		if err := srv.Serve(ln); err != nil {
			log.Fatalf("socks: %v", err)
		}
	}()

	if err := waitHandshake(dev, tnet, 15*time.Second); err != nil {
		return err
	}

	fmt.Printf("tunnel %q up, SOCKS5 on %s\n", strings.TrimSuffix(filepath.Base(path), ".conf"), ln.Addr())
	go func() {
		if ip, err := exitIP(tnet); err != nil {
			fmt.Printf("exit IP check failed: %v\n", err)
		} else {
			fmt.Printf("exit IP: %s\n", ip)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	if *watchStdin {
		go func() {
			io.Copy(io.Discard, os.Stdin)
			sig <- syscall.SIGTERM
		}()
	}
	<-sig
	fmt.Println("\nshutting down")
	return nil
}

var errNoHandshake = errors.New("no handshake from server " +
	"(config revoked/regenerated, or UDP blocked on this network)")

// waitHandshake blocks until the peer completes a WireGuard handshake.
// Without this a revoked config looks "connected" while every request hangs.
func waitHandshake(dev *device.Device, tnet *netstack.Net, timeout time.Duration) error {
	// Handshakes only start when there is traffic (or keepalive), so send some.
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	go tnet.LookupContextHost(ctx, "ipinfo.io")

	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		state, err := dev.IpcGet()
		if err != nil {
			return err
		}
		for _, line := range strings.Split(state, "\n") {
			if v, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok && v != "0" {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errNoHandshake
		case <-tick.C:
		}
	}
}

// exitIP asks a public echo service where our tunnel traffic comes out,
// e.g. "162.221.202.139 (Vancouver, CA)".
func exitIP(tnet *netstack.Net) (string, error) {
	client := &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{DialContext: tnet.DialContext},
	}
	resp, err := client.Get("https://ipinfo.io/json")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var info struct {
		IP      string `json:"ip"`
		City    string `json:"city"`
		Country string `json:"country"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&info); err != nil {
		return "", err
	}
	if info.IP == "" {
		return "", fmt.Errorf("unexpected response (HTTP %d)", resp.StatusCode)
	}
	if info.City == "" {
		return info.IP, nil
	}
	return fmt.Sprintf("%s (%s, %s)", info.IP, info.City, info.Country), nil
}

func cmdChrome(args []string) error {
	fs := flag.NewFlagSet("chrome", flag.ExitOnError)
	proxy := fs.String("proxy", defaultListen, "SOCKS5 proxy address")
	fs.Parse(args)

	c, err := net.DialTimeout("tcp", *proxy, time.Second)
	if err != nil {
		return fmt.Errorf("no proxy on %s; run `myvpn up <name>` first", *proxy)
	}
	c.Close()

	// A dedicated profile forces a new Chrome process, so the proxy flag
	// applies even while your normal Chrome is open.
	profile := filepath.Join(configDir(), "chrome-profile")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "open", "-na", "Google Chrome", "--args",
		"--user-data-dir="+profile,
		"--proxy-server=socks5://"+*proxy,
		// Keep WebRTC from sending UDP outside the proxy and revealing the real IP.
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--no-first-run",
	)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}
