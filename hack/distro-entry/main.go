// Command distro-entry prints one distribution matrix entry as
// line-oriented key=value pairs, for hack/e2e.sh to read.
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/contemper-project/contemper/test/distros"
)

func main() {
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
