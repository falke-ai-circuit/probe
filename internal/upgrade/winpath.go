package upgrade

import "strings"

// CleanWinPath normalises a Windows path for comparison and for building child
// paths. It accepts forward and back slashes, collapses repeated separators,
// resolves "." and "..", keeps UNC prefixes, and preserves the drive letter
// form ("C:\").
//
// The upgrade path manipulates Windows paths from a non-Windows host (the
// server), so path/filepath is not usable: its separator is the build host's.
func CleanWinPath(p string) string {
	if p == "" {
		return ""
	}
	s := strings.TrimSpace(p)
	s = strings.ReplaceAll(s, "/", `\`)

	unc := strings.HasPrefix(s, `\\`)
	if unc {
		s = s[2:]
	}

	parts := make([]string, 0, 8)
	for _, part := range strings.Split(s, `\`) {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) > 0 && parts[len(parts)-1] != ".." && !strings.HasSuffix(parts[len(parts)-1], ":") {
				parts = parts[:len(parts)-1]
				continue
			}
		}
		parts = append(parts, part)
	}

	out := strings.Join(parts, `\`)
	if unc {
		out = `\\` + out
	}
	if len(out) == 2 && out[1] == ':' {
		out += `\`
	}
	return out
}

// IsAbsWinPath reports whether p is an absolute Windows path: a drive-letter
// path ("C:\x") or a UNC path ("\\server\share").
func IsAbsWinPath(p string) bool {
	s := CleanWinPath(p)
	if strings.HasPrefix(s, `\\`) {
		return len(s) > 2
	}
	if len(s) >= 3 && s[1] == ':' && s[2] == '\\' {
		c := s[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return false
}

// DirWinPath returns the directory part of a cleaned Windows path.
func DirWinPath(p string) string {
	s := CleanWinPath(p)
	i := strings.LastIndex(s, `\`)
	if i < 0 {
		return ""
	}
	if i == 0 {
		return `\`
	}
	if i == 2 && s[1] == ':' {
		return s[:3]
	}
	return s[:i]
}

// BaseWinPath returns the final element of a cleaned Windows path.
func BaseWinPath(p string) string {
	s := CleanWinPath(p)
	i := strings.LastIndex(s, `\`)
	if i < 0 {
		return s
	}
	return s[i+1:]
}

// EqualWinPath compares two Windows paths case-insensitively, as the Windows
// filesystem does.
func EqualWinPath(a, b string) bool {
	return strings.EqualFold(CleanWinPath(a), CleanWinPath(b))
}

// JoinWinPath appends child to dir using a Windows separator.
func JoinWinPath(dir, child string) string {
	d := CleanWinPath(dir)
	if d == "" {
		return CleanWinPath(child)
	}
	if strings.HasSuffix(d, `\`) {
		return d + strings.TrimPrefix(child, `\`)
	}
	return d + `\` + strings.TrimPrefix(child, `\`)
}

// WithinWinPath reports whether child is dir or lies below it.
func WithinWinPath(dir, child string) bool {
	d := strings.ToLower(CleanWinPath(dir))
	c := strings.ToLower(CleanWinPath(child))
	if d == "" || c == "" {
		return false
	}
	if d == c {
		return true
	}
	if !strings.HasSuffix(d, `\`) {
		d += `\`
	}
	return strings.HasPrefix(c, d)
}
