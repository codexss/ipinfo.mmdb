package main

import (
	"bytes"
	"io"
	"net/netip"
	"os"
	"path/filepath"
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
	if err := reader.Verify(); err != nil {
		t.Fatalf("verify database: %v", err)
	}

	metadata := reader.Metadata
	if metadata.DatabaseType != "GeoIP2-Country" || metadata.IPVersion != 6 || metadata.RecordSize != 28 {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if len(metadata.Languages) != 1 || metadata.Languages[0] != "en" {
		t.Fatalf("unexpected languages: %#v", metadata.Languages)
	}

	tests := []struct {
		address string
		code    string
		name    string
	}{
		{"1.0.0.0", "AU", "Australia"},
		{"1.0.1.255", "AU", "Australia"},
		{"1.0.2.0", "CN", "China"},
		{"1.0.2.255", "CN", "China"},
		{"1.0.3.0", "", ""},
		{"2001:db8::", "AU", "Australia"},
		{"2001:db8:ffff:ffff:ffff:ffff:ffff:ffff", "AU", "Australia"},
		{"2001:db9::", "", ""},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			var record lookupRecord
			if err := reader.Lookup(netip.MustParseAddr(test.address)).Decode(&record); err != nil {
				t.Fatal(err)
			}
			if record.Country.ISOCode != test.code || record.Country.Names["en"] != test.name {
				t.Fatalf("unexpected record: %#v", record)
			}
		})
	}
}

func TestBuildTreeAcceptsBOMAndReorderedColumns(t *testing.T) {
	input := "\ufeffcountry_code,network,country\nAU,1.0.0.1,Australia\n"
	_, result, err := buildTree(strings.NewReader(input), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result != (stats{Rows: 1, Countries: 1, MergedRanges: 1}) {
		t.Fatalf("unexpected stats: %#v", result)
	}
}

func TestBuildTreeRejectsInvalidInput(t *testing.T) {
	tests := map[string]string{
		"unsorted": "network,country,country_code\n2.0.0.0/24,Australia,AU\n1.0.0.0/24,China,CN\n",
		"overlap":  "network,country,country_code\n1.0.0.0/24,Australia,AU\n1.0.0.0/25,Australia,AU\n",
		"conflict": "network,country,country_code\n1.0.0.0/24,Australia,AU\n1.0.1.0/24,Austria,AU\n",
		"missing":  "network,country\n1.0.0.0/24,Australia\n",
		"empty":    "network,country,country_code\n",
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := buildTree(strings.NewReader(input), io.Discard); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestConvertReaderWritesReadableFileAtomically(t *testing.T) {
	output := filepath.Join(t.TempDir(), "country.mmdb")
	result, err := convertReader(strings.NewReader(sampleCSV), output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows != 5 {
		t.Fatalf("unexpected stats: %#v", result)
	}

	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("output permissions = %o, want 644", info.Mode().Perm())
	}
	reader, err := maxminddb.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := reader.Verify(); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(output, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := convertReader(strings.NewReader("bad header\n"), output, io.Discard); err == nil {
		t.Fatal("expected conversion error")
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "existing" {
		t.Fatalf("existing output was changed: %q", content)
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
