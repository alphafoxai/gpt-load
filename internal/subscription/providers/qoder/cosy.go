package qoder

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Qoder's client wraps each call in a COSY envelope. The account blob is
// AES-128-CBC under a fresh key, and that key is wrapped with the client's
// embedded RSA public key. The signature is MD5 over the payload, the wrapped
// key, the timestamp, the already-encoded body, and the path.

const cosyVersion = "1.1.49"

const rsaPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

var qoderRSAKey = mustRSAPublicKey(rsaPublicKeyPEM)

func mustRSAPublicKey(pemText string) *rsa.PublicKey {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		panic("qoder: decode embedded RSA public key")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		panic("qoder: parse RSA public key: " + err.Error())
	}
	key, ok := pub.(*rsa.PublicKey)
	if !ok {
		panic("qoder: embedded key is not RSA")
	}
	return key
}

type cosyUser struct {
	UID       string
	Name      string
	Email     string
	Token     string
	MachineID string
}

func aes128CBCEncrypt(key, iv, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(plaintext)%aes.BlockSize
	padded := make([]byte, len(plaintext)+pad)
	copy(padded, plaintext)
	for i := len(plaintext); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

func userBlob(user cosyUser) (info, key string, err error) {
	raw, err := json.Marshal(map[string]string{
		"uid": user.UID, "aid": "", "name": user.Name, "email": user.Email,
		"security_oauth_token": user.Token,
	})
	if err != nil {
		return "", "", err
	}
	secret := []byte(hexID()[:16])
	encrypted, err := aes128CBCEncrypt(secret, secret, raw)
	if err != nil {
		return "", "", err
	}
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, qoderRSAKey, secret)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(encrypted), base64.StdEncoding.EncodeToString(wrapped), nil
}

func urlPathname(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	path := parsed.Path
	if strings.HasPrefix(path, "/algo") {
		path = path[len("/algo"):]
	}
	return path
}

func cosySignature(payload, key string, timestamp int64, body, path string) string {
	sum := md5.Sum([]byte(payload + "\n" + key + "\n" + strconv.FormatInt(timestamp, 10) + "\n" + body + "\n" + path))
	return hex.EncodeToString(sum[:])
}

func machineOS() string {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "386":
		arch = "x86"
	case "arm64":
		arch = "aarch64"
	}
	osName := runtime.GOOS
	if osName == "windows" {
		osName = "win32"
	}
	return arch + "_" + osName
}

func cosyHeaders(rawURL string, user cosyUser, body string, timestamp int64) (map[string]string, error) {
	if user.MachineID == "" {
		return nil, fmt.Errorf("qoder: missing machine id")
	}
	info, key, err := userBlob(user)
	if err != nil {
		return nil, err
	}
	if timestamp == 0 {
		timestamp = time.Now().Unix()
	}
	payloadJSON, err := json.Marshal(map[string]any{
		"version": "v1", "requestId": hexID(), "info": info,
		"cosyVersion": cosyVersion, "ideVersion": "",
	})
	if err != nil {
		return nil, err
	}
	payload := base64.StdEncoding.EncodeToString(payloadJSON)
	signature := cosySignature(payload, key, timestamp, body, urlPathname(rawURL))
	return map[string]string{
		"Accept": "application/json", "Accept-Encoding": "identity", "Content-Type": "application/json",
		"Authorization":         "Bearer COSY." + payload + "." + signature,
		"Cosy-Business-Product": "app", "Cosy-Business-Type": "agent",
		"Cosy-ClientIp": user.MachineID, "Cosy-ClientType": "10", "Cosy-Data-Policy": "disagree",
		"Cosy-Date": strconv.FormatInt(timestamp, 10), "Cosy-Key": key,
		"Cosy-MachineId": user.MachineID, "Cosy-MachineToken": user.MachineID, "Cosy-MachineType": "5",
		"Cosy-MachineOS": machineOS(), "Cosy-Scene": "app", "Cosy-User": user.UID,
		"Cosy-Version": cosyVersion, "Login-Version": "v2",
	}, nil
}
