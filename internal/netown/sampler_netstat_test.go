package netown

import "testing"

func TestParseDarwinNetstatTCPSynSent(t *testing.T) {
	line := "tcp4       0      0  192.168.50.197.62027   17.253.83.137.443      SYN_SENT               0          512  131072  131072   com.apple.geod:302    00000 00000000"
	item, ok := parseDarwinNetstatTCPLine(line)
	if !ok {
		t.Fatal("line did not parse")
	}
	if item.Proto != "tcp" || item.State != "syn_sent" {
		t.Fatalf("proto/state = %s/%s", item.Proto, item.State)
	}
	if item.LocalAddr != "192.168.50.197" || item.LocalPort != 62027 {
		t.Fatalf("local = %s:%d", item.LocalAddr, item.LocalPort)
	}
	if item.RemoteAddr != "17.253.83.137" || item.RemotePort != 443 {
		t.Fatalf("remote = %s:%d", item.RemoteAddr, item.RemotePort)
	}
	if item.PID != 302 || item.ProcessName != "com.apple.geod" {
		t.Fatalf("owner = %s:%d", item.ProcessName, item.PID)
	}
}

func TestParseDarwinNetstatTCPProcessNameWithSpaces(t *testing.T) {
	line := "tcp4       0      0  192.168.50.197.55322   63.140.37.151.443      SYN_SENT               0          192  131072  131072 Creative Cloud C:21369  00000 00000000"
	item, ok := parseDarwinNetstatTCPLine(line)
	if !ok {
		t.Fatal("line did not parse")
	}
	if item.PID != 21369 || item.ProcessName != "Creative Cloud C" {
		t.Fatalf("owner = %q:%d", item.ProcessName, item.PID)
	}
}

func TestMergeRawConnsPrefersNetstatPIDForSameTuple(t *testing.T) {
	base := []RawConn{{
		Proto:      "tcp",
		LocalAddr:  "192.168.50.197",
		LocalPort:  62027,
		RemoteAddr: "17.253.83.137",
		RemotePort: 443,
		State:      "syn_sent",
	}}
	extra := []RawConn{{
		Proto:       "tcp",
		LocalAddr:   "192.168.50.197",
		LocalPort:   62027,
		RemoteAddr:  "17.253.83.137",
		RemotePort:  443,
		State:       "syn_sent",
		PID:         302,
		ProcessName: "com.apple.geod",
	}}
	got := mergeRawConns(base, extra)
	if len(got) != 1 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].PID != 302 || got[0].ProcessName != "com.apple.geod" {
		t.Fatalf("merged owner = %+v", got[0])
	}
}
