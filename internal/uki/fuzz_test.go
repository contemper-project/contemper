package uki

import (
	"bytes"
	"compress/gzip"
	"debug/pe"
	"strings"
	"testing"
)

// FuzzParsePE checks that the PE header parser never panics on arbitrary
// bytes and that what it accepts has every header table inside the data.
func FuzzParsePE(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("MZ"))
	f.Add(minimalPE())
	f.Add(stubAMD64[:4096])
	f.Add(stubARM64[:4096])
	f.Fuzz(func(t *testing.T, data []byte) {
		p, err := parsePE(data)
		if err != nil {
			return
		}
		if p.sectionsOff+p.numSections*sectionHeaderSize > len(data) || p.optOff+p.optSize > len(data) {
			t.Fatalf("parsePE accepted tables that overrun the %d bytes", len(data))
		}
		for i := 0; i < p.numSections; i++ {
			_ = p.section(i)
		}
	})
}

// FuzzAppendSections appends fuzzed sections to fuzzed stubs. Whenever
// it succeeds, debug/pe has to parse the result, keep the stub's own
// sections and find the appended ones, with their content, in order.
func FuzzAppendSections(f *testing.F) {
	f.Add(minimalPE(), ".cmdline", []byte("root=LABEL=x rw"), ".linux", []byte{0xCD, 1, 2})
	f.Add(minimalPE(), ".a", []byte{}, ".b", bytes.Repeat([]byte{7}, 700))
	f.Add(minimalPE(), "", []byte("x"), "12345678", []byte("y"))
	f.Add(minimalPE(), "toolongname", []byte("x"), ".b", []byte("y"))
	f.Add(stubAMD64, ".osrel", []byte("NAME=x\n"), ".cmdline", []byte("rw"))
	f.Add(stubARM64, ".initrd", bytes.Repeat([]byte{0xAB}, 4096+37), ".linux", []byte("k"))
	f.Fuzz(func(t *testing.T, stub []byte, name1 string, data1 []byte, name2 string, data2 []byte) {
		if len(stub) > 1<<20 || len(data1)+len(data2) > 1<<20 {
			t.Skip()
		}
		secs := []namedSection{{name1, data1}, {name2, data2}}
		out, err := appendSections(stub, secs)
		if err != nil {
			return
		}
		// appendSections only reads the few header fields it edits, so
		// it also accepts stubs that debug/pe refuses (an unknown
		// machine type, say). It is only ever given the embedded stubs,
		// so the output is held to debug/pe's standard for inputs that
		// meet it too.
		in, err := pe.NewFile(bytes.NewReader(stub))
		if err != nil {
			return
		}
		f, err := pe.NewFile(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("debug/pe cannot parse the result: %v", err)
		}
		if len(f.Sections) != len(in.Sections)+len(secs) {
			t.Fatalf("%d sections, want %d + %d", len(f.Sections), len(in.Sections), len(secs))
		}
		for i, s := range in.Sections {
			if f.Sections[i].Name != s.Name || f.Sections[i].VirtualAddress != s.VirtualAddress || f.Sections[i].Offset != s.Offset {
				t.Fatalf("stub section %d (%s) changed", i, s.Name)
			}
		}
		oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
		if !ok {
			t.Fatalf("optional header is %T, want PE32+", f.OptionalHeader)
		}
		for i, want := range secs {
			sec := f.Sections[len(in.Sections)+i]
			// debug/pe cuts a name at its first NUL.
			wantName, _, _ := strings.Cut(want.Name, "\x00")
			if sec.Name != wantName {
				t.Fatalf("section %d is named %q, want %q", i, sec.Name, wantName)
			}
			if int(sec.VirtualSize) != len(want.Data) {
				t.Fatalf("section %q virtual size %d, want %d", want.Name, sec.VirtualSize, len(want.Data))
			}
			got, err := sec.Data()
			if err != nil {
				t.Fatalf("reading section %q: %v", want.Name, err)
			}
			if len(got) < len(want.Data) || !bytes.Equal(got[:len(want.Data)], want.Data) {
				t.Fatalf("section %q content differs", want.Name)
			}
			if oh.FileAlignment != 0 && sec.Offset%oh.FileAlignment != 0 {
				t.Fatalf("section %q offset %#x not file-aligned (%#x)", want.Name, sec.Offset, oh.FileAlignment)
			}
			if end := sec.VirtualAddress + sec.VirtualSize; oh.SizeOfImage < end {
				t.Fatalf("SizeOfImage %#x is below section %q end %#x", oh.SizeOfImage, want.Name, end)
			}
		}
		if oh.CheckSum != 0 {
			t.Fatalf("checksum is %#x, want 0", oh.CheckSum)
		}
	})
}

// FuzzPrepareKernel checks that PrepareKernel passes a PE image through
// untouched, bounds what it decompresses, and warns exactly when it
// returns the input unrecognized.
func FuzzPrepareKernel(f *testing.F) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("raw kernel image bytes"))
	_ = zw.Close()
	f.Add([]byte{})
	f.Add([]byte("MZ"))
	f.Add(minimalPE())
	f.Add(gz.Bytes())
	f.Add(gz.Bytes()[:len(gz.Bytes())-3])
	f.Add([]byte{0x1f, 0x8b})
	f.Add([]byte("neither MZ nor gzip"))
	f.Fuzz(func(t *testing.T, data []byte) {
		// A 16 KiB gzip stream expands to at most a few tens of MiB,
		// keeping an iteration fast; the bound itself has its own test.
		if len(data) > 16<<10 {
			t.Skip()
		}
		out, warning, err := PrepareKernel(data)
		switch {
		case len(data) >= 2 && data[0] == 'M' && data[1] == 'Z':
			if err != nil || warning != "" || !bytes.Equal(out, data) {
				t.Fatalf("MZ image not passed through: warning %q, err %v", warning, err)
			}
		case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
			if err != nil {
				return
			}
			if warning != "" || len(out) > maxKernelSize {
				t.Fatalf("gzip kernel: warning %q, %d bytes", warning, len(out))
			}
		default:
			if err != nil || warning == "" || !bytes.Equal(out, data) {
				t.Fatalf("unrecognized kernel: warning %q, err %v", warning, err)
			}
		}
	})
}
