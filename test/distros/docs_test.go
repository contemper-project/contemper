package distros

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// docsPage is the page whose "Covered distributions" table lists the
// matrix.
const docsPage = "../../docs/guide/distributions.md"

// TestDocsTableMatchesMatrix keeps the table in the distributions page
// and matrix.json in step.
//
// Rather than generating the table, the test parses the page: every
// markdown table row whose second cell is a backticked matrix id counts
// as an entry row, and nothing else on the page is looked at, so the
// surrounding prose, the header row and the free-text columns can change
// without touching the test. The cells compared are the id (column 2),
// the comma-separated architectures (column 3) and since (column 4).
// Parsing was preferred over generation because contributors then edit
// the table by hand and see the same diff the test describes.
func TestDocsTableMatchesMatrix(t *testing.T) {
	entries, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(docsPage)
	if err != nil {
		t.Fatal(err)
	}

	type row struct {
		arch  []string
		since string
	}
	rows := map[string]row{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 4 {
			continue
		}
		id, ok := strings.CutPrefix(strings.TrimSpace(cells[1]), "`")
		id, ok2 := strings.CutSuffix(id, "`")
		if !ok || !ok2 {
			continue // header, separator or another table
		}
		if _, dup := rows[id]; dup {
			t.Errorf("table lists %q twice", id)
		}
		var arch []string
		for _, a := range strings.Split(cells[2], ",") {
			arch = append(arch, strings.TrimSpace(a))
		}
		rows[id] = row{arch: arch, since: strings.TrimSpace(cells[3])}
	}

	for _, e := range entries {
		r, ok := rows[e.ID]
		if !ok {
			t.Errorf("matrix entry %q is missing from the table in %s", e.ID, docsPage)
			continue
		}
		if !slices.Equal(r.arch, e.Arch) {
			t.Errorf("%s: table has architectures %v, matrix has %v", e.ID, r.arch, e.Arch)
		}
		if r.since != e.Since {
			t.Errorf("%s: table has since %q, matrix has %q", e.ID, r.since, e.Since)
		}
		delete(rows, e.ID)
	}
	for id := range rows {
		t.Errorf("table row %q has no matrix entry", id)
	}
}
