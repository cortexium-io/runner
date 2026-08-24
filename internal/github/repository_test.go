package github

import "testing"

func TestRepositoryFromRemote(t *testing.T) {
	for _, test := range []struct {
		remote string
		want   string
	}{
		{remote: "https://github.com/Owner/Repo.git", want: "Owner/Repo"},
		{remote: "git@github.com:owner/repo.git", want: "owner/repo"},
		{remote: "ssh://git@github.com/owner/repo.git", want: "owner/repo"},
	} {
		t.Run(test.remote, func(t *testing.T) {
			got, err := RepositoryFromRemote(test.remote)
			if err != nil || got != test.want {
				t.Fatalf("RepositoryFromRemote(%q) = %q, %v; want %q", test.remote, got, err, test.want)
			}
		})
	}
	for _, remote := range []string{"", "/tmp/repo.git", "http://github.com/owner/repo.git", "https://token@github.com/owner/repo.git", "https://example.com/owner/repo.git", "https://github.com/owner/repo/extra.git"} {
		t.Run("reject_"+remote, func(t *testing.T) {
			if _, err := RepositoryFromRemote(remote); err == nil {
				t.Fatalf("RepositoryFromRemote(%q) unexpectedly succeeded", remote)
			}
		})
	}
}
