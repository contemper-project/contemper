// Command distro-entry reads the distribution matrix.
//
//	distro-entry ID [REGISTRY-MIRROR]
//
// prints one entry as line-oriented key=value pairs, for hack/e2e.sh.
//
//	distro-entry matrix [--ids a,b] [--arch amd64|arm64|all]
//
// prints the selected entry/architecture pairs as a GitHub Actions matrix.
//
//	distro-entry result --id ID --arch ARCH --outcome pass|fail
//	    [--stage S] [--digest D] [--run-url URL]
//
// prints one leg's result as JSON, for hack/distro-report.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/contemper-project/contemper/test/distros"
)

func main() {
	if len(os.Args) > 1 {
		var err error
		switch os.Args[1] {
		case "matrix":
			err = matrix(os.Args[2:])
		case "result":
			err = result(os.Args[2:])
		default:
			entryMain()
			return
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "distro-entry:", err)
			os.Exit(1)
		}
		return
	}
	entryMain()
}

func matrix(args []string) error {
	fs := flag.NewFlagSet("matrix", flag.ContinueOnError)
	ids := fs.String("ids", "", "comma-separated entry IDs (default: all)")
	arch := fs.String("arch", "all", "amd64, arm64 or all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	entries, err := distros.Load()
	if err != nil {
		return err
	}
	legs, err := distros.Legs(entries, *ids, *arch)
	if err != nil {
		return err
	}
	out, err := distros.MatrixJSON(legs)
	if err != nil {
		return err
	}
	fmt.Println(out)
	return nil
}

func result(args []string) error {
	fs := flag.NewFlagSet("result", flag.ContinueOnError)
	var r distros.Result
	fs.StringVar(&r.ID, "id", "", "entry ID")
	fs.StringVar(&r.Arch, "arch", "", "architecture")
	fs.StringVar(&r.Outcome, "outcome", "", "pass or fail")
	fs.StringVar(&r.Stage, "stage", "", "stage a failing run stopped in")
	fs.StringVar(&r.BaseDigest, "digest", "", "resolved base image digest")
	fs.StringVar(&r.RunURL, "run-url", "", "URL of the workflow run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if r.ID == "" || r.Arch == "" || (r.Outcome != "pass" && r.Outcome != "fail") {
		return fmt.Errorf("result needs --id, --arch and --outcome pass|fail")
	}
	out, err := json.Marshal(r)
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func entryMain() {
	if len(os.Args) < 2 || len(os.Args) > 3 {
		fmt.Fprintln(os.Stderr, "usage: distro-entry ID [REGISTRY-MIRROR]")
		os.Exit(2)
	}
	entries, err := distros.Load()
	if err == nil {
		var e distros.Entry
		if e, err = distros.Find(entries, os.Args[1]); err == nil {
			base := e.Base
			if len(os.Args) == 3 {
				base = distros.MirrorRef(base, os.Args[2])
			}
			fmt.Printf("base=%s\ncontainerfile=%s\narch=%s\n", base, e.Containerfile, strings.Join(e.Arch, " "))
			keys := make([]string, 0, len(e.BuildArgs))
			for k := range e.BuildArgs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Printf("buildarg=%s=%s\n", k, e.BuildArgs[k])
			}
			return
		}
	}
	fmt.Fprintln(os.Stderr, "distro-entry:", err)
	os.Exit(1)
}
