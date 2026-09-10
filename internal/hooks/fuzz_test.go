package hooks

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
)

// Fuzz targets for the pkt-line reader and the proc-receive hook client
// (githooks(5)). git-receive-pack is the only legitimate peer on the hook's
// stdin, but the hook must not panic or emit a malformed pkt stream on
// arbitrary input. See docs/security-review.md.

func fuzzPkt(lines ...string) []byte {
	var b bytes.Buffer
	for _, l := range lines {
		if l == "" {
			_ = writeFlush(&b)
			continue
		}
		_ = writePkt(&b, l)
	}
	return b.Bytes()
}

func FuzzPktReader(f *testing.F) {
	for _, s := range [][]byte{
		[]byte("0000"), []byte("0008abcd"), []byte("0005\n"), []byte("0004"), []byte("0003"), []byte("0001"),
		[]byte("ffff"), []byte("zzzz"), []byte("00"), []byte("fff0" + string(make([]byte, 65516))),
		fuzzPkt("version=1\x00push-options", "", "a b refs/for/main", "", "topic=x", ""),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		pr := &pktReader{r: bufio.NewReader(bytes.NewReader(data))}
		for i := 0; i < 1<<16; i++ {
			payload, flush, err := pr.read()
			if err != nil {
				if !errors.Is(err, errPktLine) && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("unexpected error type: %v", err)
				}
				return
			}
			if flush && payload != nil {
				t.Fatal("flush with payload")
			}
			if len(payload) > pktMaxPayload {
				t.Fatalf("payload too long: %d", len(payload))
			}
		}
	})
}

func FuzzProcReceive(f *testing.F) {
	const oid = "0123456789012345678901234567890123456789"
	f.Add(fuzzPkt("version=1\x00push-options", "", oid+" "+oid+" refs/for/main", "", "topic=x", ""))
	f.Add(fuzzPkt("version=1", "", oid+" "+oid+" refs/changes/12", ""))
	f.Add(fuzzPkt("version=2", ""))
	f.Add(fuzzPkt("version=1", "", "bad command", ""))
	f.Add(fuzzPkt("version=1", "", ""))
	f.Add(fuzzPkt(""))
	f.Add([]byte("garbage"))
	// No daemon socket: every command must be answered "ng ... forge unavailable".
	f.Setenv(EnvSocket, "")
	f.Fuzz(func(t *testing.T, data []byte) {
		var out bytes.Buffer
		_ = runProcReceive(bytes.NewReader(data), &out, io.Discard)
		// Whatever was written must be a well-formed pkt-line stream.
		pr := &pktReader{r: bufio.NewReader(bytes.NewReader(out.Bytes()))}
		for {
			payload, flush, err := pr.read()
			if err != nil {
				if !errors.Is(err, io.ErrUnexpectedEOF) || pr.r.Buffered() != 0 {
					t.Fatalf("malformed output %q: %v", out.String(), err)
				}
				return
			}
			if flush {
				continue
			}
			if bytes.HasPrefix(payload, []byte("ok ")) {
				t.Fatalf("command accepted without a daemon: %q", payload)
			}
		}
	})
}
