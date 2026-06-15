// Command toise-conformance validates a producer's OTLP entity-event output
// against the Toise wire contract, without a running Toise. Feed it the bytes
// your producer would export (an OTLP ExportLogsServiceRequest, protobuf or
// JSON) and it reports every contract violation; a clean run means Toise will
// accept the records without per-record rejection.
//
//	# from a file (protobuf or JSON, auto-detected)
//	toise-conformance producer-output.bin
//	# or from stdin
//	my-producer --dump-otlp | toise-conformance
//
// It exits 0 when conformant, 1 when there are rejections, and 2 on a usage or
// decode error. Advisory problems (e.g. a missing service.instance.id) are
// reported but do not fail the run unless -strict is set.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"go.opentelemetry.io/collector/pdata/plog"
	"go.opentelemetry.io/collector/pdata/plog/plogotlp"

	"github.com/toise-dev/toise/pkg/emit/conformance"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("toise-conformance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	format := fs.String("format", "auto", "input format: auto, proto, or json")
	var strictMode bool
	fs.BoolVar(&strictMode, "strict", false, "fail (exit 1) on advisory problems too")
	fs.BoolVar(&strictMode, "s", false, "shorthand for -strict")
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: toise-conformance [-format auto|proto|json] [-s] [file]\n\n"+
			"Validate OTLP entity-event output against the Toise wire contract.\n"+
			"Reads from the given file, or stdin if none. See -h for flags.\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	data, err := readInput(fs.Arg(0), stdin)
	if err != nil {
		fmt.Fprintf(stderr, "toise-conformance: %v\n", err)
		return 2
	}
	if len(data) == 0 {
		fmt.Fprintf(stderr, "toise-conformance: no input (give a file argument or pipe OTLP bytes to stdin)\n")
		return 2
	}

	ld, err := decode(data, *format)
	if err != nil {
		fmt.Fprintf(stderr, "toise-conformance: %v\n", err)
		return 2
	}

	problems := conformance.Check(ld)
	return report(stdout, problems, strictMode)
}

func readInput(path string, stdin io.Reader) ([]byte, error) {
	if path == "" || path == "-" {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("reading stdin: %w", err)
		}
		return b, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return b, nil
}

// decode unmarshals an OTLP ExportLogsServiceRequest. In auto mode it treats
// input beginning with '{' (after whitespace) as JSON, otherwise protobuf.
func decode(data []byte, format string) (plog.Logs, error) {
	req := plogotlp.NewExportRequest()
	switch format {
	case "json":
		if err := req.UnmarshalJSON(data); err != nil {
			return plog.Logs{}, fmt.Errorf("decoding OTLP JSON: %w", err)
		}
	case "proto":
		if err := req.UnmarshalProto(data); err != nil {
			return plog.Logs{}, fmt.Errorf("decoding OTLP protobuf: %w", err)
		}
	case "auto":
		if looksJSON(data) {
			if err := req.UnmarshalJSON(data); err != nil {
				return plog.Logs{}, fmt.Errorf("decoding OTLP JSON: %w", err)
			}
		} else if err := req.UnmarshalProto(data); err != nil {
			return plog.Logs{}, fmt.Errorf("decoding OTLP protobuf (try -format json if this is JSON): %w", err)
		}
	default:
		return plog.Logs{}, fmt.Errorf("unknown -format %q (want auto, proto, or json)", format)
	}
	return req.Logs(), nil
}

func looksJSON(data []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(data)), "{")
}

// report prints problems and returns the exit code: 0 conformant, 1 with
// rejections (or advisories under strict).
func report(w io.Writer, problems []conformance.Problem, strict bool) int {
	var rejections, advisories []conformance.Problem
	for _, p := range problems {
		if p.Advisory {
			advisories = append(advisories, p)
		} else {
			rejections = append(rejections, p)
		}
	}

	if len(rejections) == 0 && len(advisories) == 0 {
		fmt.Fprintln(w, "conformant: no contract violations found")
		return 0
	}

	if len(rejections) > 0 {
		fmt.Fprintf(w, "%d rejection(s) — Toise would reject these records:\n", len(rejections))
		for _, p := range rejections {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
	if len(advisories) > 0 {
		fmt.Fprintf(w, "%d advisory(ies) — accepted, but degrade consumer behavior:\n", len(advisories))
		for _, p := range advisories {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}

	if len(rejections) > 0 || (strict && len(advisories) > 0) {
		return 1
	}
	return 0
}
