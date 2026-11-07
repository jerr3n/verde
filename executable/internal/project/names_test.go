package project

import (
	"strings"
	"testing"
	"testing/quick"
)

func TestEncodeNameLeavesNormalNamesAlone(t *testing.T) {
	for _, name := range []string{"Util", "Main Menu", "a.b", "日本語", "Init", "x-y_z", "CONSOLE", "server"} {
		if got := encodeName(name); got != name {
			t.Errorf("encodeName(%q) = %q", name, got)
		}
	}
}

func TestEncodeNameExamples(t *testing.T) {
	tests := map[string]string{
		"../../x":  "%2E.%2F..%2Fx",
		"..":       "%2E%2E",
		".":        "%2E",
		".git":     "%2Egit",
		"a/b":      "a%2Fb",
		"100%":     "100%25",
		"init":     "%69nit",
		"x.server": "x%2Eserver",
		"CON":      "%43ON",
		"nul.txt":  "%6Eul.txt",
		"end.":     "end%2E",
		"end ":     "end%20",
	}
	for name, want := range tests {
		if got := encodeName(name); got != want {
			t.Errorf("encodeName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestDecodeNameKeepsWhatEncodingCouldntWrite(t *testing.T) {
	for _, name := range []string{"50%off", "100%", "%", "%2", "%zz", "%2f", "%FF"} {
		if got := decodeName(name); got != name {
			t.Errorf("decodeName(%q) = %q", name, got)
		}
	}
}

// The property every scan depends on: an encoded name is one path segment, comes back
// unchanged, and isn't mistaken for a different class or an init file.
func TestEncodeNameRoundTrips(t *testing.T) {
	check := func(name string) bool {
		if name == "" {
			return true
		}
		encoded := encodeName(name)
		if decodeName(encoded) != name || strings.ContainsAny(encoded, `/\`) ||
			encoded == "." || encoded == ".." {
			t.Logf("%q encoded as %q", name, encoded)
			return false
		}
		for _, kind := range scriptKinds {
			file := encoded + kind.suffix
			got, class, ok := classify(file)
			if !ok || class != kind.class || decodeName(got) != name || isInit(file) {
				t.Logf("%q as %s read back as %q, %s, %v", name, file, got, class, ok)
				return false
			}
		}
		return true
	}
	if err := quick.Check(check, &quick.Config{MaxCount: 20000}); err != nil {
		t.Error(err)
	}
	for _, name := range []string{
		"..", ".", "init", "x.server", "x.client", "x.server.luau", ".server", ".client",
		"%", "%%", "%25", "%2E", "a. ", " .", ".. ", "CON.server", "init.server",
	} {
		if !check(name) {
			t.Errorf("%q doesn't round-trip", name)
		}
	}
}
