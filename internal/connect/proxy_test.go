package connect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyStripsTokenAndIgnoresGuestAuth(t *testing.T) {
	const token = "test-connection-value"
	var mu sync.Mutex
	var sawAuth, sawAPI, sawBody string
	var dials int
	ts, dial := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		dials++
		sawAuth = r.Header.Get("Authorization")
		sawAPI = r.Header.Get("X-GitHub-Api-Version")
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		sawBody = string(buf[:n])
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"octo","echo":"` + token + `","basic":"` + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)) + `"}`))
	})
	defer ts.Close()
	res, err := Do(context.Background(), token, Call{
		Connection: GitHub,
		Method:     "GET",
		URL:        "https://api.github.com/user",
		Headers:    map[string]string{"Authorization": "Bearer guest-marker", "Accept": "application/json"},
	}, dial)
	if err != nil || res.Outcome != OutcomeOK || res.Status != 200 {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Contains(string(res.Body), token) || strings.Contains(sawAuth, "guest-marker") || sawAuth != "Bearer "+token || sawAPI != "2022-11-28" {
		t.Fatal("auth or body")
	}
	if !strings.Contains(string(res.Body), "[redacted]") {
		t.Fatal("redaction missing")
	}
	mu.Lock()
	if dials != 1 || sawBody != "" {
		t.Fatalf("dials %d body %q", dials, sawBody)
	}
	mu.Unlock()

	gitRes, err := Do(context.Background(), token, Call{
		Connection: GitHub,
		Method:     "GET",
		URL:        "https://github.com/octo/repo.git/info/refs?service=git-upload-pack",
	}, dial)
	if err != nil || gitRes.Outcome != OutcomeOK {
		t.Fatalf("git %+v %v", gitRes, err)
	}
	mu.Lock()
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token))
	if sawAuth != wantBasic {
		t.Fatal("git auth")
	}
	mu.Unlock()

	doRes, err := Do(context.Background(), token, Call{
		Connection: DigitalOcean,
		Method:     "GET",
		URL:        "https://api.digitalocean.com/v2/account",
	}, dial)
	if err != nil || doRes.Outcome != OutcomeOK || strings.Contains(string(doRes.Body), token) {
		t.Fatal("digitalocean")
	}
	mu.Lock()
	if sawAuth != "Bearer "+token {
		t.Fatal("do auth")
	}
	mu.Unlock()
}

func TestProxyRefusesBeforeDial(t *testing.T) {
	const token = "test-connection-value"
	var dials int
	ts, dial := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		dials++
		w.WriteHeader(http.StatusOK)
	})
	defer ts.Close()
	cases := []Call{
		{Connection: GitHub, Method: "TRACE", URL: "https://api.github.com/user"},
		{Connection: GitHub, Method: "GET", URL: "http://api.github.com/user"},
		{Connection: GitHub, Method: "GET", URL: "https://1.2.3.4/user"},
		{Connection: GitHub, Method: "GET", URL: "https://user:pw@api.github.com/user"},
		{Connection: GitHub, Method: "GET", URL: "https://api.github.com:8443/user"},
		{Connection: GitHub, Method: "GET", URL: "https://evil.example/user"},
		{Connection: GitHub, Method: "POST", URL: "https://api.github.com/user?x=" + token},
		{Connection: GitHub, Method: "POST", URL: "https://api.github.com/user", Body: []byte(token)},
		{Connection: GitHub, Method: "POST", URL: "https://api.github.com/user", Body: bytesOf(MaxBody + 1)},
	}
	for _, call := range cases {
		dials = 0
		res, err := Do(context.Background(), token, call, dial)
		if err == nil || dials != 0 {
			t.Fatalf("%s %s dials %d err %v", call.Method, call.URL, dials, err)
		}
		if res.Outcome != OutcomeAllow && res.Outcome != OutcomeBad {
			t.Fatalf("outcome %s", res.Outcome)
		}
		if strings.Contains(string(res.Body), token) {
			t.Fatal("token in error body")
		}
	}
	res, err := Do(context.Background(), "", Call{Connection: GitHub, Method: "GET", URL: "https://api.github.com/user"}, dial)
	if err == nil || res.Outcome != OutcomeNoGrant || dials != 0 {
		t.Fatal("empty token")
	}
}

func TestProxyRedirectRechecksAllowlist(t *testing.T) {
	const token = "test-connection-value"
	var dials int
	ts, dial := proxyServer(t, func(w http.ResponseWriter, r *http.Request) {
		dials++
		http.Redirect(w, r, "https://evil.example/steal", http.StatusFound)
	})
	defer ts.Close()
	res, err := Do(context.Background(), token, Call{
		Connection: GitHub, Method: "GET", URL: "https://api.github.com/user",
	}, dial)
	if err == nil || res.Outcome != OutcomeRedirect || dials != 1 {
		t.Fatalf("%+v %v dials %d", res, err, dials)
	}
	if strings.Contains(string(res.Body), token) {
		t.Fatal("token leaked")
	}
}

func proxyServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *Dial) {
	t.Helper()
	cert, pool := testCert(t, "api.github.com", "github.com", "api.digitalocean.com")
	ts := httptest.NewUnstartedServer(h)
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	dial := &Dial{
		Roots: pool,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", ts.Listener.Addr().String())
		},
	}
	return ts, dial
}

func testCert(t *testing.T, hosts ...string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     hosts,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func bytesOf(n int) []byte {
	return make([]byte, n)
}
