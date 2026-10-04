package qoder

import "strings"

// Body codec used by Qoder's desktop client. The plaintext is base64'd with a
// shuffled alphabet, then the first and last thirds of that text are swapped.
// Alphabet and grouping are the ones dumped from the client and checked
// against captured traffic by CLIProxyAPI (MIT); the same codec is what
// @magpie-community/opencode-qoder-auth sends.

const bodyAlphabet = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"

var bodyAlphabetIndex = func() [256]int8 {
	var index [256]int8
	for i := range index {
		index[i] = -1
	}
	for i, c := range bodyAlphabet {
		index[c] = int8(i)
	}
	return index
}()

func groupBytes(group string) []byte {
	var vals []int
	for _, c := range group {
		if c == '$' {
			continue
		}
		vals = append(vals, int(bodyAlphabetIndex[c]))
	}
	if len(vals) == 0 {
		return nil
	}
	packed := 0
	for _, v := range vals {
		packed = (packed << 6) | v
	}
	count := (6 * len(vals)) / 8
	out := make([]byte, count)
	for i := 0; i < count; i++ {
		shift := 6*len(vals) - 8*(i+1)
		out[i] = byte((packed >> shift) & 255)
	}
	return out
}

func segmentEncode(data []byte) string {
	var b strings.Builder
	acc, bits := 0, 0
	emit := func(v int) { b.WriteByte(bodyAlphabet[v]) }
	for _, value := range data {
		acc = (acc << 8) | int(value)
		bits += 8
		for bits >= 6 {
			bits -= 6
			emit((acc >> bits) & 0x3F)
		}
	}
	if bits > 0 {
		emit((acc << (6 - bits)) & 0x3F)
	}
	for b.Len()%4 != 0 {
		b.WriteByte('$')
	}
	return b.String()
}

func bodyEncode(data []byte) string { return segmentEncode(data) }

func bodyDecode(encoded string) []byte {
	var out []byte
	for i := 0; i+4 <= len(encoded); i += 4 {
		if part := groupBytes(encoded[i : i+4]); part != nil {
			out = append(out, part...)
		}
	}
	return out
}

func swapOuterThirds(encoded string) string {
	third := len(encoded) / 3
	if third == 0 {
		return encoded
	}
	return encoded[len(encoded)-third:] + encoded[third:len(encoded)-third] + encoded[:third]
}

func encodeRequestBody(plaintext []byte) string {
	return swapOuterThirds(bodyEncode(plaintext))
}

func decodeRequestBody(wire string) []byte {
	return bodyDecode(swapOuterThirds(wire))
}
