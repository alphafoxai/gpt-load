package qoder

import (
	"crypto/rand"
	"io"
)

// randReader is replaced in tests that need a fixed verifier.
var randReader io.Reader = rand.Reader
