package routeros

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type WGPeer struct {
	Interface      string  `json:"interface"`
	Name           string  `json:"name"`
	Endpoint       string  `json:"endpoint,omitempty"`
	AllowedAddress string  `json:"allowed_address"`
	LastHandshake  string  `json:"last_handshake"`
	HandshakeAge   int64   `json:"handshake_age_secs"` // -1 when never
	RxBytes        int64   `json:"rx_bytes"`
	TxBytes        int64   `json:"tx_bytes"`
	RxBps          float64 `json:"rx_bps"`
	TxBps          float64 `json:"tx_bps"`
	Disabled       bool    `json:"disabled"`
}

type NetwatchHost struct {
	Host    string `json:"host"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Since   string `json:"since"`
	Comment string `json:"comment,omitempty"`
}

type Pool struct {
	Name    string  `json:"name"`
	Ranges  string  `json:"ranges"`
	Size    int64   `json:"size"`
	Used    int64   `json:"used"`
	UsedPct float64 `json:"used_pct"`
}

var reDuration = regexp.MustCompile(`(\d+)(w|d|h|m|s|ms)`)

// parseDuration reads RouterOS durations such as "1w2d3h4m5s" or "12m3s450ms".
func parseDuration(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	var d time.Duration
	n := 0
	for _, m := range reDuration.FindAllStringSubmatch(s, -1) {
		v, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "w":
			d += time.Duration(v) * 7 * 24 * time.Hour
		case "d":
			d += time.Duration(v) * 24 * time.Hour
		case "h":
			d += time.Duration(v) * time.Hour
		case "m":
			d += time.Duration(v) * time.Minute
		case "s":
			d += time.Duration(v) * time.Second
		case "ms":
			d += time.Duration(v) * time.Millisecond
		}
		n++
	}
	return d, n > 0
}

func (p *Poller) pollWireGuard(ctx context.Context, snap *Snapshot, now time.Time) error {
	var peers []struct {
		Interface      string `json:"interface"`
		Name           string `json:"name"`
		Comment        string `json:"comment"`
		Endpoint       string `json:"current-endpoint-address"`
		EndpointPort   string `json:"current-endpoint-port"`
		AllowedAddress string `json:"allowed-address"`
		LastHandshake  string `json:"last-handshake"`
		Rx             string `json:"rx"`
		Tx             string `json:"tx"`
		Disabled       string `json:"disabled"`
		PublicKey      string `json:"public-key"`
	}
	snap.WireGuard = []WGPeer{}
	if err := p.client.Get(ctx, "/interface/wireguard/peers", &peers); err != nil {
		var nf NotFound
		if errors.As(err, &nf) {
			return nil
		}
		return err
	}
	seen := map[string]bool{}
	for _, r := range peers {
		name := r.Name
		if name == "" {
			name = r.Comment
		}
		if name == "" && len(r.PublicKey) >= 8 {
			name = r.PublicKey[:8]
		}
		key := r.Interface + "/" + name
		seen[key] = true
		peer := WGPeer{Interface: r.Interface, Name: name, AllowedAddress: r.AllowedAddress, LastHandshake: r.LastHandshake,
			HandshakeAge: -1, RxBytes: atoi(r.Rx), TxBytes: atoi(r.Tx), Disabled: isTrue(r.Disabled)}
		if r.Endpoint != "" {
			peer.Endpoint = r.Endpoint
			if r.EndpointPort != "" {
				peer.Endpoint += ":" + r.EndpointPort
			}
		}
		if d, ok := parseDuration(r.LastHandshake); ok {
			peer.HandshakeAge = int64(d.Seconds())
		}
		cur := counters{at: now, rx: peer.RxBytes, tx: peer.TxBytes}
		if prev, ok := p.peers[key]; ok {
			secs := now.Sub(prev.at).Seconds()
			if secs > 0 && cur.rx >= prev.rx && cur.tx >= prev.tx {
				peer.RxBps = float64(cur.rx-prev.rx) * 8 / secs
				peer.TxBps = float64(cur.tx-prev.tx) * 8 / secs
				p.sample(now, MetricWGRx, key, peer.RxBps)
				p.sample(now, MetricWGTx, key, peer.TxBps)
			}
		}
		p.peers[key] = cur
		snap.WireGuard = append(snap.WireGuard, peer)
	}
	for key := range p.peers {
		if !seen[key] {
			delete(p.peers, key)
		}
	}
	return nil
}

func (p *Poller) pollNetwatch(ctx context.Context, snap *Snapshot, _ time.Time) error {
	var hosts []struct {
		Host     string `json:"host"`
		Type     string `json:"type"`
		Status   string `json:"status"`
		Since    string `json:"since"`
		Comment  string `json:"comment"`
		Disabled string `json:"disabled"`
	}
	snap.Netwatch = []NetwatchHost{}
	if err := p.client.Get(ctx, "/tool/netwatch", &hosts); err != nil {
		var nf NotFound
		if errors.As(err, &nf) {
			return nil
		}
		return err
	}
	for _, r := range hosts {
		if isTrue(r.Disabled) {
			continue
		}
		snap.Netwatch = append(snap.Netwatch, NetwatchHost{Host: r.Host, Type: r.Type, Status: r.Status, Since: r.Since, Comment: r.Comment})
	}
	return nil
}

// pollPools measures DHCP address pool usage by counting leases inside each
// pool's ranges.
func (p *Poller) pollPools(ctx context.Context, snap *Snapshot, now time.Time) error {
	var pools []struct {
		Name   string `json:"name"`
		Ranges string `json:"ranges"`
	}
	snap.Pools = []Pool{}
	if err := p.client.Get(ctx, "/ip/pool", &pools); err != nil {
		var nf NotFound
		if errors.As(err, &nf) {
			return nil
		}
		return err
	}
	var leases []struct {
		Address string `json:"address"`
		Status  string `json:"status"`
	}
	if err := p.client.Get(ctx, "/ip/dhcp-server/lease", &leases); err != nil {
		return err
	}
	for _, pool := range pools {
		var size, used int64
		for rng := range strings.SplitSeq(pool.Ranges, ",") {
			lo, hi, ok := parseRange(strings.TrimSpace(rng))
			if !ok {
				continue
			}
			size += hi - lo + 1
			for _, l := range leases {
				if l.Status != "bound" {
					continue
				}
				if a, err := netip.ParseAddr(l.Address); err == nil && a.Is4() {
					v := ip4Int(a)
					if v >= lo && v <= hi {
						used++
					}
				}
			}
		}
		if size == 0 {
			continue
		}
		pct := 100 * float64(used) / float64(size)
		snap.Pools = append(snap.Pools, Pool{Name: pool.Name, Ranges: pool.Ranges, Size: size, Used: used, UsedPct: pct})
		p.sample(now, MetricPoolUsed, pool.Name, pct)
	}
	return nil
}

func parseRange(s string) (lo, hi int64, ok bool) {
	a, b, found := strings.Cut(s, "-")
	start, err := netip.ParseAddr(strings.TrimSpace(a))
	if err != nil || !start.Is4() {
		return 0, 0, false
	}
	end := start
	if found {
		if end, err = netip.ParseAddr(strings.TrimSpace(b)); err != nil || !end.Is4() {
			return 0, 0, false
		}
	}
	return ip4Int(start), ip4Int(end), true
}

func ip4Int(a netip.Addr) int64 {
	b := a.As4()
	return int64(b[0])<<24 | int64(b[1])<<16 | int64(b[2])<<8 | int64(b[3])
}

// pollEthernet reads negotiated link rates; monitor is a command, so it
// goes through POST with once=true.
func (p *Poller) pollEthernet(ctx context.Context, snap *Snapshot, _ time.Time) error {
	var names []string
	for _, i := range snap.Interfaces {
		if i.Type == "ether" && i.Running {
			names = append(names, i.Name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	var rows []struct {
		Name       string `json:"name"`
		Rate       string `json:"rate"`
		Status     string `json:"status"`
		FullDuplex string `json:"full-duplex"`
	}
	body := map[string]string{"numbers": strings.Join(names, ","), "once": "true", ".proplist": "name,rate,status,full-duplex"}
	if err := p.client.Post(ctx, "/interface/ethernet/monitor", body, &rows); err != nil {
		var nf NotFound
		if errors.As(err, &nf) {
			return nil
		}
		return err
	}
	rates := map[string]struct{ rate, duplex string }{}
	for _, r := range rows {
		duplex := ""
		if r.FullDuplex != "" {
			duplex = "half"
			if isTrue(r.FullDuplex) {
				duplex = "full"
			}
		}
		rates[r.Name] = struct{ rate, duplex string }{r.Rate, duplex}
	}
	for i := range snap.Interfaces {
		if r, ok := rates[snap.Interfaces[i].Name]; ok {
			snap.Interfaces[i].LinkRate = r.rate
			snap.Interfaces[i].Duplex = r.duplex
		}
	}
	return nil
}
