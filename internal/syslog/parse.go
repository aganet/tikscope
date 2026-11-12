// Package syslog turns RouterOS remote log lines into typed events.
package syslog

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Outcome of a DNS request as reported by the router. Empty means the router
// never logged an answer line.
const (
	OutcomeOK       = "ok"
	OutcomeNoData   = "nodata"
	OutcomeNXDomain = "nxdomain"
	OutcomeServFail = "servfail"
	OutcomeBlocked  = "blocked"
	OutcomeError    = "error"
)

type Kind int

const (
	KindEvent Kind = iota // any line that is not a DNS query or answer
	KindQuery
	KindAnswer
)

// Line is one parsed log line.
type Line struct {
	Kind Kind

	// KindQuery and KindAnswer
	ID      string
	Client  string // empty for the router's own queries
	Domain  string
	Type    string
	Outcome string
	Answers []string

	// KindEvent
	Topics   []string
	Message  string
	Severity string // info, warning, error, critical

	// DHCP lease events carry the device identity.
	Lease *Lease
}

type Lease struct {
	IP   string
	MAC  string
	Name string
}

// The DNS patterns are unanchored at the start so any syslog envelope (PRI,
// timestamp, hostname, topics, user prefix) in front of the message is skipped.
var (
	reQuery    = regexp.MustCompile(`(?:^|\s)(?:query from (\S+)|local query): #(\d+) (\S+?)\.? (\S+(?: \(\d+\))?)$`)
	reAnswer   = regexp.MustCompile(`(?:^|\s)done query: #(\d+)(?: (.*))?$`)
	reLease    = regexp.MustCompile(`(?:^|\s)\S+ assigned (\S+) (?:for|to) ([0-9A-Fa-f]{2}(?::[0-9A-Fa-f]{2}){5})(?: (.+))?$`)
	reEnvelope = regexp.MustCompile(`^(?:<\d{1,3}>)?(?:\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})? |[A-Z][a-z]{2} [ \d]\d \d{2}:\d{2}:\d{2} )?`)
	reTopics   = regexp.MustCompile(`^([a-z][a-z0-9]*(?:,[a-z][a-z0-9]*)*) `)
	reHostname = regexp.MustCompile(`^\S+ `)
)

var answerErrors = map[string]string{
	"name does not exist":                    OutcomeNXDomain,
	"name exists, but no appropriate record": OutcomeNoData,
	"server failure":                         OutcomeServFail,
}

// Parse classifies one line. It never fails: anything unrecognised becomes
// an event with whatever topics could be read from it.
func Parse(raw string) Line {
	line := strings.TrimRight(raw, "\r\n\x00 ")
	if m := reQuery.FindStringSubmatch(line); m != nil {
		return Line{Kind: KindQuery, Client: m[1], ID: m[2], Domain: strings.ToLower(m[3]), Type: normalizeType(m[4])}
	}
	if m := reAnswer.FindStringSubmatch(line); m != nil {
		l := parseAnswer(m[2])
		l.ID = m[1]
		return l
	}

	body := reEnvelope.ReplaceAllString(line, "")
	// BSD syslog puts a hostname between the timestamp and the topics. A
	// hostname can look like a single topic, so the token is only taken as
	// a hostname when a comma-separated topic list follows it.
	if first := reTopics.FindString(body); !strings.Contains(first, ",") {
		if h := reHostname.FindString(body); h != "" {
			if next := reTopics.FindString(body[len(h):]); strings.Contains(next, ",") {
				body = body[len(h):]
			}
		}
	}
	l := Line{Kind: KindEvent, Message: body, Severity: "info"}
	if m := reTopics.FindStringSubmatch(body); m != nil {
		l.Topics = strings.Split(m[1], ",")
		l.Message = strings.TrimSpace(body[len(m[0]):])
	}
	for _, t := range l.Topics {
		switch t {
		case "critical":
			l.Severity = "critical"
		case "error":
			if l.Severity != "critical" {
				l.Severity = "error"
			}
		case "warning":
			if l.Severity == "info" {
				l.Severity = "warning"
			}
		}
	}
	if m := reLease.FindStringSubmatch(line); m != nil {
		l.Lease = &Lease{IP: m[1], MAC: strings.ToUpper(m[2]), Name: strings.TrimSpace(m[3])}
	}
	return l
}

// parseAnswer reads the text after "done query: #id": either
// "<name> <record>..." or an error description starting with "dns ".
func parseAnswer(rest string) Line {
	l := Line{Kind: KindAnswer, Outcome: OutcomeOK}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return l
	}
	if fields[0] == "dns" && len(fields) > 1 && !isAddress(fields[1]) {
		msg := strings.Join(fields[1:], " ")
		l.Outcome = answerErrors[msg]
		if l.Outcome == "" {
			l.Outcome = OutcomeError
		}
		l.Answers = []string{msg}
		return l
	}
	l.Domain = strings.ToLower(strings.TrimSuffix(fields[0], "."))
	for _, a := range fields[1:] {
		a = strings.TrimSuffix(a, ".")
		if a == "0.0.0.0" || a == "::" {
			l.Outcome = OutcomeBlocked
		}
		l.Answers = append(l.Answers, a)
	}
	return l
}

func isAddress(s string) bool {
	_, err := netip.ParseAddr(s)
	return err == nil
}

// normalizeType maps "UNKNOWN (65)" style types to their RFC names.
func normalizeType(t string) string {
	if !strings.HasPrefix(t, "UNKNOWN") {
		return t
	}
	open, close := strings.IndexByte(t, '('), strings.IndexByte(t, ')')
	if open < 0 || close <= open {
		return "UNKNOWN"
	}
	n, err := strconv.Atoi(t[open+1 : close])
	if err != nil {
		return "UNKNOWN"
	}
	if name, ok := typeNames[n]; ok {
		return name
	}
	return "TYPE" + strconv.Itoa(n)
}

var typeNames = map[int]string{
	1: "A", 2: "NS", 5: "CNAME", 6: "SOA", 12: "PTR", 13: "HINFO", 15: "MX", 16: "TXT",
	17: "RP", 18: "AFSDB", 24: "SIG", 25: "KEY", 28: "AAAA", 29: "LOC", 33: "SRV",
	35: "NAPTR", 36: "KX", 37: "CERT", 39: "DNAME", 41: "OPT", 42: "APL", 43: "DS",
	44: "SSHFP", 45: "IPSECKEY", 46: "RRSIG", 47: "NSEC", 48: "DNSKEY", 49: "DHCID",
	50: "NSEC3", 51: "NSEC3PARAM", 52: "TLSA", 53: "SMIMEA", 55: "HIP", 59: "CDS",
	60: "CDNSKEY", 61: "OPENPGPKEY", 62: "CSYNC", 63: "ZONEMD", 64: "SVCB", 65: "HTTPS",
	99: "SPF", 108: "EUI48", 109: "EUI64", 249: "TKEY", 250: "TSIG", 252: "AXFR",
	255: "ANY", 256: "URI", 257: "CAA", 32768: "TA", 32769: "DLV",
}
