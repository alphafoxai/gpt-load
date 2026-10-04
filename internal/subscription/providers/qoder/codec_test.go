package qoder

import (
	"bytes"
	"encoding/hex"
	"runtime"
	"testing"
)

func TestCodecKnownAnswers(t *testing.T) {
	for _, tt := range []struct{ hex, encoded, wire string }{
		{"", "", ""},
		{"66", "D&$$", "$&$D"},
		{"666f", "DOq$", "$OqD"},
		{"666f6f", "DOWb", "bOWD"},
		{"666f6f626172", "DOWb#OgY", "gYWb#ODO"},
		{"00ff3f7f", "_vq!ef$$", "$$q!ef_v"},
		{"e4b8ade69687", "QG*tQ)PZ", "PZ*tQ)QG"},
		{"7b226d65737361676573223a5b5d7d", "mYKtDxj^#SJLNYByS..W", "ByS..Wj^#SJLNYmYKtDx"},
	} {
		t.Run(tt.hex, func(t *testing.T) {
			plain, _ := hex.DecodeString(tt.hex)
			if got := bodyEncode(plain); got != tt.encoded {
				t.Fatalf("encode %q, want %q", got, tt.encoded)
			}
			if got := bodyDecode(tt.encoded); !bytes.Equal(got, plain) {
				t.Fatalf("decode %x, want %x", got, plain)
			}
			if got := encodeRequestBody(plain); got != tt.wire {
				t.Fatalf("wire %q, want %q", got, tt.wire)
			}
			if got := decodeRequestBody(tt.wire); !bytes.Equal(got, plain) {
				t.Fatalf("wire decode %x, want %x", got, plain)
			}
		})
	}
}

func TestCosySignatureKnownAnswer(t *testing.T) {
	const path = "/api/v2/service/pro/sse/agent_chat_generation"
	if got := cosySignature("cGF5bG9hZA==", "c2VjcmV0", 1700000000, "ByS..Wj^#SJLNYmYKtDx", path); got != "da9dbed272847ea4664dd7ae8bf82575" {
		t.Fatalf("signature = %q", got)
	}
	if got := urlPathname(siteGlobal.API + chatPath); got != path {
		t.Fatalf("signed path %q", got)
	}
}

func TestCosyHeadersNameTheMachine(t *testing.T) {
	headers, err := cosyHeaders(siteGlobal.chatURL(), cosyUser{UID: "uid", Token: "jt", MachineID: "fixed-machine"}, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Cosy-MachineId", "Cosy-MachineToken", "Cosy-ClientIp"} {
		if headers[key] != "fixed-machine" {
			t.Fatalf("%s = %q", key, headers[key])
		}
	}
	if got := headers["Authorization"]; len(got) < len("Bearer COSY.") || got[:len("Bearer COSY.")] != "Bearer COSY." {
		t.Fatalf("authorization %q", got)
	}
	arch := map[string]string{"amd64": "x86_64", "386": "x86", "arm64": "aarch64"}[runtime.GOARCH]
	if arch == "" {
		arch = runtime.GOARCH
	}
	osName := runtime.GOOS
	if osName == "windows" {
		osName = "win32"
	}
	if headers["Cosy-MachineOS"] != arch+"_"+osName {
		t.Fatalf("OS %q", headers["Cosy-MachineOS"])
	}
}
