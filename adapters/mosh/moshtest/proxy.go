// SPDX-License-Identifier: GPL-3.0-or-later

package moshtest

import (
	"net"
	"sync"
	"testing"
)

// Proxy relays UDP datagrams between a mosh client and a Server. It models the
// transport seam a network change moves: Blackhole drops datagrams while the
// Session is out of coverage, and Rebind gives the server a new client source
// address when coverage returns, as a Wi-Fi toggle or a NAT rebinding does.
//
// The client keeps its one unconnected socket throughout; only the path
// changes. That is what lets the phone test assert the Session survived the
// change without a new SSH bootstrap.
type Proxy struct {
	t      testing.TB
	front  *net.UDPConn
	server *net.UDPAddr

	mu         sync.Mutex
	clientAddr *net.UDPAddr
	upstream   *net.UDPConn
	streams    map[*net.UDPConn]bool
	blackhole  bool
	rebinds    int
	drops      int
	stop       chan struct{}
	closeOnce  sync.Once
	wg         sync.WaitGroup
}

// NewProxy starts a relay in front of server on loopback.
func NewProxy(t testing.TB, server *net.UDPAddr) *Proxy {
	t.Helper()
	front, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("moshtest: proxy front: %v", err)
	}
	upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		_ = front.Close()
		t.Fatalf("moshtest: proxy upstream: %v", err)
	}
	proxy := &Proxy{
		t:        t,
		front:    front,
		server:   server,
		upstream: upstream,
		streams:  map[*net.UDPConn]bool{upstream: true},
		stop:     make(chan struct{}),
	}
	proxy.wg.Add(2)
	go proxy.readFront()
	go proxy.readUpstream(upstream)
	t.Cleanup(proxy.Close)
	return proxy
}

// Port is the client-facing relay port the announcement advertises.
func (p *Proxy) Port() int { return p.front.LocalAddr().(*net.UDPAddr).Port }

// Close stops the relay and waits for its goroutines.
func (p *Proxy) Close() {
	p.closeOnce.Do(func() {
		close(p.stop)
		p.mu.Lock()
		streams := make([]*net.UDPConn, 0, len(p.streams))
		for stream := range p.streams {
			streams = append(streams, stream)
			delete(p.streams, stream)
		}
		p.upstream = nil
		p.mu.Unlock()
		_ = p.front.Close()
		for _, stream := range streams {
			_ = stream.Close()
		}
		p.wg.Wait()
	})
}

// Blackhole makes the relay drop every datagram, as leaving coverage does.
func (p *Proxy) Blackhole(on bool) {
	p.mu.Lock()
	p.blackhole = on
	p.mu.Unlock()
}

// Rebind opens a new upstream socket, so the server observes a new client
// source address on the next datagram. Datagrams still in flight to the old
// socket are lost, exactly as at a network switch.
func (p *Proxy) Rebind() {
	p.mu.Lock()
	if p.upstream == nil {
		p.mu.Unlock()
		return
	}
	old := p.upstream
	upstream, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		p.mu.Unlock()
		p.t.Errorf("moshtest: proxy rebind: %v", err)
		return
	}
	p.upstream = upstream
	p.streams[upstream] = true
	p.rebinds++
	p.wg.Add(1)
	p.mu.Unlock()

	_ = old.Close()
	p.mu.Lock()
	delete(p.streams, old)
	p.mu.Unlock()
	go p.readUpstream(upstream)
}

// UpstreamAddr is the source address the server currently sees.
func (p *Proxy) UpstreamAddr() *net.UDPAddr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.upstream == nil {
		return nil
	}
	return p.upstream.LocalAddr().(*net.UDPAddr)
}

// Rebinds reports how many upstream rebinds happened.
func (p *Proxy) Rebinds() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rebinds
}

// Drops reports how many datagrams the relay discarded.
func (p *Proxy) Drops() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.drops
}

func (p *Proxy) readFront() {
	defer p.wg.Done()
	buffer := make([]byte, 2048)
	for {
		n, addr, err := p.front.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		p.mu.Lock()
		p.clientAddr = addr
		upstream := p.upstream
		blackhole := p.blackhole
		if blackhole {
			p.drops++
		}
		p.mu.Unlock()
		if blackhole || upstream == nil {
			continue
		}
		_, _ = upstream.WriteToUDP(buffer[:n], p.server)
	}
}

func (p *Proxy) readUpstream(upstream *net.UDPConn) {
	defer p.wg.Done()
	buffer := make([]byte, 2048)
	for {
		n, _, err := upstream.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		p.mu.Lock()
		current := p.upstream == upstream
		blackhole := p.blackhole
		target := p.clientAddr
		if blackhole {
			p.drops++
		}
		p.mu.Unlock()
		if blackhole || !current || target == nil {
			continue
		}
		_, _ = p.front.WriteToUDP(buffer[:n], target)
	}
}
