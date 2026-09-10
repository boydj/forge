// Command loadtest drives a forge node with concurrent Gemini or Titan
// requests (one request per TLS connection, as the protocol requires) or
// with concurrent `git clone --bare` runs, and reports throughput, latency
// percentiles and errors by status.
//
//	loadtest -url gemini://localhost:1965/~alice/proj/ -c 32 -d 10s
//	loadtest -url 'titan://localhost:1965/~alice/proj/issues/1/comment' \
//	    -cert c.crt -key c.key -body 'ping' -mime text/gemini -c 4 -d 10s
//	loadtest -clone ssh://git@localhost:2222/alice/proj.git -c 4 -n 16
//
// See docs/failure-testing.md for how the numbers are used.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type options struct {
	url, sni, cert, key, body, bodyFile, mime, clone string
	concurrency, count                               int
	duration, timeout                                time.Duration
	insecure, json                                   bool
}

// Summary is the report printed at the end (as JSON with -json).
type Summary struct {
	Mode        string         `json:"mode"`
	Target      string         `json:"target"`
	Concurrency int            `json:"concurrency"`
	DurationS   float64        `json:"duration_s"`
	Requests    int            `json:"requests"`
	RPS         float64        `json:"rps"`
	Latency     Latency        `json:"latency_ms"`
	Statuses    map[string]int `json:"statuses"`
	Errors      map[string]int `json:"errors"`
	Bytes       int64          `json:"bytes"`
}

// Latency holds percentiles in milliseconds.
type Latency struct {
	P50, P95, P99, Max, Mean float64
}

type recorder struct {
	mu       sync.Mutex
	lat      []time.Duration
	statuses map[string]int
	errors   map[string]int
	bytes    int64
}

func newRecorder() *recorder {
	return &recorder{statuses: map[string]int{}, errors: map[string]int{}}
}

func (r *recorder) add(d time.Duration, status string, n int64, errClass string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lat = append(r.lat, d)
	r.bytes += n
	if errClass != "" {
		r.errors[errClass]++
		return
	}
	r.statuses[status]++
}

func (r *recorder) summary(mode, target string, c int, elapsed time.Duration) Summary {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := Summary{Mode: mode, Target: target, Concurrency: c, DurationS: elapsed.Seconds(),
		Requests: len(r.lat), Statuses: r.statuses, Errors: r.errors, Bytes: r.bytes}
	if elapsed > 0 {
		s.RPS = float64(len(r.lat)) / elapsed.Seconds()
	}
	if len(r.lat) == 0 {
		return s
	}
	sorted := append([]time.Duration(nil), r.lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	pct := func(q float64) float64 {
		i := int(float64(len(sorted)-1) * q)
		return float64(sorted[i]) / float64(time.Millisecond)
	}
	var total time.Duration
	for _, d := range sorted {
		total += d
	}
	s.Latency = Latency{P50: pct(0.50), P95: pct(0.95), P99: pct(0.99),
		Max:  float64(sorted[len(sorted)-1]) / float64(time.Millisecond),
		Mean: float64(total) / float64(len(sorted)) / float64(time.Millisecond)}
	return s
}

func main() {
	var o options
	flag.StringVar(&o.url, "url", "", "gemini:// or titan:// URL to request")
	flag.StringVar(&o.sni, "sni", "", "TLS server name (default: URL host)")
	flag.StringVar(&o.cert, "cert", "", "client certificate PEM (Titan)")
	flag.StringVar(&o.key, "key", "", "client key PEM (Titan)")
	flag.StringVar(&o.body, "body", "", "Titan body")
	flag.StringVar(&o.bodyFile, "body-file", "", "Titan body from file")
	flag.StringVar(&o.mime, "mime", "text/gemini", "Titan mime type")
	flag.StringVar(&o.clone, "clone", "", "git clone URL: run `git clone --bare` loops instead of requests")
	flag.IntVar(&o.concurrency, "c", 32, "concurrent workers")
	flag.IntVar(&o.count, "n", 0, "stop after this many requests/clones (0: run for -d; clone mode default 16)")
	flag.DurationVar(&o.duration, "d", 10*time.Second, "run duration (request mode)")
	flag.DurationVar(&o.timeout, "timeout", 30*time.Second, "per request/clone timeout")
	flag.BoolVar(&o.insecure, "insecure", true, "skip TLS verification (TOFU services are self-signed)")
	flag.BoolVar(&o.json, "json", false, "print the summary as JSON")
	flag.Parse()

	var s Summary
	var err error
	switch {
	case o.clone != "":
		s, err = runClones(o)
	case o.url != "":
		s, err = runRequests(o)
	default:
		fmt.Fprintln(os.Stderr, "loadtest: -url or -clone is required")
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
	if o.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(s)
	} else {
		printText(s)
	}
	if len(s.Errors) > 0 {
		os.Exit(3)
	}
}

func printText(s Summary) {
	fmt.Printf("%s %s\nconcurrency %d, %.1fs, %d requests, %.1f req/s, %s\n", s.Mode, s.Target,
		s.Concurrency, s.DurationS, s.Requests, s.RPS, humanBytes(s.Bytes))
	fmt.Printf("latency ms: p50 %.1f  p95 %.1f  p99 %.1f  max %.1f  mean %.1f\n",
		s.Latency.P50, s.Latency.P95, s.Latency.P99, s.Latency.Max, s.Latency.Mean)
	keys := make([]string, 0, len(s.Statuses))
	for k := range s.Statuses {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("status %s: %d\n", k, s.Statuses[k])
	}
	keys = keys[:0]
	for k := range s.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("error %s: %d\n", k, s.Errors[k])
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// runRequests hammers one URL: every worker opens a TLS connection, sends
// one request (plus the Titan body), reads the header and drains the body.
func runRequests(o options) (Summary, error) {
	u, err := url.Parse(o.url)
	if err != nil {
		return Summary{}, err
	}
	if u.Scheme != "gemini" && u.Scheme != "titan" {
		return Summary{}, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "1965"
	}
	addr := net.JoinHostPort(host, port)
	sni := o.sni
	if sni == "" {
		sni = host
	}
	cfg := &tls.Config{InsecureSkipVerify: o.insecure, ServerName: sni, MinVersion: tls.VersionTLS12}
	if o.cert != "" || o.key != "" {
		c, err := tls.LoadX509KeyPair(o.cert, o.key)
		if err != nil {
			return Summary{}, fmt.Errorf("client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{c}
	}
	var body []byte
	line := o.url
	if u.Scheme == "titan" {
		body = []byte(o.body)
		if o.bodyFile != "" {
			if body, err = os.ReadFile(o.bodyFile); err != nil {
				return Summary{}, err
			}
		}
		if !strings.Contains(u.Path, ";size=") {
			line = fmt.Sprintf("%s;size=%d;mime=%s", o.url, len(body), o.mime)
		}
	}
	rec := newRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), o.duration)
	defer cancel()
	var remaining int64 = -1
	if o.count > 0 {
		remaining = int64(o.count)
		cancel()
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}
	var mu sync.Mutex
	take := func() bool {
		if ctx.Err() != nil {
			return false
		}
		if remaining < 0 {
			return true
		}
		mu.Lock()
		defer mu.Unlock()
		if remaining == 0 {
			return false
		}
		remaining--
		return true
	}
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < o.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for take() {
				t0 := time.Now()
				status, n, class := requestOnce(addr, cfg, line, body, o.timeout)
				rec.add(time.Since(t0), status, n, class)
			}
		}()
	}
	wg.Wait()
	return rec.summary(u.Scheme, o.url, o.concurrency, time.Since(start)), nil
}

// requestOnce performs one request and classifies failures.
func requestOnce(addr string, cfg *tls.Config, line string, body []byte, timeout time.Duration) (status string, n int64, errClass string) {
	d := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(d, "tcp", addr, cfg)
	if err != nil {
		return "", 0, classify("dial", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := io.WriteString(conn, line+"\r\n"); err != nil {
		return "", 0, classify("write", err)
	}
	if len(body) > 0 {
		if _, err := conn.Write(body); err != nil {
			return "", 0, classify("write-body", err)
		}
	}
	br := bufio.NewReader(conn)
	header, err := br.ReadString('\n')
	if err != nil {
		return "", 0, classify("read-header", err)
	}
	f := strings.Fields(header)
	if len(f) == 0 {
		return "", 0, "bad-header"
	}
	if _, err := strconv.Atoi(f[0]); err != nil {
		return "", 0, "bad-header"
	}
	n, err = io.Copy(io.Discard, br)
	if err != nil {
		return f[0], n, classify("read-body", err)
	}
	return f[0], n + int64(len(header)), ""
}

func classify(stage string, err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return stage + ":timeout"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return stage + ":eof"
	case strings.Contains(err.Error(), "connection refused"):
		return stage + ":refused"
	case strings.Contains(err.Error(), "reset by peer"):
		return stage + ":reset"
	}
	return stage + ":" + strings.TrimSpace(strings.SplitN(err.Error(), "\n", 2)[0])
}

// runClones runs `git clone --bare` of one URL o.count times with
// o.concurrency workers. GIT_SSH_COMMAND from the environment is honoured.
func runClones(o options) (Summary, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Summary{}, err
	}
	total := o.count
	if total <= 0 {
		total = 16
	}
	root, err := os.MkdirTemp("", "loadtest-clone-")
	if err != nil {
		return Summary{}, err
	}
	defer os.RemoveAll(root)
	rec := newRecorder()
	jobs := make(chan int)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < o.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				dst := filepath.Join(root, strconv.Itoa(j))
				ctx, cancel := context.WithTimeout(context.Background(), o.timeout)
				cmd := exec.CommandContext(ctx, "git", "clone", "--bare", "--quiet", "--", o.clone, dst)
				t0 := time.Now()
				out, err := cmd.CombinedOutput()
				cancel()
				el := time.Since(t0)
				var n int64
				if err == nil {
					n = dirSize(dst)
					rec.add(el, "ok", n, "")
				} else {
					msg := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
					if ctx.Err() != nil {
						msg = "timeout"
					}
					if len(msg) > 80 {
						msg = msg[:80]
					}
					rec.add(el, "", 0, "clone:"+msg)
				}
				_ = os.RemoveAll(dst)
			}
		}()
	}
	for j := 0; j < total; j++ {
		jobs <- j
	}
	close(jobs)
	wg.Wait()
	return rec.summary("clone", o.clone, o.concurrency, time.Since(start)), nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}
