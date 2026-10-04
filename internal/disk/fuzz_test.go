package disk

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/contemper-project/contemper/internal/rootfs"
)

// errUnbalanced is what ssParse reports for the "Unbalanced quotes in
// command line" case.
var errUnbalanced = errors.New("unbalanced quotes")

// ssParse splits one script line into arguments the way debugfs does:
// it is a port of ss_parse in e2fsprogs lib/ss/parse.c, which every
// debugfs command line goes through (ss_execute_line). Arguments are
// separated by spaces and tabs; a double quote starts a quoted section
// in which whitespace is literal and a doubled quote stands for one
// literal quote; a quoted section can continue a token. The line comes
// from fgets into a C string, so it ends at the first NUL.
func ssParse(line string) ([]string, error) {
	if i := strings.IndexByte(line, 0); i >= 0 {
		line = line[:i]
	}
	const (
		whitespace = iota
		token
		quoted
	)
	var args []string
	var cur []byte
	mode := whitespace
	for i := 0; ; {
		switch mode {
		case whitespace:
			if i >= len(line) {
				return args, nil
			}
			switch line[i] {
			case ' ', '\t':
				i++
			case '"':
				mode = quoted
				i++
				cur = cur[:0]
			default:
				mode = token
				cur = cur[:0]
			}
		case token:
			if i >= len(line) {
				args = append(args, string(cur))
				return args, nil
			}
			switch line[i] {
			case ' ', '\t':
				args = append(args, string(cur))
				mode = whitespace
				i++
			case '"':
				mode = quoted
				i++
			default:
				cur = append(cur, line[i])
				i++
			}
		case quoted:
			if i >= len(line) {
				return nil, errUnbalanced
			}
			if line[i] != '"' {
				cur = append(cur, line[i])
				i++
				continue
			}
			i++
			if i < len(line) && line[i] == '"' {
				cur = append(cur, '"')
				i++
			} else {
				mode = token
			}
		}
	}
}

func TestSSParse(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{``, nil},
		{`mkdir "/a b"`, []string{"mkdir", "/a b"}},
		{`write "f000001" "/x"`, []string{"write", "f000001", "/x"}},
		{`a  b	c`, []string{"a", "b", "c"}},
		{`x "a""b"`, []string{"x", `a"b`}},
		{`x ""`, []string{"x", ""}},
		{`x "a"b"c d"`, []string{"x", "abc d"}},
	} {
		got, err := ssParse(tc.line)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("ssParse(%q) = %q, %v; want %q", tc.line, got, err, tc.want)
		}
	}
	if _, err := ssParse(`x "abc`); !errors.Is(err, errUnbalanced) {
		t.Errorf("unbalanced quote: err = %v", err)
	}
}

// FuzzQuoteArg checks that quoteArg either refuses a string or returns
// one single-line argument that debugfs parses back to exactly the input.
func FuzzQuoteArg(f *testing.F) {
	for _, s := range []string{
		"", "/", "/etc/os-release", "/with space", "/tab\there", `/qu"ote`,
		"/new\nline", "/cr\rline", "/nul\x00byte", "-f", "'single'", `\`,
		"/ünï/cødé", "\xff\xfe", "#comment", "!shell", `"`, `""`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q, err := quoteArg(s)
		if err != nil {
			return
		}
		if strings.ContainsAny(q, "\n\r\x00") {
			t.Fatalf("quoteArg(%q) = %q has a line break or NUL", s, q)
		}
		args, err := ssParse("cmd " + q + " next")
		if err != nil {
			t.Fatalf("quoteArg(%q) = %q does not parse: %v", s, q, err)
		}
		if len(args) != 3 || args[0] != "cmd" || args[1] != s || args[2] != "next" {
			t.Fatalf("quoteArg(%q) = %q parses as %q", s, q, args)
		}
	})
}

var sifValue = regexp.MustCompile(`^@?-?[0-9]+$`)

var payloadName = regexp.MustCompile(`^[fx][0-9]{6}$`)

// FuzzBuildDebugfsScript builds the debugfs script for fuzzed entries
// and checks that no field of an entry can change the shape of the
// script: whenever checkScriptLines accepts it, every line parses under
// debugfs's own rules into a known command with the expected number of
// arguments, and every path-like argument is one of the strings that came
// from the entries (or a payload file name), never a truncation or a
// concatenation of them.
func FuzzBuildDebugfsScript(f *testing.F) {
	type seed struct {
		p1, link1 string
		typ1      byte
		p2, link2 string
		typ2      byte
		mode      int64
		xname     string
		xval      string
	}
	for _, s := range []seed{
		{"/etc/hosts", "", tar.TypeReg, "/etc/link", "hosts", tar.TypeSymlink, 0o644, "user.a", "v"},
		{"/dir", "", tar.TypeDir, "/dir/h", "/etc/hosts", tar.TypeLink, 0o755, "security.capability", "\x01\x00"},
		{"/dev/null", "", tar.TypeChar, "/dev/sda", "", tar.TypeBlock, 0o666, "", ""},
		{"/run/fifo", "", tar.TypeFifo, "/a b/c d", "e f", tar.TypeSymlink, 0o600, "user.", ""},
		{`/q"uote`, "", tar.TypeReg, "/n\nl", "x\ny", tar.TypeSymlink, 0, "-f/etc/passwd", "x"},
		{"/x", "", tar.TypeDir, "/x/y", "/x", tar.TypeLink, 0o7777, "user.n\"ame", "v"},
		{"/p", "", tar.TypeXHeader, "/q", "", tar.TypeGNUSparse, -1, "system.posix_acl_access", "z"},
	} {
		f.Add(s.p1, s.link1, s.typ1, s.p2, s.link2, s.typ2, s.mode, s.xname, s.xval)
	}
	payloadDir := f.TempDir()
	tarPath := filepath.Join(f.TempDir(), "rootfs.tar")
	if err := os.WriteFile(tarPath, make([]byte, 64), 0o600); err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, p1, link1 string, typ1 byte, p2, link2 string, typ2 byte, mode int64, xname, xval string) {
		if len(p1)+len(link1)+len(p2)+len(link2)+len(xname)+len(xval) > 2048 {
			t.Skip()
		}
		rfs := &rootfs.Rootfs{TarPath: tarPath, Index: map[string]*rootfs.Entry{}}
		allowed := map[string]bool{"/": true}
		allow := func(ss ...string) {
			for _, s := range ss {
				allowed[s] = true
			}
		}
		for i, e := range []struct {
			name, link string
			typ        byte
		}{{p1, link1, typ1}, {p2, link2, typ2}} {
			key := normalize(e.name)
			if key == "/" {
				continue
			}
			hdr := &tar.Header{
				Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: mode,
				Uid: int(mode), Gid: i, ModTime: time.Unix(mode, 0),
			}
			if e.typ == tar.TypeReg {
				hdr.Size = 8
			}
			if i == 0 && xname != "" {
				hdr.PAXRecords = map[string]string{xattrSchilyPrefix + xname: xval}
			}
			rfs.Index[key] = &rootfs.Entry{Path: key, Header: hdr}
			dir, base := splitPath(key)
			allow(key, e.link, normalize(e.link), dir, base, xname)
		}

		script, _, err := buildDebugfsScript(context.Background(), rfs, payloadDir, nil)
		if err != nil {
			return
		}
		if checkScriptLines(script) != nil {
			return
		}
		if script == "" {
			return
		}
		if !strings.HasSuffix(script, "\n") {
			t.Fatalf("script does not end in a newline: %q", script)
		}
		for _, line := range strings.Split(strings.TrimSuffix(script, "\n"), "\n") {
			args, err := ssParse(line)
			if err != nil {
				t.Fatalf("line %q does not parse: %v", line, err)
			}
			if len(args) == 0 {
				t.Fatalf("empty script line in %q", script)
			}
			wantArgs := map[string][]int{
				"mkdir": {2}, "write": {3}, "symlink": {3}, "expand_dir": {2},
				"ln": {3}, "cd": {2}, "mknod": {3, 5}, "sif": {4}, "ea_set": {5},
			}[args[0]]
			ok := false
			for _, n := range wantArgs {
				ok = ok || len(args) == n
			}
			if !ok {
				t.Fatalf("line %q parses as %q: unknown command or wrong argument count", line, args)
			}
			var data []string // arguments that carry image-supplied or generated names
			switch args[0] {
			case "write":
				if !payloadName.MatchString(args[1]) {
					t.Fatalf("line %q: payload file %q", line, args[1])
				}
				data = args[2:]
			case "ea_set":
				if args[1] != "-f" || !payloadName.MatchString(args[2]) {
					t.Fatalf("line %q: ea_set options %q %q", line, args[1], args[2])
				}
				data = args[3:]
			case "mknod":
				data = args[1:2]
			case "sif":
				data = args[1:2]
				if !sifValue.MatchString(args[3]) {
					t.Fatalf("line %q: value %q", line, args[3])
				}
			default:
				data = args[1:]
			}
			for _, a := range data {
				if !allowed[a] {
					t.Fatalf("line %q: argument %q is not one of the entries' own strings", line, a)
				}
			}
		}
	})
}

// FuzzFindErrorMarker checks that debugfs echoing the script it runs is
// never mistaken for a failure, whatever the script (image paths) says,
// while a real error line is still found in the same output.
func FuzzFindErrorMarker(f *testing.F) {
	f.Add("mkdir \"/already exists\"\nwrite \"f000001\" \"/File not found\"\n", "")
	f.Add("cd \"/\"\n", "extra line\r\n")
	f.Add("sif \"/Usage:\" mode 0644\n", "debugfs: sif \"/Usage:\" mode 0644 \n")
	f.Fuzz(func(t *testing.T, script, noise string) {
		// quoteArg refuses a CR, so a generated script never has one;
		// findErrorMarker strips CRs from debugfs's output only.
		if strings.Contains(script, "\r") {
			t.Skip()
		}
		var out strings.Builder
		for _, line := range strings.Split(script, "\n") {
			out.WriteString("debugfs: " + line + "\n")
		}
		if m := findErrorMarker(script, out.String()); m != "" {
			t.Fatalf("echoed script lines matched marker %q", m)
		}
		// A genuine error line is still reported, unless the noise
		// makes it part of an echoed line.
		if strings.ContainsAny(noise, "\n") {
			return
		}
		withReal := out.String() + noise + "\nmkdir: File not found by ext2_lookup while looking up x\n"
		if findErrorMarker(script, withReal) == "" {
			t.Fatalf("real error line not found")
		}
	})
}
