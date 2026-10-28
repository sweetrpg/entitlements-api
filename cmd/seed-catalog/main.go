// Command seed-catalog loads the catalog seed through a running entitlements-api's admin API.
//
//	ENTITLEMENTS_API_URL=https://host/api/1/entitlements ENTITLEMENTS_ADMIN_TOKEN=... seed-catalog
//
// It creates what is missing and never overwrites existing rows. Use -dry-run to preview and
// -file to load a different seed than the embedded Initiative one.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sweetrpg/entitlements-api/seed"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "seed-catalog:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("seed-catalog", flag.ContinueOnError)
	file := fs.String("file", "", "seed file (default: the embedded Initiative seed)")
	dryRun := fs.Bool("dry-run", false, "report what would be created without writing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	url, token := getenv("ENTITLEMENTS_API_URL"), getenv("ENTITLEMENTS_ADMIN_TOKEN")
	if url == "" || token == "" {
		return errors.New("ENTITLEMENTS_API_URL and ENTITLEMENTS_ADMIN_TOKEN are required")
	}

	raw := seed.Initiative
	if *file != "" {
		b, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		raw = b
	}
	doc, err := seed.Parse(raw)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report, err := (&seed.Client{BaseURL: url, Token: token}).Load(ctx, doc, *dryRun)
	for _, l := range report.Created {
		_, _ = fmt.Fprintln(out, "created ", l)
	}
	for _, l := range report.Existing {
		_, _ = fmt.Fprintln(out, "exists  ", l)
	}
	return err
}
