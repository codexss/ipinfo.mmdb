package main

import (
	"encoding/binary"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

const (
	defaultInput  = "ipinfo_lite.csv"
	defaultOutput = "ipinfo_country.mmdb"
)

type stats struct {
	Rows         int
	Countries    int
	MergedRanges int
}

type countryRange struct {
	start netip.Addr
	end   netip.Addr
	code  string
	name  string
}

// countryKeyGenerator avoids hashing the same small country record for every range.
type countryKeyGenerator struct{}

func (countryKeyGenerator) Key(value mmdbtype.DataType) ([]byte, error) {
	record, ok := value.(mmdbtype.Map)
	if !ok {
		return nil, errors.New("country record is not a map")
	}
	country, ok := record["country"].(mmdbtype.Map)
	if !ok {
		return nil, errors.New("country field is not a map")
	}
	code, ok := country["iso_code"].(mmdbtype.String)
	if !ok || code == "" {
		return nil, errors.New("country ISO code is missing")
	}
	return []byte(code), nil
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [input.csv] [output.mmdb]\n", filepath.Base(os.Args[0]))
		fmt.Fprintln(flag.CommandLine.Output(), "Convert IPinfo Lite CSV to a country-only GeoIP2 MMDB file.")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() > 2 {
		flag.Usage()
		os.Exit(2)
	}

	inputPath := defaultInput
	outputPath := defaultOutput
	if flag.NArg() >= 1 {
		inputPath = flag.Arg(0)
	}
	if flag.NArg() == 2 {
		outputPath = flag.Arg(1)
	}

	result, err := convert(inputPath, outputPath, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(
		"Wrote %s from %d rows, %d countries, and %d merged ranges.\n",
		outputPath,
		result.Rows,
		result.Countries,
		result.MergedRanges,
	)
}

func convert(inputPath, outputPath string, progress io.Writer) (stats, error) {
	input, err := os.Open(inputPath)
	if err != nil {
		return stats{}, fmt.Errorf("opening input: %w", err)
	}
	defer input.Close()

	tree, result, err := buildTree(input, progress)
	if err != nil {
		return stats{}, err
	}
	if err := writeAtomically(tree, outputPath); err != nil {
		return stats{}, err
	}
	return result, nil
}

func buildTree(input io.Reader, progress io.Writer) (*mmdbwriter.Tree, stats, error) {
	tree, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType:            "GeoIP2-Country",
		Description:             map[string]string{"en": "IPinfo Lite country database"},
		IncludeReservedNetworks: true,
		IPVersion:               6,
		KeyGenerator:            countryKeyGenerator{},
		Languages:               []string{"en"},
		RecordSize:              28,
	})
	if err != nil {
		return nil, stats{}, fmt.Errorf("creating MMDB tree: %w", err)
	}

	reader := csv.NewReader(input)
	reader.ReuseRecord = true
	header, err := reader.Read()
	if err != nil {
		return nil, stats{}, fmt.Errorf("reading CSV header: %w", err)
	}
	if len(header) > 0 {
		header[0] = strings.TrimPrefix(header[0], "\ufeff")
	}
	columns, err := requiredColumns(header)
	if err != nil {
		return nil, stats{}, err
	}

	countries := make(map[string]string, 256)
	result := stats{}
	var current *countryRange
	var previousEnd netip.Addr

	flush := func() error {
		if current == nil {
			return nil
		}
		record := countryRecord(current.code, current.name)
		if err := tree.InsertRange(toNetIP(current.start), toNetIP(current.end), record); err != nil {
			return fmt.Errorf("inserting range %s-%s: %w", current.start, current.end, err)
		}
		result.MergedRanges++
		return nil
	}

	for line := 2; ; line++ {
		row, readErr := reader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, stats{}, fmt.Errorf("reading CSV near line %d: %w", line, readErr)
		}

		networkText := strings.TrimSpace(row[columns["network"]])
		countryName := strings.TrimSpace(row[columns["country"]])
		countryCode := strings.TrimSpace(row[columns["country_code"]])
		if networkText == "" || countryName == "" || !validCountryCode(countryCode) {
			return nil, stats{}, fmt.Errorf("line %d: invalid network or country fields", line)
		}

		previousName, exists := countries[countryCode]
		if exists && previousName != countryName {
			return nil, stats{}, fmt.Errorf(
				"line %d: conflicting names for %s: %q and %q",
				line,
				countryCode,
				previousName,
				countryName,
			)
		}
		countries[countryCode] = countryName

		prefix, parseErr := parseNetwork(networkText)
		if parseErr != nil {
			return nil, stats{}, fmt.Errorf("line %d: invalid network %q: %w", line, networkText, parseErr)
		}
		prefix = prefix.Masked()
		start := prefix.Addr()
		end := lastAddress(prefix)
		if previousEnd.IsValid() && start.Compare(previousEnd) <= 0 {
			return nil, stats{}, fmt.Errorf(
				"line %d: network %s overlaps or is out of order after address %s",
				line,
				prefix,
				previousEnd,
			)
		}

		if current != nil && current.code == countryCode && current.name == countryName && current.end.Next() == start {
			current.end = end
		} else {
			if err := flush(); err != nil {
				return nil, stats{}, err
			}
			current = &countryRange{start: start, end: end, code: countryCode, name: countryName}
		}

		previousEnd = end
		result.Rows++
		if result.Rows%250_000 == 0 {
			fmt.Fprintf(progress, "Read %d rows (%d merged ranges)...\n", result.Rows, result.MergedRanges)
		}
	}

	if result.Rows == 0 {
		return nil, stats{}, errors.New("CSV contains no data rows")
	}
	if err := flush(); err != nil {
		return nil, stats{}, err
	}
	result.Countries = len(countries)
	return tree, result, nil
}

func parseNetwork(value string) (netip.Prefix, error) {
	if strings.Contains(value, "/") {
		return netip.ParsePrefix(value)
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func requiredColumns(header []string) (map[string]int, error) {
	required := map[string]int{"network": -1, "country": -1, "country_code": -1}
	for index, name := range header {
		if _, ok := required[name]; ok {
			required[name] = index
		}
	}
	var missing []string
	for name, index := range required {
		if index == -1 {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("CSV is missing required columns: %s", strings.Join(missing, ", "))
	}
	return required, nil
}

func validCountryCode(code string) bool {
	return len(code) == 2 && code[0] >= 'A' && code[0] <= 'Z' && code[1] >= 'A' && code[1] <= 'Z'
}

func countryRecord(code, name string) mmdbtype.Map {
	return mmdbtype.Map{
		"country": mmdbtype.Map{
			"iso_code": mmdbtype.String(code),
			"names": mmdbtype.Map{
				"en": mmdbtype.String(name),
			},
		},
	}
}

func lastAddress(prefix netip.Prefix) netip.Addr {
	addr := prefix.Addr()
	if addr.Is4() {
		raw := addr.As4()
		value := binary.BigEndian.Uint32(raw[:])
		hostBits := 32 - prefix.Bits()
		if hostBits == 32 {
			value = ^uint32(0)
		} else if hostBits > 0 {
			value |= (uint32(1) << hostBits) - 1
		}
		binary.BigEndian.PutUint32(raw[:], value)
		return netip.AddrFrom4(raw)
	}

	raw := addr.As16()
	fullBytes := prefix.Bits() / 8
	remainingBits := prefix.Bits() % 8
	if remainingBits != 0 {
		raw[fullBytes] |= byte(0xff >> remainingBits)
		fullBytes++
	}
	for index := fullBytes; index < len(raw); index++ {
		raw[index] = 0xff
	}
	return netip.AddrFrom16(raw)
}

func toNetIP(addr netip.Addr) net.IP {
	if addr.Is4() {
		raw := addr.As4()
		return net.IP(raw[:])
	}
	raw := addr.As16()
	return net.IP(raw[:])
}

func writeAtomically(tree *mmdbwriter.Tree, outputPath string) error {
	directory := filepath.Dir(outputPath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	temporary, err := os.CreateTemp(directory, "."+filepath.Base(outputPath)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := tree.WriteTo(temporary); err != nil {
		temporary.Close()
		return fmt.Errorf("writing MMDB: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("syncing MMDB: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing MMDB: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		return fmt.Errorf("replacing output: %w", err)
	}
	return nil
}
