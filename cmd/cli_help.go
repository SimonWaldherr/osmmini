package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
)

// cliSubcommands lists the git-style `osmmini <verb>` tools dispatched in
// main(). Keep this in sync with the switch there; `osmmini help` prints it.
var cliSubcommands = []struct{ name, summary string }{
	{"route", "Compute one route from a PBF and print JSON, GPX or GeoJSON"},
	{"territories load", "Validate and register a territory GeoJSON layer"},
	{"territory lookup", "Find the territory containing a coordinate"},
	{"dispatch assign", "Assign CSV parcels/addresses to territories"},
	{"dispatch manifests", "Write one CSV manifest per territory group"},
	{"geodata import", "Import GeoJSON, KML/KMZ or Shapefile as a map layer"},
	{"pbf-index", "Write a spatial block sidecar index for a large PBF"},
	{"region-extract", "Cut a complete regional PBF from a large one (uses osmium)"},
	{"version", "Print version and build information"},
	{"help", "Show this overview"},
}

// versionString reports the module version and, for builds from a git
// checkout, the VCS revision recorded by the Go toolchain.
func versionString() string {
	version, revision, modified := "devel", "", false
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			version = v
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	out := "osmmini " + version
	if len(revision) > 12 {
		revision = revision[:12]
	}
	// Pseudo-versions (v0.0.0-<date>-<rev>) already carry the revision.
	if revision != "" && !strings.Contains(version, revision) {
		out += " (" + revision
		if modified {
			out += ", modified"
		}
		out += ")"
	}
	return fmt.Sprintf("%s %s %s/%s", out, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// printUsage writes the top-level help: server usage, subcommands and the
// server flags registered on the default flag set.
func printUsage(w io.Writer) {
	fmt.Fprintf(w, `OSMmini – offline routing server and map UI for OSM PBF extracts

Usage:
  osmmini [server flags]              start the HTTP server (default)
  osmmini <command> [command flags]   run a standalone tool

Commands:
`)
	for _, c := range cliSubcommands {
		fmt.Fprintf(w, "  %-19s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(w, `
Run "osmmini <command> -h" for the flags of a command.

Server flags:
`)
	flag.CommandLine.SetOutput(w)
	flag.PrintDefaults()
	flag.CommandLine.SetOutput(os.Stderr)
}
