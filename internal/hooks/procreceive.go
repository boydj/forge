package hooks

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// proc-receive (ADR 0012 §3). git-receive-pack hands commands under
// receive.procReceiveRefs to this hook instead of applying them itself, and
// speaks pkt-line on the hook's stdin/stdout (githooks(5) "proc-receive"):
//
//	S: version=1\0push-options atomic ...   S: flush
//	H: version=1\0push-options              H: flush
//	S: <old> <new> <ref> (one per command)  S: flush
//	S: <push-option> (if negotiated) ...    S: flush
//	H: ok <ref> [option refname <r>] [option old-oid <o>] [option new-oid <n>]
//	   or ng <ref> <reason>, per command    H: flush
//
// The hook forwards the commands and push options to the daemon as one
// Request{Hook:"proc-receive"} and translates Response.Results back.
// "atomic" is deliberately not acknowledged (open question 2): git then
// refuses --atomic pushes that contain a change command.

// Reason reported when the daemon cannot be reached.
const reasonUnavailable = "forge unavailable"

// pkt-line framing: a 4-digit hex length (including the prefix) followed by
// the payload; "0000" is a flush packet.
const (
	pktMaxLen     = 65520
	pktMaxPayload = pktMaxLen - 4
)

var errPktLine = errors.New("proc-receive: malformed pkt-line")

type pktReader struct {
	r *bufio.Reader
}

// read returns the next payload, or flush=true for a flush packet.
func (p *pktReader) read() (payload []byte, flush bool, err error) {
	var hdr [4]byte
	if _, err := io.ReadFull(p.r, hdr[:]); err != nil {
		if err == io.EOF {
			return nil, false, io.ErrUnexpectedEOF
		}
		return nil, false, err
	}
	var raw [2]byte
	if _, err := hex.Decode(raw[:], hdr[:]); err != nil {
		return nil, false, errPktLine
	}
	n := int(raw[0])<<8 | int(raw[1])
	switch {
	case n == 0:
		return nil, true, nil
	case n < 4 || n > pktMaxLen:
		// 1-3 are protocol-v2 special packets; not used by hooks.
		return nil, false, errPktLine
	}
	buf := make([]byte, n-4)
	if _, err := io.ReadFull(p.r, buf); err != nil {
		return nil, false, io.ErrUnexpectedEOF
	}
	return buf, false, nil
}

// readLine reads one payload with a trailing newline (if any) removed.
func (p *pktReader) readLine() (string, bool, error) {
	b, flush, err := p.read()
	if err != nil || flush {
		return "", flush, err
	}
	return strings.TrimSuffix(string(b), "\n"), false, nil
}

func writePkt(w io.Writer, s string) error {
	if len(s)+1 > pktMaxPayload {
		s = s[:pktMaxPayload-1]
	}
	_, err := fmt.Fprintf(w, "%04x%s\n", len(s)+5, s)
	return err
}

func writeFlush(w io.Writer) error {
	_, err := io.WriteString(w, "0000")
	return err
}

// oneLine makes s safe for a single pkt-line: no NUL or newline.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\x00", " ")
	if len(s) > 1024 {
		s = s[:1024]
	}
	return strings.TrimSpace(s)
}

// runProcReceive is Run for the proc-receive hook.
func runProcReceive(stdin io.Reader, stdout, stderr io.Writer) error {
	pr := &pktReader{r: bufio.NewReader(stdin)}
	out := bufio.NewWriter(stdout)

	// Version negotiation.
	line, flush, err := pr.readLine()
	if err != nil {
		return fmt.Errorf("proc-receive: reading version: %w", err)
	}
	if flush {
		return errors.New("proc-receive: missing version line")
	}
	version, features, _ := strings.Cut(line, "\x00")
	if version != "version=1" {
		return fmt.Errorf("proc-receive: unsupported %q", version)
	}
	usePushOptions := false
	for _, f := range strings.Fields(features) {
		if f == "push-options" {
			usePushOptions = true
		}
	}
	if err := skipToFlush(pr); err != nil {
		return fmt.Errorf("proc-receive: after version: %w", err)
	}
	reply := "version=1"
	if usePushOptions {
		reply += "\x00push-options"
	}
	if err := writePkt(out, reply); err != nil {
		return err
	}
	if err := writeFlush(out); err != nil {
		return err
	}
	if err := out.Flush(); err != nil {
		return err
	}

	// Commands.
	req := newRequest("proc-receive")
	for {
		line, flush, err := pr.readLine()
		if err != nil {
			return fmt.Errorf("proc-receive: reading commands: %w", err)
		}
		if flush {
			break
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return fmt.Errorf("proc-receive: bad command %q", line)
		}
		req.Updates = append(req.Updates, Update{Old: f[0], New: f[1], Ref: f[2]})
		if len(req.Updates) > 1000 {
			return errors.New("proc-receive: too many commands")
		}
	}
	if usePushOptions {
		for {
			line, flush, err := pr.readLine()
			if err != nil {
				return fmt.Errorf("proc-receive: reading push options: %w", err)
			}
			if flush {
				break
			}
			req.PushOptions = append(req.PushOptions, line)
			if len(req.PushOptions) > 100 {
				return errors.New("proc-receive: too many push options")
			}
		}
	}
	if len(req.Updates) == 0 {
		return errors.New("proc-receive: no commands")
	}

	// Ask the daemon. Failure to reach it rejects every command; git then
	// shows the reason next to each ref rather than a generic hook error.
	var resp *Response
	if sock := os.Getenv(EnvSocket); sock == "" {
		fmt.Fprintln(stderr, "forge: hook invoked outside the forge (no socket); refusing")
	} else if resp, err = call(sock, &req); err != nil {
		fmt.Fprintf(stderr, "forge: hook error: %v\n", err)
		resp = nil
	}
	results := map[string]Result{}
	if resp != nil {
		for _, m := range resp.Messages {
			fmt.Fprintln(stderr, m)
		}
		for _, r := range resp.Results {
			results[r.Ref] = r
		}
	}

	// Report.
	for _, u := range req.Updates {
		r, ok := results[u.Ref]
		switch {
		case resp == nil:
			err = writePkt(out, "ng "+u.Ref+" "+reasonUnavailable)
		case !ok:
			err = writePkt(out, "ng "+u.Ref+" no result from forge")
		case !r.OK:
			reason := oneLine(r.Reason)
			if reason == "" {
				reason = "rejected by forge"
			}
			err = writePkt(out, "ng "+u.Ref+" "+reason)
		default:
			err = writePkt(out, "ok "+u.Ref)
			if err == nil && r.RefName != "" {
				err = writePkt(out, "option refname "+oneLine(r.RefName))
			}
			if err == nil && r.OldOID != "" {
				err = writePkt(out, "option old-oid "+oneLine(r.OldOID))
			}
			if err == nil && r.NewOID != "" {
				err = writePkt(out, "option new-oid "+oneLine(r.NewOID))
			}
		}
		if err != nil {
			return err
		}
	}
	if err := writeFlush(out); err != nil {
		return err
	}
	return out.Flush()
}

// skipToFlush discards packets up to and including the next flush.
func skipToFlush(pr *pktReader) error {
	for i := 0; i < 1000; i++ {
		_, flush, err := pr.read()
		if err != nil {
			return err
		}
		if flush {
			return nil
		}
	}
	return errPktLine
}
