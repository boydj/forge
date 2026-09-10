package sshd

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Op is a Git transport operation.
type Op int

const (
	// OpRead is git-upload-pack (clone, fetch).
	OpRead Op = iota
	// OpWrite is git-receive-pack (push).
	OpWrite
)

// String returns the FORGE_OP spelling of the operation.
func (o Op) String() string {
	switch o {
	case OpRead:
		return "upload"
	case OpWrite:
		return "receive"
	}
	return "unknown"
}

// ErrBadCommand is returned by ParseCommand for anything that is not one
// of the accepted git transport commands.
var ErrBadCommand = errors.New("sshd: bad command")

// Command is a parsed, validated exec request.
type Command struct {
	Op    Op
	Owner string
	Repo  string
}

// Path returns "owner/repo".
func (c Command) Path() string { return c.Owner + "/" + c.Repo }

// maxCommandLen bounds the exec request payload we are willing to parse.
// The longest legal command is well under 150 bytes.
const maxCommandLen = 256

var (
	ownerRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	repoRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

// ParseCommand parses an SSH exec command line using a strict grammar:
//
//	("git-upload-pack" | "git-receive-pack" | "git upload-pack" | "git receive-pack") SP path
//	path := ["'"] ( "~" owner | ["/"] owner ) "/" repo [".git"] ["'"]
//
// Quotes must be balanced, exactly one space separates verb and path, and
// no other characters are permitted. The line is never shell-split.
// Anything else returns an error wrapping ErrBadCommand with a message that
// is safe to show to the client.
func ParseCommand(line string) (Command, error) {
	var cmd Command
	if line == "" {
		return cmd, fmt.Errorf("%w: empty command", ErrBadCommand)
	}
	if len(line) > maxCommandLen {
		return cmd, fmt.Errorf("%w: command too long", ErrBadCommand)
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c < 0x20 || c >= 0x7f {
			return cmd, fmt.Errorf("%w: control or non-ASCII byte in command", ErrBadCommand)
		}
	}
	var rest string
	switch {
	case strings.HasPrefix(line, "git-upload-pack "):
		cmd.Op, rest = OpRead, line[len("git-upload-pack "):]
	case strings.HasPrefix(line, "git upload-pack "):
		cmd.Op, rest = OpRead, line[len("git upload-pack "):]
	case strings.HasPrefix(line, "git-receive-pack "):
		cmd.Op, rest = OpWrite, line[len("git-receive-pack "):]
	case strings.HasPrefix(line, "git receive-pack "):
		cmd.Op, rest = OpWrite, line[len("git receive-pack "):]
	case strings.HasPrefix(line, "git-upload-archive") || strings.HasPrefix(line, "git upload-archive"):
		return cmd, fmt.Errorf("%w: git-upload-archive is not supported", ErrBadCommand)
	default:
		return cmd, fmt.Errorf("%w: only git-upload-pack and git-receive-pack are available", ErrBadCommand)
	}
	// Optional single quotes, as sent by git clients.
	if strings.HasPrefix(rest, "'") {
		if len(rest) < 3 || !strings.HasSuffix(rest, "'") {
			return cmd, fmt.Errorf("%w: unbalanced quotes in repository path", ErrBadCommand)
		}
		rest = rest[1 : len(rest)-1]
	}
	if rest == "" {
		return cmd, fmt.Errorf("%w: missing repository path", ErrBadCommand)
	}
	// Every remaining byte must be from the path alphabet; this rejects
	// spaces, quotes, shell metacharacters and option-looking arguments
	// before any structural parsing.
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '.', c == '_', c == '/', c == '~':
		default:
			return cmd, fmt.Errorf("%w: invalid character %q in repository path", ErrBadCommand, c)
		}
	}
	// "~owner/repo" (tilde directly before the owner) or "/owner/repo".
	if strings.HasPrefix(rest, "~") {
		rest = rest[1:]
		if strings.HasPrefix(rest, "/") {
			return cmd, fmt.Errorf("%w: repository path must be owner/repo", ErrBadCommand)
		}
	} else {
		rest = strings.TrimPrefix(rest, "/")
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return cmd, fmt.Errorf("%w: repository path must be owner/repo", ErrBadCommand)
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	if !ownerRe.MatchString(owner) {
		return cmd, fmt.Errorf("%w: invalid owner name", ErrBadCommand)
	}
	if !repoRe.MatchString(repo) || strings.HasSuffix(repo, ".git") || strings.Contains(repo, "..") {
		return cmd, fmt.Errorf("%w: invalid repository name", ErrBadCommand)
	}
	cmd.Owner, cmd.Repo = owner, repo
	return cmd, nil
}
