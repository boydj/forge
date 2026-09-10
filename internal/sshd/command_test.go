package sshd

import (
	"errors"
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	type want struct {
		op          Op
		owner, repo string
	}
	valid := []struct {
		in   string
		want want
	}{
		{"git-upload-pack 'alice/proj.git'", want{OpRead, "alice", "proj"}},
		{"git-upload-pack alice/proj.git", want{OpRead, "alice", "proj"}},
		{"git-upload-pack /alice/proj.git", want{OpRead, "alice", "proj"}},
		{"git-upload-pack '/alice/proj.git'", want{OpRead, "alice", "proj"}},
		{"git-upload-pack ~alice/proj.git", want{OpRead, "alice", "proj"}},
		{"git-upload-pack '~alice/proj'", want{OpRead, "alice", "proj"}},
		{"git-upload-pack alice/proj", want{OpRead, "alice", "proj"}},
		{"git-receive-pack 'alice/proj.git'", want{OpWrite, "alice", "proj"}},
		{"git upload-pack 'alice/proj.git'", want{OpRead, "alice", "proj"}},
		{"git receive-pack 'alice/proj.git'", want{OpWrite, "alice", "proj"}},
		{"git-upload-pack 'a/b'", want{OpRead, "a", "b"}},
		{"git-upload-pack 'a-b/c.d_e-f'", want{OpRead, "a-b", "c.d_e-f"}},
		{"git-upload-pack 'a0/0a.git'", want{OpRead, "a0", "0a"}},
		{"git-upload-pack '" + strings.Repeat("a", 32) + "/" + strings.Repeat("b", 64) + ".git'",
			want{OpRead, strings.Repeat("a", 32), strings.Repeat("b", 64)}},
	}
	for _, tc := range valid {
		got, err := ParseCommand(tc.in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", tc.in, err)
			continue
		}
		if got.Op != tc.want.op || got.Owner != tc.want.owner || got.Repo != tc.want.repo {
			t.Errorf("%q: got %+v want %+v", tc.in, got, tc.want)
		}
	}

	invalid := []string{
		"",
		" ",
		"git-upload-pack",
		"git-upload-pack ",
		"git-upload-pack ''",
		"git-upload-pack '",
		"git-upload-pack 'alice/proj",
		"git-upload-pack alice/proj'",
		"git-upload-pack \"alice/proj\"",
		"git-upload-pack  'alice/proj'", // two spaces
		"git-upload-pack 'alice/proj' ",
		" git-upload-pack 'alice/proj'",
		"git-upload-pack 'alice/proj' ; rm -rf /",
		"git-upload-pack 'alice/proj'; rm -rf /",
		"git-upload-pack 'alice/proj' && id",
		"git-upload-pack 'alice/proj' | cat",
		"git-upload-pack 'alice/proj' $(id)",
		"git-upload-pack 'alice/proj' `id`",
		"git-upload-pack 'alice/proj' 'bob/other'",
		"git-upload-pack --output=x",
		"git-upload-pack --output=x 'alice/proj'",
		"git-upload-pack 'alice/proj' --output=x",
		"git-upload-pack -alice/proj",
		"git-upload-pack '-alice/proj'",
		"git-upload-pack '--alice/proj'",
		"git-upload-pack 'alice/-proj'",
		"git-upload-pack 'alice/--proj'",
		"git-upload-pack '../../etc'",
		"git-upload-pack ../../etc",
		"git-upload-pack '../alice/proj'",
		"git-upload-pack 'alice/../proj'",
		"git-upload-pack 'alice/proj/../x'",
		"git-upload-pack 'alice/proj/'",
		"git-upload-pack '/alice/proj/'",
		"git-upload-pack 'alice//proj'",
		"git-upload-pack '//alice/proj'",
		"git-upload-pack 'alice'",
		"git-upload-pack '/alice'",
		"git-upload-pack 'alice/proj/extra'",
		"git-upload-pack 'alice/.git'",
		"git-upload-pack 'alice/.'",
		"git-upload-pack 'alice/..'",
		"git-upload-pack 'alice/..git'",
		"git-upload-pack 'alice/proj.git.git'",
		"git-upload-pack 'Alice/proj'",
		"git-upload-pack 'alice/Proj'",
		"git-upload-pack '0alice/proj'",
		"git-upload-pack '-alice/proj'",
		"git-upload-pack 'alice/.proj'",
		"git-upload-pack 'alice/_proj'",
		"git-upload-pack 'alice_x/proj'",
		"git-upload-pack 'alice.x/proj'",
		"git-upload-pack 'ali ce/proj'",
		"git-upload-pack 'alice/pro j'",
		"git-upload-pack 'alice/proj\x00'",
		"git-upload-pack 'alice/proj\n'",
		"git-upload-pack 'alice/proj'\n",
		"git-upload-pack 'alice/pröj'",
		"git-upload-pack 'ålice/proj'",
		"git-upload-pack '∕alice/proj'",
		"git-upload-pack '~/alice/proj'",
		"git-upload-pack '/~alice/proj'",
		"git-upload-pack '~~alice/proj'",
		"git-upload-pack 'alice/~proj'",
		"git-upload-pack '" + strings.Repeat("a", 33) + "/proj'",
		"git-upload-pack 'alice/" + strings.Repeat("b", 65) + "'",
		"git-upload-pack '" + strings.Repeat("a", 300) + "'",
		"git-upload-archive 'alice/proj'",
		"git upload-archive 'alice/proj'",
		"git-upload-pack\t'alice/proj'",
		"GIT-UPLOAD-PACK 'alice/proj'",
		"git-receive-pack'alice/proj'",
		"git  upload-pack 'alice/proj'",
		"git\tupload-pack 'alice/proj'",
		"git 'alice/proj'",
		"git",
		"true",
		"/bin/sh",
		"bash -c 'git-upload-pack alice/proj'",
		"gitk-upload-pack 'alice/proj'",
		"xgit-upload-pack 'alice/proj'",
		"git-upload-pack-x 'alice/proj'",
		"env X=1 git-upload-pack 'alice/proj'",
		"GIT_PROTOCOL=version=2 git-upload-pack 'alice/proj'",
	}
	for _, in := range invalid {
		got, err := ParseCommand(in)
		if err == nil {
			t.Errorf("%q: accepted as %+v", in, got)
			continue
		}
		if !errors.Is(err, ErrBadCommand) {
			t.Errorf("%q: error %v does not wrap ErrBadCommand", in, err)
		}
		if got.Owner != "" || got.Repo != "" {
			t.Errorf("%q: partial result on error: %+v", in, got)
		}
	}
}

func TestOpString(t *testing.T) {
	if OpRead.String() != "upload" || OpWrite.String() != "receive" || Op(9).String() != "unknown" {
		t.Errorf("Op.String: %s %s %s", OpRead, OpWrite, Op(9))
	}
}
