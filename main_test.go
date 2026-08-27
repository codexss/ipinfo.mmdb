package main

import (
	"bytes"
	"io"
	"net/netip"
	"strings"
	"testing"

	"github.com/oschwald/maxminddb-golang/v2"
)

const sampleCSV = `network,country,country_code,continent,continent_code,asn,as_name,as_domain
1.0.0.0/24,Australia,AU,Oceania,OC,,,
1.0.1.0/24,Australia,AU,Oceania,OC,,,
1.0.2.0/24,China,CN,Asia,AS,,,
2001:db8::/33,Australia,AU,Oceania,OC,,,
2001:db8:8000::/33,Australia,AU,Oceania,OC,,,
`

type lookupRecord struct {
	Country struct {
		ISOCode string            `maxminddb:"iso_code"`
		Names   map[string]string `maxminddb:"names"`
	} `maxminddb:"country"`
}

func TestBuildTree(t *testing.T) {
	tree, result, err := buildTree(strings.NewReader(sampleCSV), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result != (stats{Rows: 5, Countries: 2, MergedRanges: 3}) {
		t.Fatalf("unexpected stats: %#v", result)
	}

	var database bytes.Buffer
	if _, err := tree.WriteTo(&database); err != nil {
		t.Fatal(err)
	}
	reader, err := maxminddb.OpenBytes(database.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if reader.Metadata.DatabaseType != "GeoIP2-Country" || reader.Metadata.IPVersion != 6 {
		t.Fatalf("unexpected metadata: %#v", reader.Metadata)
	}

	tests := []struct {
		address string
		code    string
		name    string
	}{
		{"1.0.0.1", "AU", "Australia"},
		{"1.0.2.1", "CN", "China"},
		{"2001:db8::1", "AU", "Australia"},
		{"8.8.8.8", "", ""},
	}
	for _, test := range tests {
		var record lookupRecord
		if err := reader.Lookup(netip.MustParseAddr(test.address)).Decode(&record); err != nil {
			t.Fatalf("lookup %s: %v", test.address, err)
		}
		if record.Country.ISOCode != test.code || record.Country.Names["en"] != test.name {
			t.Errorf("lookup %s returned %#v", test.address, record)
		}
	}
}

func TestBuildTreeRejectsUnsortedNetworks(t *testing.T) {
	input := `network,country,country_code
2.0.0.0/24,Australia,AU
1.0.0.0/24,China,CN
`
	_, _, err := buildTree(strings.NewReader(input), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "out of order") {
		t.Fatalf("expected ordering error, got %v", err)
	}
}

func TestLastAddress(t *testing.T) {
	tests := map[string]string{
		"0.0.0.0/0":     "255.255.255.255",
		"1.2.3.0/24":    "1.2.3.255",
		"2001:db8::/32": "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff",
		"::/0":          "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff",
	}
	for prefixText, expected := range tests {
		prefix := netip.MustParsePrefix(prefixText)
		if actual := lastAddress(prefix).String(); actual != expected {
			t.Errorf("lastAddress(%s) = %s, want %s", prefix, actual, expected)
		}
	}
}

func TestParseNetworkAcceptsHostAddress(t *testing.T) {
	tests := map[string]string{
		"1.7.168.174": "1.7.168.174/32",
		"2001:db8::1": "2001:db8::1/128",
	}
	for input, expected := range tests {
		prefix, err := parseNetwork(input)
		if err != nil {
			t.Fatal(err)
		}
		if prefix.String() != expected {
			t.Errorf("parseNetwork(%q) = %s, want %s", input, prefix, expected)
		}
	}
}
