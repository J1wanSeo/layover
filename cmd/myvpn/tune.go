package main

import (
	"fmt"
	"reflect"
	"unsafe"

	"golang.zx2c4.com/wireguard/tun/netstack"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
)

// tuneTCP raises gVisor's TCP buffer limits. The defaults (1MB fixed window,
// reno) cap a single connection to a few MB/s at ~150ms RTT, which is
// typical for an overseas VPN server.
//
// netstack.Net doesn't expose its stack, so we reach in by field name.
func tuneTCP(tnet *netstack.Net) error {
	f := reflect.ValueOf(tnet).Elem().FieldByName("stack")
	if !f.IsValid() {
		return fmt.Errorf("netstack.Net has no stack field")
	}
	s, ok := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Interface().(*stack.Stack)
	if !ok || s == nil {
		return fmt.Errorf("netstack.Net stack field has unexpected type %s", f.Type())
	}

	const maxBuf = 16 << 20
	opts := []tcpip.SettableTransportProtocolOption{
		&tcpip.TCPReceiveBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: maxBuf},
		&tcpip.TCPSendBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: maxBuf},
	}
	moderate := tcpip.TCPModerateReceiveBufferOption(true)
	cubic := tcpip.CongestionControlOption("cubic")
	opts = append(opts, &moderate, &cubic)
	for _, o := range opts {
		if err := s.SetTransportProtocolOption(tcp.ProtocolNumber, o); err != nil {
			return fmt.Errorf("set %T: %v", o, err)
		}
	}
	return nil
}
