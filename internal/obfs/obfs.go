// Package obfs provides runtime string deobfuscation for strings that AV
// heuristics key on as static signatures (process-spawn commands, protocol
// keywords). Strings are stored XOR-encoded and decoded at the point of use,
// so the plaintext never appears in the binary's .rdata/.text string table.
//
// This preserves the Go pclntab (the GC still reads self-consistent stack
// maps) — unlike wholesale gopclntab grafting, which breaks the runtime.
//
// NOTE: Do NOT use a third-party obfuscator like garble for this. Garble is
// itself signature-detected by AV vendors (ClamAV: Win.Tool.Garble-*). A
// minimal, in-house XOR decoder has no such signature.
package obfs

const xorKey = byte(0x5A)

// Str XOR-decodes a string that was encoded with Encode at build time.
// The argument is written as a Go escaped byte literal (e.g. "\x39\x37\x3e"),
// so the plaintext never appears as a contiguous string in the binary.
func Str(encoded string) string {
	b := []byte(encoded)
	for i := range b {
		b[i] ^= xorKey
	}
	return string(b)
}

// Encode XOR-encodes a plaintext string. Used at build time (in a small
// helper or offline) to produce the escaped literal for Str().
func Encode(plain string) string {
	b := []byte(plain)
	for i := range b {
		b[i] ^= xorKey
	}
	return string(b)
}
