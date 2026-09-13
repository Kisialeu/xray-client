package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

const controlTokenPath = "/usr/local/etc/xray-cli/control.token"

func validateLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("control/status address must use a loopback IP and port")
	}
	return nil
}

func readControlToken(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("control token must be a regular file with mode 0600")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if decoded, err := hex.DecodeString(token); err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("control token must contain 32 random bytes in hexadecimal")
	}
	return token, nil
}

func createControlToken(path string) (string, error) {
	if token, err := readControlToken(path); err == nil {
		return token, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw[:])
	_, err = f.WriteString(token + "\n")
	closeErr := f.Close()
	if err != nil {
		return "", err
	}
	return token, closeErr
}

func controlHandler(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		next.ServeHTTP(w, r)
	})
}

type controlTransport struct{ tokenPath string }

func (t controlTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	token, err := readControlToken(t.tokenPath)
	if err != nil {
		return nil, err
	}
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return http.DefaultTransport.RoundTrip(r)
}
