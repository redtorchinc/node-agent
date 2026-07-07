package netown

import (
	"fmt"
	"time"
)

// Query is one gateway-observed flow, normalized to the node perspective
// (local_* is this node's side of the tuple).
type Query struct {
	Proto        string `json:"proto"`
	LocalAddr    string `json:"local_addr"`
	LocalPort    uint32 `json:"local_port"`
	RemoteAddr   string `json:"remote_addr"`
	RemotePort   uint32 `json:"remote_port"`
	ObservedAtNS int64  `json:"observed_at_unix_ns,omitempty"`
}

// Owner is the resolved process identity for a matched flow.
type Owner struct {
	PID           int32  `json:"pid,omitempty"`
	ProcessName   string `json:"process_name,omitempty"`
	CmdlineHead   string `json:"cmdline_head,omitempty"`
	Exe           string `json:"exe,omitempty"`
	User          string `json:"user,omitempty"`
	UID           *int32 `json:"uid,omitempty"`
	Service       string `json:"service,omitempty"`
	ContainerID   string `json:"container_id,omitempty"`
	ContainerName string `json:"container_name,omitempty"`
	Cgroup        string `json:"cgroup,omitempty"`
}

// MatchedSocket echoes the socket the match landed on.
type MatchedSocket struct {
	Proto      string `json:"proto"`
	State      string `json:"state,omitempty"`
	LocalAddr  string `json:"local_addr"`
	LocalPort  uint32 `json:"local_port"`
	RemoteAddr string `json:"remote_addr,omitempty"`
	RemotePort uint32 `json:"remote_port,omitempty"`
	Live       bool   `json:"live"`
}

// Match is the GET /network/resolve verdict. Status not_found is a 200
// with confidence 0 — HTTP 404 is reserved for unknown routes.
//
// Confidence is deterministic per match tier (docs/api/network-flows.md
// §Match semantics) so the backend can threshold on it without modeling
// agent version drift:
//
//	0.97 exact 5-tuple, socket live
//	0.90 exact 5-tuple from the retention window (socket closed)
//	0.85 listening socket covering an inbound flow
//	0.80 UDP local-port owner (unconnected socket)
//	0.50 port-only owner (weak hint — do not automate policy on this)
type Match struct {
	Status     string         `json:"status"` // matched | probable | ambiguous | not_found
	Confidence float64        `json:"confidence"`
	Reason     string         `json:"reason"`
	Socket     *MatchedSocket `json:"socket,omitempty"`
	Owner      *Owner         `json:"owner,omitempty"`
}

const (
	confExactLive  = 0.97
	confExactCache = 0.90
	confListen     = 0.85
	confUDPPort    = 0.80
	confPortOnly   = 0.50
)

// Resolve maps one gateway 5-tuple to the best local owner, walking the
// tiers strongest-first. Ambiguity (several distinct PIDs equally good at
// the winning tier) downgrades status but still returns the most recent
// candidate — the backend decides whether to trust it.
func (c *Collector) Resolve(q Query) Match {
	q.LocalAddr = normAddr(q.LocalAddr)
	q.RemoteAddr = normAddr(q.RemoteAddr)

	c.mu.RLock()
	defer c.mu.RUnlock()

	type tier struct {
		match  func(*FlowItem) bool
		status string
		conf   float64
		reason string
	}
	tiers := []tier{
		{
			match: func(it *FlowItem) bool {
				return it.Live && exactTuple(it, q)
			},
			status: "matched", conf: confExactLive,
			reason: "exact 5-tuple, socket live",
		},
		{
			match: func(it *FlowItem) bool {
				return !it.Live && exactTuple(it, q)
			},
			status: "probable", conf: confExactCache,
			reason: "exact 5-tuple from recent history; socket closed",
		},
		{
			match: func(it *FlowItem) bool {
				return q.Proto == "tcp" && it.Proto == "tcp" && it.State == "listen" &&
					it.LocalPort == q.LocalPort && localCovers(it.LocalAddr, q.LocalAddr)
			},
			status: "probable", conf: confListen,
			reason: "listening socket covers inbound flow; accept path may be a child process",
		},
		{
			match: func(it *FlowItem) bool {
				return q.Proto == "udp" && it.Proto == "udp" &&
					it.LocalPort == q.LocalPort && localCovers(it.LocalAddr, q.LocalAddr) &&
					(it.RemoteAddr == "" || it.RemoteAddr == q.RemoteAddr)
			},
			status: "probable", conf: confUDPPort,
			reason: "udp local-port owner",
		},
		{
			match: func(it *FlowItem) bool {
				return it.Proto == q.Proto && it.LocalPort == q.LocalPort
			},
			status: "probable", conf: confPortOnly,
			reason: "port-only owner; remote peer not observed",
		},
	}

	for _, t := range tiers {
		var candidates []*FlowItem
		for _, e := range c.entries {
			if t.match(&e.item) {
				candidates = append(candidates, &e.item)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		best := candidates[0]
		pids := map[int32]bool{}
		for _, it := range candidates {
			pids[it.PID] = true
			if it.LastSeenNS > best.LastSeenNS {
				best = it
			}
		}
		m := Match{Status: t.status, Confidence: t.conf, Reason: t.reason}
		if len(pids) > 1 {
			m.Status = "ambiguous"
			m.Confidence = t.conf * 0.7
			m.Reason = fmt.Sprintf("%s; %d distinct owner pids match — returning most recent", t.reason, len(pids))
		}
		m.Socket = &MatchedSocket{
			Proto: best.Proto, State: best.State, Live: best.Live,
			LocalAddr: best.LocalAddr, LocalPort: best.LocalPort,
			RemoteAddr: best.RemoteAddr, RemotePort: best.RemotePort,
		}
		m.Owner = &Owner{
			PID: best.PID, ProcessName: best.ProcessName, CmdlineHead: best.CmdlineHead,
			Exe: best.Exe, User: best.User, UID: best.UID,
			Service: best.Service, ContainerID: best.ContainerID,
			ContainerName: best.ContainerName, Cgroup: best.Cgroup,
		}
		return m
	}

	reason := "no socket observed for tuple"
	if q.ObservedAtNS > 0 {
		age := time.Now().UnixNano() - q.ObservedAtNS
		if age > int64(c.cfg.WindowS)*int64(time.Second) {
			reason = fmt.Sprintf("no socket observed for tuple; observed_at is %ds old, outside the %ds retention window",
				age/int64(time.Second), c.cfg.WindowS)
		}
	}
	return Match{Status: "not_found", Confidence: 0, Reason: reason}
}

// exactTuple: full 5-tuple equality, treating a wildcard local bind as
// covering the queried local address.
func exactTuple(it *FlowItem, q Query) bool {
	return it.Proto == q.Proto &&
		it.LocalPort == q.LocalPort && localCovers(it.LocalAddr, q.LocalAddr) &&
		it.RemotePort == q.RemotePort && it.RemoteAddr == q.RemoteAddr
}

// localCovers reports whether a socket's local address covers the queried
// one — equal, or the socket is bound to the any-address.
func localCovers(sockAddr, queryAddr string) bool {
	return sockAddr == queryAddr || isWildcard(sockAddr)
}
