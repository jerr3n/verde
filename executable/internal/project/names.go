// SPDX-License-Identifier: MPL-2.0

package project

import (
	"fmt"
	"strconv"
	"strings"
)

// Instance names come from the client and can hold anything, including "../../x", so
// they're never used as paths directly. encodeName percent-encodes whatever would be
// unsafe or would read back as something else, and decodeName reverses it when
// scanning. Names that don't need it stay readable: Util stays Util.
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

	// A leading dot hides the file, and could make a folder called .git.
	if strings.HasPrefix(s, ".") {
		s = "%2E" + s[1:]
	}
	// Windows drops trailing dots and spaces, and this also covers ".." and ".".
	if strings.HasSuffix(s, ".") {
		s = s[:len(s)-1] + "%2E"
	} else if strings.HasSuffix(s, " ") {
		s = s[:len(s)-1] + "%20"
	}
	// A ModuleScript called x.server would otherwise come back as a Script called x.
	for _, suffix := range []string{".server", ".client"} {
		if stem, found := strings.CutSuffix(s, suffix); found {
			s = stem + "%2E" + suffix[1:]
		}
	}
	// init.luau means "the script of this folder", and Windows reserves some names.
	if s == "init" || isReservedOnWindows(s) {
		s = fmt.Sprintf("%%%02X", s[0]) + s[1:]
	}
	return s
}

// isReservedOnWindows reports whether Windows refuses this as a file name. It looks
// only at the part before the first dot, because NUL.luau is reserved too.
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

// decodeName turns a file or folder name back into an instance name. Only what
// encodeName could have written is decoded, so a file like 50%off.luau keeps its name.
func decodeName(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) && isUpperHex(s[i+1]) && isUpperHex(s[i+2]) {
			value, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			// Encoding only touches ASCII, and anything above it isn't valid text alone.
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

func isUpperHex(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
}
