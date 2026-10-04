package qoder

import (
	"crypto/rand"
	"fmt"
	"strings"
)

// newUUID is a version-4 UUID. Qoder's device flow sends this form for the
// nonce and the machine id.
func newUUID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("qoder: random id: " + err.Error())
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:])
}

func hexID() string { return strings.ReplaceAll(newUUID(), "-", "") }
