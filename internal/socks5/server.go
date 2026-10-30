// Package socks5 is a minimal SOCKS5 server (no auth, CONNECT only) whose
// outbound connections go through a caller-supplied dialer.
//
// Hostnames are passed to the dialer unresolved, so DNS happens inside the
// tunnel instead of leaking to the local network.
package socks5

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"time"
)

type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

type Server struct {
	Dial   DialFunc
	Logger *log.Logger
}

const (
	version5     = 0x05
	methodNoAuth = 0x00
	methodNone   = 0xff
	cmdConnect   = 0x01
	atypIPv4     = 0x01
	atypDomain   = 0x03
	atypIPv6     = 0x04

	repSuccess         = 0x00
	repHostUnreachable = 0x04
	repCmdNotSupported = 0x07
	repAtypUnsupported = 0x08
)

func (s *Server) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	c.SetDeadline(time.Now().Add(30 * time.Second))

	target, err := s.handshake(c)
	if err != nil {
		s.logf("socks: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	up, err := s.Dial(ctx, "tcp", target)
	cancel()
	if err != nil {
		s.logf("socks: dial %s: %v", target, err)
		reply(c, repHostUnreachable)
		return
	}
	defer up.Close()
	if err := reply(c, repSuccess); err != nil {
		return
	}
	c.SetDeadline(time.Time{})

	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
	<-done
}

func (s *Server) handshake(c net.Conn) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return "", err
	}
	if hdr[0] != version5 {
		return "", fmt.Errorf("unsupported version %d", hdr[0])
	}
	methods := make([]byte, hdr[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return "", err
	}
	method := byte(methodNone)
	for _, m := range methods {
		if m == methodNoAuth {
			method = methodNoAuth
		}
	}
	if _, err := c.Write([]byte{version5, method}); err != nil {
		return "", err
	}
	if method == methodNone {
		return "", errors.New("client offered no acceptable auth method")
	}

	var req [4]byte
	if _, err := io.ReadFull(c, req[:]); err != nil {
		return "", err
	}
	if req[1] != cmdConnect {
		reply(c, repCmdNotSupported)
		return "", fmt.Errorf("unsupported command %d", req[1])
	}

	var host string
	switch req[3] {
	case atypIPv4, atypIPv6:
		n := net.IPv4len
		if req[3] == atypIPv6 {
			n = net.IPv6len
		}
		ip := make(net.IP, n)
		if _, err := io.ReadFull(c, ip); err != nil {
			return "", err
		}
		host = ip.String()
	case atypDomain:
		var l [1]byte
		if _, err := io.ReadFull(c, l[:]); err != nil {
			return "", err
		}
		name := make([]byte, l[0])
		if _, err := io.ReadFull(c, name); err != nil {
			return "", err
		}
		host = string(name)
	default:
		reply(c, repAtypUnsupported)
		return "", fmt.Errorf("unsupported address type %d", req[3])
	}

	var port [2]byte
	if _, err := io.ReadFull(c, port[:]); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port[:])))), nil
}

// reply sends a SOCKS5 reply with a zero bind address; clients ignore it for CONNECT.
func reply(c net.Conn, rep byte) error {
	_, err := c.Write([]byte{version5, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
	return err
}

func (s *Server) logf(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}
