package buildx_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/contemper-project/contemper/internal/buildx"
)

func TestArgs(t *testing.T) {
	got := buildx.Args(buildx.Options{
		Context:   "./my-app",
		File:      "docker/Containerfile",
		Tag:       "my-app:dev",
		Platform:  "linux/arm64",
		BuildArgs: []string{"FOO=bar", "BAZ=qux"},
	})
	want := []string{
		"buildx", "build", "--load",
		"--platform", "linux/arm64",
		"-t", "my-app:dev",
		"-f", "docker/Containerfile",
		"--build-arg", "FOO=bar",
		"--build-arg", "BAZ=qux",
		"./my-app",
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Args = %q, want %q", got, want)
	}
}

func TestArgsNoFileNoBuildArgs(t *testing.T) {
	got := buildx.Args(buildx.Options{Context: ".", Tag: "my-app:dev", Platform: "linux/amd64"})
	want := []string{"buildx", "build", "--load", "--platform", "linux/amd64", "-t", "my-app:dev", "."}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Args = %q, want %q", got, want)
	}
}

func TestCommandIsShellQuotedAndCopyPasteable(t *testing.T) {
	cmd := buildx.Command(buildx.Options{
		Context:  "a dir/with spaces",
		Tag:      "my-app:dev",
		Platform: "linux/amd64",
	})
	if !strings.HasPrefix(cmd, "docker buildx build --load") {
		t.Errorf("Command = %q, want it to start with the docker invocation", cmd)
	}
	if !strings.Contains(cmd, "'a dir/with spaces'") {
		t.Errorf("Command = %q, want the space-containing context single-quoted", cmd)
	}
}

func TestDefaultTag(t *testing.T) {
	for dir, wantBase := range map[string]string{
		"./my-app":       "my-app",
		"my-app":         "my-app",
		"My_App":         "my_app",
		"./My Cool.App!": "my-cool.app",
		"/":              "image",
		"-":              "image",
		"./.hidden_":     "hidden",
	} {
		got := buildx.DefaultTag(dir)
		want := wantBase + ":dev"
		if got != want {
			t.Errorf("DefaultTag(%q) = %q, want %q", dir, got, want)
		}
	}
}

func TestDefaultTagCurrentDir(t *testing.T) {
	tmp := t.TempDir()
	sub := filepath.Join(tmp, "My-Project")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(sub); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	got := buildx.DefaultTag(".")
	want := "my-project:dev"
	if got != want {
		t.Errorf("DefaultTag(.) = %q, want %q", got, want)
	}
}

// dockerPathComponent is the path-component grammar of Docker's image
// reference format (github.com/distribution/reference): alphanumeric
// runs joined by ".", "_", "__" or one or more "-".
var dockerPathComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)

func TestSanitizeRepoName(t *testing.T) {
	cases := map[string]string{
		"my-app":           "my-app",
		"My.App_1":         "my.app_1",
		"has spaces":       "has-spaces",
		"Weird!Chars@Here": "weird-chars-here",
		"":                 "",
		"my__app":          "my__app",
		"my---app":         "my---app",
		"my..app":          "my-app",
		"my._app":          "my-app",
		"my___app":         "my-app",
		"-._leading":       "leading",
		"trailing._-":      "trailing",
		"café":             "caf",
		"!!!":              "",
	}
	for in, want := range cases {
		got := buildx.SanitizeRepoName(in)
		if got != want {
			t.Errorf("SanitizeRepoName(%q) = %q, want %q", in, got, want)
		}
		if got != "" && !dockerPathComponent.MatchString(got) {
			t.Errorf("SanitizeRepoName(%q) = %q, not a valid Docker repository name component", in, got)
		}
	}
}

// TestArgsDefaultFile checks the argv for the build file lookup when no
// -f is given: docker's own Dockerfile default wins, and a Containerfile
// is passed explicitly only when there is no Dockerfile.
func TestArgsDefaultFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		wantF bool
	}{
		{"Dockerfile only", []string{"Dockerfile"}, false},
		{"Containerfile only", []string{"Containerfile"}, true},
		{"both", []string{"Dockerfile", "Containerfile"}, false},
		{"neither", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.TempDir()
			for _, f := range tc.files {
				if err := os.WriteFile(filepath.Join(ctx, f), []byte("FROM scratch\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := buildx.Args(buildx.Options{Context: ctx, File: buildx.DefaultFile(ctx), Tag: "my-app:dev", Platform: "linux/amd64"})
			want := []string{"buildx", "build", "--load", "--platform", "linux/amd64", "-t", "my-app:dev"}
			if tc.wantF {
				want = append(want, "-f", filepath.Join(ctx, "Containerfile"))
			}
			want = append(want, ctx)
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("Args = %q, want %q", got, want)
			}
		})
	}
}

// TestBuildKeepsStdoutClean checks that nothing buildx writes reaches
// contemper's own stdout, which carries only the command's result.
func TestBuildKeepsStdoutClean(t *testing.T) {
	dir := t.TempDir()
	dockerPath := filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho buildx-stdout-noise\necho buildx-stderr-noise >&2\n"
	if err := os.WriteFile(dockerPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	stdout, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()
	orig := os.Stdout
	os.Stdout = stdout
	err = buildx.Build(dockerPath, buildx.Options{Context: ".", Tag: "my-app:dev", Platform: "linux/amd64"})
	os.Stdout = orig
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	got, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("buildx output reached stdout: %q", got)
	}
}

func TestCheckAvailableMissingDocker(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := buildx.CheckAvailable(buildx.Options{Context: ".", Tag: "my-app:dev", Platform: "linux/amd64"})
	if err == nil {
		t.Fatalf("CheckAvailable: expected an error when docker is not on PATH")
	}
	if !strings.Contains(err.Error(), "docker buildx build") || !strings.Contains(err.Error(), "contemper convert docker-daemon:my-app:dev") {
		t.Errorf("error %q does not show the copy-pasteable command and the convert suggestion", err.Error())
	}
}

func TestCheckAvailableMissingBuildx(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nif [ \"$1\" = buildx ]; then echo 'docker: buildx: command not found' >&2; exit 1; fi\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := buildx.CheckAvailable(buildx.Options{Context: ".", Tag: "my-app:dev", Platform: "linux/amd64"})
	if err == nil {
		t.Fatalf("CheckAvailable: expected an error when docker buildx version fails")
	}
	if !strings.Contains(err.Error(), "buildx") {
		t.Errorf("error %q does not mention buildx", err.Error())
	}
}

func TestCheckAvailableOK(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	dockerPath, err := buildx.CheckAvailable(buildx.Options{Context: ".", Tag: "my-app:dev", Platform: "linux/amd64"})
	if err != nil {
		t.Fatalf("CheckAvailable: %v", err)
	}
	if dockerPath == "" {
		t.Errorf("CheckAvailable: expected a resolved docker path")
	}
}
