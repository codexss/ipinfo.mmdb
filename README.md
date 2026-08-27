# IPinfo Country MMDB

Convert the IPinfo Lite CSV database to a country-only MaxMind DB file.

## Download

- [Download the latest `ipinfo_country.mmdb`](https://github.com/codexss/ipinfo.mmdb/releases/latest/download/ipinfo_country.mmdb)

The converter uses MaxMind's official Go writer and emits records in this form:

```json
{
  "country": {
    "iso_code": "CN",
    "names": {
      "en": "China"
    }
  }
}
```

## Requirements

- Go 1.24 or later
- An IPinfo Lite CSV file sorted by network address

## Usage

Build and run with the default paths `ipinfo_lite.csv` and
`ipinfo_country.mmdb`:

```sh
go build -o ipinfo-country-mmdb .
./ipinfo-country-mmdb
```

Or provide explicit paths:

```sh
go run . input.csv output.mmdb
```

The converter supports IPv4 and IPv6 CIDRs as well as individual host
addresses. It merges adjacent networks assigned to the same country while
streaming the CSV, then atomically replaces the output after a successful
build.

## Test

```sh
go test ./...
go vet ./...
```

## GitHub Actions

The `GeoIP Updater` workflow runs daily at 01:00 UTC and can also be started
manually or through `repository_dispatch`. Add an `IPINFO_TOKEN` repository
secret before running it. The workflow tests the converter, downloads the
current IPinfo Lite CSV, and publishes `ipinfo_country.mmdb` plus its SHA-256
checksum to a release tagged with the UTC date (`YYYY.MM.DD`). It does not
create or update a download branch. The latest two releases and workflow runs
are retained.
