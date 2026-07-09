package netown

import (
	"strconv"
	"strings"
)

func mergeRawConns(base []RawConn, extra []RawConn) []RawConn {
	if len(extra) == 0 {
		return base
	}
	out := append([]RawConn{}, base...)
	index := map[string]int{}
	for i, item := range out {
		index[rawConnTupleKey(item)] = i
	}
	for _, item := range extra {
		if item.Proto == "" || item.LocalPort == 0 {
			continue
		}
		k := rawConnTupleKey(item)
		if i, ok := index[k]; ok {
			if out[i].PID == 0 && item.PID != 0 {
				out[i] = item
			}
			continue
		}
		index[k] = len(out)
		out = append(out, item)
	}
	return out
}

func rawConnTupleKey(item RawConn) string {
	return strings.Join([]string{
		item.Proto,
		normAddr(item.LocalAddr),
		strconv.FormatUint(uint64(item.LocalPort), 10),
		normAddr(item.RemoteAddr),
		strconv.FormatUint(uint64(item.RemotePort), 10),
	}, "|")
}

func parseDarwinNetstatTCP(output []byte) []RawConn {
	lines := strings.Split(string(output), "\n")
	out := make([]RawConn, 0, len(lines))
	for _, line := range lines {
		item, ok := parseDarwinNetstatTCPLine(line)
		if ok {
			out = append(out, item)
		}
	}
	return out
}

func parseDarwinNetstatTCPLine(line string) (RawConn, bool) {
	fields := strings.Fields(line)
	if len(fields) < 6 || !strings.HasPrefix(fields[0], "tcp") {
		return RawConn{}, false
	}
	localAddr, localPort, ok := parseDarwinNetstatEndpoint(fields[3])
	if !ok || localPort == 0 {
		return RawConn{}, false
	}
	remoteAddr, remotePort, _ := parseDarwinNetstatEndpoint(fields[4])
	pid, processName := parseDarwinNetstatProcess(fields)
	return RawConn{
		Proto:       "tcp",
		LocalAddr:   localAddr,
		LocalPort:   localPort,
		RemoteAddr:  remoteAddr,
		RemotePort:  remotePort,
		State:       strings.ToLower(strings.ReplaceAll(fields[5], "-", "_")),
		PID:         pid,
		ProcessName: processName,
	}, true
}

func parseDarwinNetstatEndpoint(raw string) (string, uint32, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "*.*" || raw == "*" {
		return "", 0, false
	}
	i := strings.LastIndexByte(raw, '.')
	if i < 0 || i == len(raw)-1 {
		return raw, 0, true
	}
	port, err := strconv.ParseUint(raw[i+1:], 10, 32)
	if err != nil {
		return raw, 0, true
	}
	addr := raw[:i]
	if addr == "*" {
		addr = ""
	}
	return addr, uint32(port), true
}

func parseDarwinNetstatProcess(fields []string) (int32, string) {
	for i := 10; i < len(fields); i++ {
		token := fields[i]
		colon := strings.LastIndexByte(token, ':')
		if colon < 0 || colon == len(token)-1 {
			continue
		}
		pid, err := strconv.ParseInt(token[colon+1:], 10, 32)
		if err != nil {
			continue
		}
		nameParts := append([]string{}, fields[10:i]...)
		nameParts = append(nameParts, token[:colon])
		return int32(pid), strings.TrimSpace(strings.Join(nameParts, " "))
	}
	return 0, ""
}
