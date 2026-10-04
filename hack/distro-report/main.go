// Command distro-report turns the results of a distribution test run into
// GitHub issues: one issue per matrix entry and architecture, opened when
// it fails, commented on when it keeps failing, and closed when it passes
// again.
//
//	distro-report --results DIR --run-url URL [--event NAME] [--dry-run]
//
// DIR holds the result JSON files the test legs wrote (see
// distros.Result). GITHUB_REPOSITORY, GITHUB_TOKEN and GITHUB_API_URL
// (default https://api.github.com) come from the environment.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/contemper-project/contemper/test/distros"
)

func main() {
	resultsDir := flag.String("results", "", "directory of result JSON files")
	runURL := flag.String("run-url", "", "URL of this workflow run")
	event := flag.String("event", "", "event that started the run, for the issue text")
	dryRun := flag.Bool("dry-run", false, "print the planned actions without writing")
	flag.Parse()
	if *resultsDir == "" || *runURL == "" {
		fmt.Fprintln(os.Stderr, "usage: distro-report --results DIR --run-url URL [--event NAME] [--dry-run]")
		os.Exit(2)
	}
	if err := run(*resultsDir, *runURL, *event, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "distro-report:", err)
		os.Exit(1)
	}
}

func run(dir, runURL, event string, dryRun bool) error {
	repo, token := os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_TOKEN")
	if repo == "" || token == "" {
		return fmt.Errorf("GITHUB_REPOSITORY and GITHUB_TOKEN must be set")
	}
	api := os.Getenv("GITHUB_API_URL")
	if api == "" {
		api = "https://api.github.com"
	}
	// Bad result files don't stop the valid ones from being reported.
	results, readErr := readResults(dir)
	entries, err := distros.Load()
	if err != nil {
		return err
	}
	r := &reporter{
		gh:      newClient(api, repo, token),
		entries: entries,
		runURL:  runURL,
		event:   event,
		dryRun:  dryRun,
		log:     os.Stdout,
	}
	return errors.Join(readErr, r.report(results))
}
