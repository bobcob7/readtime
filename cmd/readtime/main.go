// Command readtime estimates how long it takes different audiences to read, understand and grok text piped to it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/bobcob7/readtime/internal/estimate"
	"github.com/bobcob7/readtime/internal/jev"
	"github.com/bobcob7/readtime/internal/links"
	"github.com/bobcob7/readtime/internal/report"
)

const (
	maxInputBytes = 16 << 20
	linkTimeout   = 15 * time.Second
)

var (
	errMissingAPIKey = errors.New("TYPESAFE_API_KEY is not set")
	errNoInput       = errors.New("no input: pass a file or pipe text to stdin, e.g. `readtime essay.md`")
	errInputTooLarge = errors.New("input exceeds 16 MiB")
	errTooManyArgs   = errors.New("expected at most one file argument")
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "readtime: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("readtime", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit JSON instead of a table")
	model := fs.String("model", jev.DefaultModel, "Jev model to query")
	timeout := fs.Duration("timeout", 2*time.Minute, "overall time limit")
	verbose := fs.Bool("v", false, "show ranges, link breakdown and processing details")
	debug := fs.Bool("debug", false, "log API activity to stderr")
	baseDir := fs.String("base", ".", "directory relative links resolve against when reading stdin")
	external := fs.Bool("external", false, "also fetch http(s) links")
	maxLinks := fs.Int("max-links", -1, "maximum links to follow; 0 disables, negative follows all")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: readtime [flags] [file]\n       <text> | readtime [flags]\n\nEstimates read, understand and grok times per professional audience using TypeSafe Jev.\nLinked content is followed one level deep. Requires TYPESAFE_API_KEY.\n\nFlags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	apiKey := os.Getenv("TYPESAFE_API_KEY")
	if apiKey == "" {
		return errMissingAPIKey
	}
	text, base, err := readInput(fs.Args(), *baseDir)
	if err != nil {
		return err
	}
	level := slog.LevelWarn
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client := jev.NewClient(&http.Client{}, jev.DefaultBaseURL, apiKey, logger)
	fetcher := links.NewFetcher(&http.Client{Timeout: linkTimeout}, base, *external, logger)
	estimator := estimate.NewEstimator(client, fetcher, *model, *maxLinks, logger)
	rep, err := estimator.Estimate(ctx, text)
	if err != nil {
		return fmt.Errorf("estimating: %w", err)
	}
	switch {
	case *jsonOut:
		return report.JSON(os.Stdout, rep)
	case *verbose:
		return report.Detailed(os.Stdout, rep)
	default:
		return report.Summary(os.Stdout, rep)
	}
}

// readInput reads the file argument if given, else stdin, and returns the directory relative links resolve against.
func readInput(args []string, baseDir string) (string, string, error) {
	switch len(args) {
	case 0:
		text, err := readStdin(os.Stdin)
		return text, baseDir, err
	case 1:
		file, err := os.Open(args[0])
		if err != nil {
			return "", "", fmt.Errorf("opening input: %w", err)
		}
		defer file.Close()
		text, err := readAll(file)
		return text, filepath.Dir(args[0]), err
	default:
		return "", "", errTooManyArgs
	}
}

func readStdin(f *os.File) (string, error) {
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("inspecting stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return "", errNoInput
	}
	return readAll(f)
}

func readAll(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	if len(data) > maxInputBytes {
		return "", errInputTooLarge
	}
	return string(data), nil
}
