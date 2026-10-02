package project

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
	"pcoder/internal/sshkeys"
)

// EnsureGitSSH re-injects the deploy key when the fingerprint marker
// disagrees (regen) or is absent, and skips the rewrite when it matches.
// No git identity is involved anywhere: authorship is per repo, asked at
// commit time.
func TestEnsureGitSSHSkipsOnMatch(t *testing.T) {
	s, d, st, _ := newService(t)
	s.sshKeys = sshkeys.New(st)
	kp, err := s.sshKeys.EnsureKeypair()
	if err != nil {
		t.Skipf("ssh-keygen unavailable: %v", err)
	}

	d.EXPECT().Exec(mock.Anything, "cid", []string{"cat", "/root/.ssh-configured-sha"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: kp.Fingerprint + "\n"}, nil).Once()

	if err := s.EnsureGitSSH(context.Background(), "cid"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	d.AssertNotCalled(t, "WriteFile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestEnsureGitSSHReinjectsOnMismatch(t *testing.T) {
	s, d, st, _ := newService(t)
	s.sshKeys = sshkeys.New(st)
	if _, err := s.sshKeys.EnsureKeypair(); err != nil {
		t.Skipf("ssh-keygen unavailable: %v", err)
	}

	d.EXPECT().Exec(mock.Anything, "cid", []string{"cat", "/root/.ssh-configured-sha"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "stale\n"}, nil).Once()
	// the re-inject itself: mkdir, config grep, marker write
	d.EXPECT().Exec(mock.Anything, "cid", mock.Anything, false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Maybe()
	d.EXPECT().WriteFile(mock.Anything, "cid", mock.Anything, mock.Anything).Return(nil).Maybe()

	if err := s.EnsureGitSSH(context.Background(), "cid"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}
