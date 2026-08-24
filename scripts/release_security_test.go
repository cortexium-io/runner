package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestReleaseWorkflowContract(t *testing.T) {
	workflowBytes, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(workflowBytes)
	helperBytes, err := os.ReadFile("verify-release-source.sh")
	if err != nil {
		t.Fatal(err)
	}
	helper := string(helperBytes)

	requireContains(t, workflow,
		"workflow_dispatch:",
		"version:",
		"required: true",
		"permissions:\n  contents: read",
		"test \"$DISPATCH_REF\" = refs/heads/main",
		"sh scripts/verify-release-source.sh \"$VERSION\" \"$REVIEWED_COMMIT\"",
		"REPOSITORY_READ_TOKEN: ${{ github.token }}",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=\"AUTHORIZATION: basic $git_auth\"",
		"unset REPOSITORY_READ_TOKEN",
		"persist-credentials: false",
		"artifact_id: ${{ steps.upload.outputs.artifact-id }}",
		"artifact_digest: ${{ steps.upload.outputs.artifact-digest }}",
		"artifact-ids: ${{ needs.build.outputs.artifact_id }}",
		"digest-mismatch: error",
		"environment: release",
		"gh api \"/repos/$GITHUB_REPOSITORY/actions/artifacts/$ARTIFACT_ID\" --jq .digest",
		"gh api \"/repos/$GITHUB_REPOSITORY/git/ref/tags/$TAG\" --jq '[.object.sha, .object.type] | join(\" \")'",
		"test \"$tag_object\" = \"$VERIFIED_COMMIT commit\"",
		"GH_TOKEN: ${{ github.token }}",
		"gh release create \"$TAG\"",
	)
	requireContains(t, helper,
		"grep -Eq '^v(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)$'",
		"git ls-remote --refs",
		"git cat-file -t",
		"does not identify the exact reviewed dispatch commit",
		"moved after it was fetched",
	)
	requireNotContains(t, helper, "git merge-base --is-ancestor")
	requireNotContains(t, workflow,
		"push:\n    tags:",
		"secrets.RELEASE_TOKEN",
		"if: github.ref == 'refs/heads/main'",
	)

	if count := strings.Count(workflow, "uses: actions/checkout@"); count != 2 {
		t.Fatalf("release build must contain exactly two reviewed-source checkouts, got %d", count)
	}
	if count := strings.Count(workflow, "persist-credentials: false"); count != 2 {
		t.Fatalf("every checkout must disable credential persistence, got %d declarations", count)
	}
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "uses:") && !regexp.MustCompile(`@[0-9a-f]{40}(?:\s|$)`).MatchString(trimmed) {
			t.Errorf("action is not pinned to a full commit SHA: %s", trimmed)
		}
	}

	build := jobSection(t, workflow, "build")
	requireContains(t, build, "contents: read")
	requireNotContains(t, build, "contents: write")
	if count := strings.Count(build, "github.token"); count != 1 {
		t.Fatalf("release build must expose the read-only repository token only to source verification; got %d uses", count)
	}

	publish := jobSection(t, workflow, "publish")
	requireContains(t, publish,
		"needs: build",
		"environment: release",
		"actions: read",
		"contents: write",
		"artifact-ids: ${{ needs.build.outputs.artifact_id }}",
		"digest-mismatch: error",
	)
	requireNotContains(t, publish,
		"actions/checkout@",
		"actions/setup-go@",
		"go test",
		"go build",
		"npm ",
		"scripts/",
	)
	if count := strings.Count(workflow, "github.token"); count != 3 {
		t.Fatalf("ephemeral credential must appear once for private source verification and twice in publication; got %d", count)
	}
	if count := strings.Count(workflow, "contents: write"); count != 1 {
		t.Fatalf("write permission must appear exactly once, on the isolated publish job; got %d", count)
	}
	finalStep := workflow[strings.Index(workflow, "- name: Publish verified GitHub release"):]
	requireContains(t, finalStep, "GH_TOKEN: ${{ github.token }}")
	requireNotContains(t, finalStep, "git ls-remote")
}

func TestReleaseSourceGate(t *testing.T) {
	t.Run("accepts reviewed main lightweight tag", func(t *testing.T) {
		repository := newReleaseGateRepository(t)
		reviewedCommit := repository.commit("reviewed release")
		repository.lightweightTag("v1.2.3")
		repository.push()
		repository.verifySuccess("v1.2.3", reviewedCommit, "")
	})

	tests := []struct {
		name    string
		version string
		prepare func(*releaseGateRepository) (reviewedCommit, expectedCommit string)
	}{
		{
			name:    "malformed tag",
			version: "v1.2",
			prepare: func(r *releaseGateRepository) (string, string) {
				return r.commit("reviewed"), ""
			},
		},
		{
			name:    "missing tag",
			version: "v1.2.3",
			prepare: func(r *releaseGateRepository) (string, string) {
				return r.commit("reviewed"), ""
			},
		},
		{
			name:    "annotated tag",
			version: "v1.2.3",
			prepare: func(r *releaseGateRepository) (string, string) {
				commit := r.commit("reviewed")
				r.annotatedTag("v1.2.3")
				r.push()
				return commit, ""
			},
		},
		{
			name:    "historical main ancestor tag",
			version: "v1.2.3",
			prepare: func(r *releaseGateRepository) (string, string) {
				r.commit("historical release")
				r.lightweightTag("v1.2.3")
				reviewedCommit := r.commit("reviewed follow-up")
				r.push()
				return reviewedCommit, ""
			},
		},
		{
			name:    "off-main tag",
			version: "v1.2.3",
			prepare: func(r *releaseGateRepository) (string, string) {
				reviewedCommit := r.commit("reviewed")
				r.run("checkout", "--quiet", "--orphan", "unreviewed")
				r.run("rm", "--quiet", "--cached", "release.txt")
				r.commit("unreviewed release")
				r.lightweightTag("v1.2.3")
				r.run("checkout", "--quiet", "main")
				r.pushAllRefs()
				return reviewedCommit, ""
			},
		},
		{
			name:    "moved tag",
			version: "v1.2.3",
			prepare: func(r *releaseGateRepository) (string, string) {
				expectedCommit := r.commit("reviewed release")
				r.lightweightTag("v1.2.3")
				reviewedCommit := r.commit("reviewed follow-up")
				r.run("tag", "--force", "v1.2.3", reviewedCommit)
				r.pushAllRefs()
				return reviewedCommit, expectedCommit
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := newReleaseGateRepository(t)
			reviewedCommit, expectedCommit := test.prepare(repository)
			repository.verifyFailure(test.version, reviewedCommit, expectedCommit)
		})
	}
}

func requireContains(t *testing.T, value string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			t.Errorf("missing required workflow contract %q", fragment)
		}
	}
}

func requireNotContains(t *testing.T, value string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if strings.Contains(value, fragment) {
			t.Errorf("forbidden workflow contract %q is present", fragment)
		}
	}
}

func jobSection(t *testing.T, workflow, job string) string {
	t.Helper()
	start := strings.Index(workflow, "  "+job+":\n")
	if start < 0 {
		t.Fatalf("missing job %q", job)
	}
	section := workflow[start:]
	if end := regexp.MustCompile(`(?m)^  [a-zA-Z0-9_-]+:\n`).FindStringIndex(section[len("  "+job+":\n"):]); end != nil {
		return section[:len("  "+job+":\n")+end[0]]
	}
	return section
}

type releaseGateRepository struct {
	t          *testing.T
	workingDir string
	remoteDir  string
	helper     string
	sequence   int
}

func newReleaseGateRepository(t *testing.T) *releaseGateRepository {
	t.Helper()
	root := t.TempDir()
	remoteDir := filepath.Join(root, "remote.git")
	workingDir := filepath.Join(root, "working")
	runGit(t, root, "init", "--quiet", "--bare", remoteDir)
	runGit(t, root, "init", "--quiet", "--initial-branch=main", workingDir)
	r := &releaseGateRepository{
		t:          t,
		workingDir: workingDir,
		remoteDir:  remoteDir,
		helper:     filepath.Join("..", "scripts", "verify-release-source.sh"),
	}
	r.run("config", "user.name", "Release Test")
	r.run("config", "user.email", "release-test@example.invalid")
	r.run("remote", "add", "origin", remoteDir)
	return r
}

func (r *releaseGateRepository) commit(message string) string {
	r.t.Helper()
	r.sequence++
	contents := []byte(message + "\n" + strings.Repeat("x", r.sequence) + "\n")
	if err := os.WriteFile(filepath.Join(r.workingDir, "release.txt"), contents, 0o644); err != nil {
		r.t.Fatal(err)
	}
	r.run("add", "release.txt")
	r.run("commit", "--quiet", "-m", message)
	return strings.TrimSpace(r.run("rev-parse", "HEAD"))
}

func (r *releaseGateRepository) lightweightTag(tag string) {
	r.t.Helper()
	r.run("tag", tag)
}

func (r *releaseGateRepository) annotatedTag(tag string) {
	r.t.Helper()
	r.run("tag", "-a", tag, "-m", tag)
}

func (r *releaseGateRepository) push() {
	r.t.Helper()
	r.run("push", "--quiet", "origin", "main", "--tags")
}

func (r *releaseGateRepository) pushAllRefs() {
	r.t.Helper()
	r.run("push", "--quiet", "--force", "origin", "refs/heads/main:refs/heads/main", "--tags")
}

func (r *releaseGateRepository) verifySuccess(version, reviewedCommit, expectedCommit string) {
	r.t.Helper()
	output, err := r.verify(version, reviewedCommit, expectedCommit)
	if err != nil {
		r.t.Fatalf("expected verification success: %v\n%s", err, output)
	}
	if !strings.Contains(output, "tag="+version+"\n") || !strings.Contains(output, "commit_sha=") {
		r.t.Fatalf("verification did not emit required outputs:\n%s", output)
	}
}

func (r *releaseGateRepository) verifyFailure(version, reviewedCommit, expectedCommit string) {
	r.t.Helper()
	if output, err := r.verify(version, reviewedCommit, expectedCommit); err == nil {
		r.t.Fatalf("expected verification failure, got success:\n%s", output)
	}
}

func (r *releaseGateRepository) verify(version, reviewedCommit, expectedCommit string) (string, error) {
	r.t.Helper()
	helper, err := filepath.Abs(r.helper)
	if err != nil {
		r.t.Fatal(err)
	}
	args := []string{helper, version, reviewedCommit, "origin"}
	if expectedCommit != "" {
		args = append(args, expectedCommit)
	}
	command := exec.Command("sh", args...)
	command.Dir = r.workingDir
	output, err := command.CombinedOutput()
	return string(output), err
}

func (r *releaseGateRepository) run(args ...string) string {
	r.t.Helper()
	return runGit(r.t, r.workingDir, args...)
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
