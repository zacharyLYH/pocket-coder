package project

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
)

// Provisioning writes the credential file, global git config, and a
// rotation marker; EnsureGitConfig skips on match and re-injects on
// mismatch.
func TestInjectGitConfig(t *testing.T) {
	s, d, _, _ := newService(t)
	s.SetGit(func() (string, string, string) { return "N", "n@e.com", "tok123" })
	ctx := context.Background()

	d.EXPECT().WriteFile(mock.Anything, "cid", "/root/.git-credentials",
		[]byte("https://x-access-token:tok123@github.com")).Return(nil).Once()
	d.EXPECT().Exec(mock.Anything, "cid",
		[]string{"sh", "-c", "git config --global credential.helper 'store --file /root/.git-credentials' && chmod 600 /root/.git-credentials"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Once()
	d.EXPECT().Exec(mock.Anything, "cid",
		[]string{"git", "config", "--global", "user.name", "N"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Once()
	d.EXPECT().Exec(mock.Anything, "cid",
		[]string{"git", "config", "--global", "user.email", "n@e.com"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil).Once()
	d.EXPECT().WriteFile(mock.Anything, "cid", "/root/.git-configured-sha",
		[]byte(gitSHA("N", "n@e.com", "tok123"))).Return(nil).Once()

	if err := s.injectGitConfig(ctx, "cid"); err != nil {
		t.Fatalf("inject: %v", err)
	}
}

func TestEnsureGitConfigSkipsOnMatch(t *testing.T) {
	s, d, _, _ := newService(t)
	s.SetGit(func() (string, string, string) { return "N", "n@e.com", "tok123" })

	d.EXPECT().Exec(mock.Anything, "cid", []string{"cat", "/root/.git-configured-sha"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: gitSHA("N", "n@e.com", "tok123") + "\n"}, nil).Once()

	if err := s.EnsureGitConfig(context.Background(), "cid"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	d.AssertNotCalled(t, "WriteFile", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestEnsureGitConfigReinjectsOnMismatch(t *testing.T) {
	s, d, _, _ := newService(t)
	s.SetGit(func() (string, string, string) { return "N", "n@e.com", "tok123" })
	ctx := context.Background()

	d.EXPECT().Exec(mock.Anything, "cid", []string{"cat", "/root/.git-configured-sha"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "stale\n"}, nil).Once()
	d.EXPECT().WriteFile(mock.Anything, "cid", "/root/.git-credentials", mock.Anything).Return(nil).Once()
	d.EXPECT().Exec(mock.Anything, "cid", mock.Anything, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	d.EXPECT().WriteFile(mock.Anything, "cid", "/root/.git-configured-sha", mock.Anything).Return(nil).Once()

	if err := s.EnsureGitConfig(ctx, "cid"); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}
