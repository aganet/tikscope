package syslog

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		line string
		want Line
	}{
		{"bare query", "dns query from 192.168.88.10: #123 Example.COM. A",
			Line{Kind: KindQuery, Client: "192.168.88.10", ID: "123", Domain: "example.com", Type: "A"}},
		{"iso timestamp", "2025-01-15 14:23:45 dns query from 192.168.1.100: #12345 google.com. AAAA",
			Line{Kind: KindQuery, Client: "192.168.1.100", ID: "12345", Domain: "google.com", Type: "AAAA"}},
		{"bsd envelope with prefix", "<30>Nov 12 13:34:00 router MT-DNS: query from 10.0.0.5: #8 cdn.example.net. HTTPS",
			Line{Kind: KindQuery, Client: "10.0.0.5", ID: "8", Domain: "cdn.example.net", Type: "HTTPS"}},
		{"unknown numeric type", "dns query from 10.0.0.5: #9 example.org. UNKNOWN (65)",
			Line{Kind: KindQuery, Client: "10.0.0.5", ID: "9", Domain: "example.org", Type: "HTTPS"}},
		{"unmapped numeric type", "dns query from 10.0.0.5: #9 example.org. UNKNOWN (12345)",
			Line{Kind: KindQuery, Client: "10.0.0.5", ID: "9", Domain: "example.org", Type: "TYPE12345"}},
		{"ipv6 client", "dns query from fd00::1:2: #77 example.org. A",
			Line{Kind: KindQuery, Client: "fd00::1:2", ID: "77", Domain: "example.org", Type: "A"}},
		{"local query", "dns local query: #33347 cloud.mikrotik.com. A",
			Line{Kind: KindQuery, ID: "33347", Domain: "cloud.mikrotik.com", Type: "A"}},
		{"answer", "dns done query: #33347 cloud.mikrotik.com 91.188.51.139",
			Line{Kind: KindAnswer, ID: "33347", Domain: "cloud.mikrotik.com", Outcome: OutcomeOK, Answers: []string{"91.188.51.139"}}},
		{"answer trailing dot, two records", "dns done query: #239527 www.example.gov. 192.0.66.168 192.0.66.169",
			Line{Kind: KindAnswer, ID: "239527", Domain: "www.example.gov", Outcome: OutcomeOK, Answers: []string{"192.0.66.168", "192.0.66.169"}}},
		{"answer blocked", "dns done query: #5 ads.example.com 0.0.0.0",
			Line{Kind: KindAnswer, ID: "5", Domain: "ads.example.com", Outcome: OutcomeBlocked, Answers: []string{"0.0.0.0"}}},
		{"answer cname only", "dns done query: #3129141 time.g.aaplimg.com.",
			Line{Kind: KindAnswer, ID: "3129141", Domain: "time.g.aaplimg.com", Outcome: OutcomeOK}},
		{"answer nxdomain", "dns done query: #25 dns name does not exist",
			Line{Kind: KindAnswer, ID: "25", Outcome: OutcomeNXDomain, Answers: []string{"name does not exist"}}},
		{"answer nodata", "dns done query: #26 dns name exists, but no appropriate record",
			Line{Kind: KindAnswer, ID: "26", Outcome: OutcomeNoData, Answers: []string{"name exists, but no appropriate record"}}},
		{"answer servfail", "dns done query: #27 dns server failure",
			Line{Kind: KindAnswer, ID: "27", Outcome: OutcomeServFail, Answers: []string{"server failure"}}},
		{"answer unknown error", "dns done query: #28 dns query refused",
			Line{Kind: KindAnswer, ID: "28", Outcome: OutcomeError, Answers: []string{"query refused"}}},
		{"answer for a domain named dns", "dns done query: #29 dns 10.0.0.1",
			Line{Kind: KindAnswer, ID: "29", Domain: "dns", Outcome: OutcomeOK, Answers: []string{"10.0.0.1"}}},
		{"dhcp lease with hostname", "dhcp,info defconf assigned 192.168.88.37 for B0:E4:5C:27:EF:F2 Samsung Galaxy",
			Line{Kind: KindEvent, Topics: []string{"dhcp", "info"}, Message: "defconf assigned 192.168.88.37 for B0:E4:5C:27:EF:F2 Samsung Galaxy", Severity: "info",
				Lease: &Lease{IP: "192.168.88.37", MAC: "B0:E4:5C:27:EF:F2", Name: "Samsung Galaxy"}}},
		{"dhcp lease legacy wording with envelope", "<30>Nov 12 13:34:00 rb dhcp,info dhcp1 assigned 192.168.88.10 to aa:bb:cc:dd:ee:01",
			Line{Kind: KindEvent, Topics: []string{"dhcp", "info"}, Message: "dhcp1 assigned 192.168.88.10 to aa:bb:cc:dd:ee:01", Severity: "info",
				Lease: &Lease{IP: "192.168.88.10", MAC: "AA:BB:CC:DD:EE:01"}}},
		{"link down is an error event", "2025-11-12 10:00:00 interface,error ether1 link down",
			Line{Kind: KindEvent, Topics: []string{"interface", "error"}, Message: "ether1 link down", Severity: "error"}},
		{"critical wins", "system,error,critical router was rebooted without proper shutdown",
			Line{Kind: KindEvent, Topics: []string{"system", "error", "critical"}, Message: "router was rebooted without proper shutdown", Severity: "critical"}},
		{"warning", "dhcp,warning defconf offering lease without success",
			Line{Kind: KindEvent, Topics: []string{"dhcp", "warning"}, Message: "defconf offering lease without success", Severity: "warning"}},
		{"line without topics", "<14>Nov 12 13:34:00 Hello world",
			Line{Kind: KindEvent, Message: "Hello world", Severity: "info"}},
		{"crlf", "dns query from 10.0.0.5: #1 example.com. A\r\n",
			Line{Kind: KindQuery, Client: "10.0.0.5", ID: "1", Domain: "example.com", Type: "A"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Parse(tt.line); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%q)\n got %+v\nwant %+v", tt.line, got, tt.want)
			}
		})
	}
}
