// SPDX-License-Identifier: MPL-2.0

package project

import (
	"fmt"
	"strconv"
	"strings"
)

// encodeName changes an instance name into a safe file name.
// An instance name comes from the client and can hold any text, for example "../../x".
// Thus Verde never uses an instance name as a path directly.
// encodeName percent-encodes each character that is unsafe or that changes the meaning of the name.
// decodeName reverses the encoding during a scan.
// A name that needs no encoding stays the same. For example, Util stays Util.
func encodeName(name string) string {
	var b strings.Builder
	for i := range len(name) {
		if c := name[i]; c < 0x20 || c == 0x7f || strings.IndexByte(`%/\:*?"<>|`, c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
		} else {
			b.WriteByte(c)
		}
	}
	s := b.String()

	// A dot at the start hides the file. It can also make a folder with the name .git.
	if strings.HasPrefix(s, ".") {
		s = "%2E" + s[1:]
	}
	// Windows removes dots and spaces at the end of a name. This step also changes ".." and ".".
	if strings.HasSuffix(s, ".") {
		s = s[:len(s)-1] + "%2E"
	} else if strings.HasSuffix(s, " ") {
		s = s[:len(s)-1] + "%20"
	}
	// Without this step, a ModuleScript with the name x.server comes back as a Script with the name x.
	for _, suffix := range []string{".server", ".client"} {
		if stem, found := strings.CutSuffix(s, suffix); found {
			s = stem + "%2E" + suffix[1:]
		}
	}
	// init.luau is the script of its folder, and Windows reserves some names. Encode the first letter of these names.
	if s == "init" || isReservedOnWindows(s) {
		s = fmt.Sprintf("%%%02X", s[0]) + s[1:]
	}
	return s
}

// isReservedOnWindows reports whether Windows refuses name as a file name.
// It checks only the part before the first dot, because Windows also reserves names such as NUL.luau.
func isReservedOnWindows(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// decodeName changes a file or folder name back into an instance name.
// It decodes only the text that encodeName can write. Thus a file such as 50%off.luau keeps its name.
func decodeName(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isUpperHex(s[i+1]) && isUpperHex(s[i+2]) {
			value, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			// encodeName changes only ASCII bytes. A byte above ASCII is not valid text alone.
			if err == nil && value < 0x80 {
				b.WriteByte(byte(value))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// isUpperHex reports whether c is a digit or an uppercase letter from A to F.
func isUpperHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
}
