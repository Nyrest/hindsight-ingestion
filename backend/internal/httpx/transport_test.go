package httpx

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/Nyrest/hindsight-ingestion/internal/proxy"
)

func TestEnvironmentProxy(t *testing.T) {
	if os.Getenv("PROXY_TEST_CHILD") == "1" {
		for _, tc := range []struct{ url, want string }{{"http://remote.test", "http://http-proxy.test:8080"}, {"https://remote.test", "http://https-proxy.test:8081"}, {"http://bypass.test", ""}, {"https://bypass.test", ""}} {
			req, _ := http.NewRequest("GET", tc.url, nil)
			got, err := Transport(proxy.Default).Proxy(req)
			if err != nil {
				t.Fatal(err)
			}
			actual := ""
			if got != nil {
				actual = got.String()
			}
			if actual != tc.want {
				t.Fatalf("%s: %s", tc.url, actual)
			}
		}
		if Transport(proxy.Config{Type: "none"}).Proxy != nil {
			t.Fatal("direct transport still uses environment")
		}
		return
	}
	// Go caches proxy environment settings. Test uppercase and lowercase in fresh processes.
	for _, lower := range []bool{false, true} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEnvironmentProxy$")
		for _, v := range os.Environ() {
			key := strings.ToUpper(strings.SplitN(v, "=", 2)[0])
			if key != "HTTP_PROXY" && key != "HTTPS_PROXY" && key != "NO_PROXY" && key != "REQUEST_METHOD" {
				cmd.Env = append(cmd.Env, v)
			}
		}
		vars := []string{"HTTP_PROXY=http://http-proxy.test:8080", "HTTPS_PROXY=http://https-proxy.test:8081", "NO_PROXY=bypass.test"}
		if lower {
			for i, v := range vars {
				p := strings.SplitN(v, "=", 2)
				vars[i] = strings.ToLower(p[0]) + "=" + p[1]
			}
		}
		cmd.Env = append(cmd.Env, append(vars, "PROXY_TEST_CHILD=1")...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child: %s %v", out, err)
		}
	}
}

func TestHTTPAndHTTPSProxyAuthentication(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "target") }))
	defer target.Close()
	for _, secure := range []bool{false, true} {
		auth := make(chan string, 4)
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth <- r.Header.Get("Proxy-Authorization")
			if r.Method != "CONNECT" {
				io.WriteString(w, "proxy")
				return
			}
			upstream, err := net.Dial("tcp", r.Host)
			if err != nil {
				w.WriteHeader(502)
				return
			}
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				upstream.Close()
				return
			}
			buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			buf.Flush()
			go func() { defer upstream.Close(); io.Copy(upstream, conn) }()
			defer conn.Close()
			io.Copy(conn, upstream)
		})
		p := httptest.NewUnstartedServer(handler)
		typ := "http"
		if secure {
			typ = "https"
			p.StartTLS()
		} else {
			p.Start()
		}
		config := proxy.Config{Type: typ, Address: strings.TrimPrefix(strings.TrimPrefix(p.URL, "https://"), "http://"), Username: "proxy-user", Password: "p@ss:/word"}
		tr := Transport(config)
		if tr != Transport(config) {
			t.Fatal("transport not reused")
		}
		tr = tr.Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		c := &http.Client{Transport: tr}
		for _, url := range []string{"http://target.invalid/resource", target.URL} {
			resp, err := c.Get(url)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(body) != "proxy" && string(body) != "target" {
				t.Fatal(string(body))
			}
			if got := <-auth; got != "Basic "+base64.StdEncoding.EncodeToString([]byte("proxy-user:p@ss:/word")) {
				t.Fatal("missing proxy auth")
			}
		}
		tr.CloseIdleConnections()
		p.Close()
	}
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") }))
	defer direct.Close()
	resp, err := (&http.Client{Transport: Transport(proxy.Config{Type: "none"})}).Get(direct.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "direct" {
		t.Fatal(string(body))
	}
}

func TestSOCKS5ProxyAuthentication(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "socks-target") }))
	defer target.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	outcome := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			outcome <- err
			return
		}
		defer conn.Close()
		read := func(n int) []byte { b := make([]byte, n); _, err = io.ReadFull(conn, b); return b }
		head := read(2)
		read(int(head[1]))
		conn.Write([]byte{5, 2})
		head = read(2)
		user := read(int(head[1]))
		head = read(1)
		pass := read(int(head[0]))
		if string(user) != "user" || string(pass) != "password" {
			outcome <- io.ErrUnexpectedEOF
			return
		}
		conn.Write([]byte{1, 0})
		head = read(4)
		var host string
		switch head[3] {
		case 1:
			host = net.IP(read(4)).String()
		case 3:
			head = read(1)
			host = string(read(int(head[0])))
		case 4:
			host = net.IP(read(16)).String()
		}
		port := binary.BigEndian.Uint16(read(2))
		upstream, dialErr := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
		if dialErr != nil {
			outcome <- dialErr
			return
		}
		defer upstream.Close()
		conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
		outcome <- err
		go io.Copy(upstream, conn)
		io.Copy(conn, upstream)
	}()
	tr := Transport(proxy.Config{Type: "socks5", Address: listener.Addr().String(), Username: "user", Password: "password"})
	resp, err := (&http.Client{Transport: tr}).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	tr.CloseIdleConnections()
	if string(body) != "socks-target" {
		t.Fatal(string(body))
	}
	if err := <-outcome; err != nil {
		t.Fatal(err)
	}
}
