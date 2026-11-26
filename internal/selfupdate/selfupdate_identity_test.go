package selfupdate

// The update channel (docs/dev/multigame/spec.md 1.2): installed copies
// look for releases of this repository, tagged build-N, with these files.
// A copy that comes back after a rollback updates itself the same way, so
// none of it changes, even when the name people see does. Each value is
// compared with its literal: renaming one fails here.

import (
	"context"
	"runtime"
	"testing"
)

func TestIdentity(t *testing.T) {
	if Repo != "TheCrowBro/Valheim-Easy-Server-" || RepoID != 1430331409 {
		t.Errorf("repository = %q (id %d), want TheCrowBro/Valheim-Easy-Server- (id 1430331409)", Repo, RepoID)
	}
	if DefaultAPI != "https://api.github.com" {
		t.Errorf("GitHub's API = %q", DefaultAPI)
	}
	for _, c := range []struct{ what, got, want string }{
		{"Windows program", windowsExe, "ValheimEasyServer.exe"},
		{"Mac program", macExe, "ValheimEasyServer-mac"},
		{"checksum file ending", sumSuffix, ".sha256"},
		{"release tag", reBuild.String(), `^build-(\d{1,9})$`},
		{"downloads", (&Updater{}).downloads(Repo), "https://github.com/TheCrowBro/Valheim-Easy-Server-/releases/download/"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.what, c.got, c.want)
		}
	}
	want := map[string]string{"windows": "ValheimEasyServer.exe", "darwin": "ValheimEasyServer-mac"}[runtime.GOOS]
	if want == "" {
		want = "ValheimEasyServer.exe" // Linux runs only the tests, which use the Windows name
	}
	if ExeName != want {
		t.Errorf("this system's program = %q, want %q", ExeName, want)
	}

	// A release published as today's releases are is found, with its
	// program and checksum, and nothing else counts as a build.
	program := ProgramHeader + " build 12"
	files := map[string]string{
		"/build-12/" + want:             program,
		"/build-12/" + want + ".sha256": sum(program) + "  " + want + "\n",
	}
	srv := fakeGitHub(t, files, func(base string) any {
		return []any{
			release(base, "build-12", "Safety nets before several games", want, want+".sha256"),
			release(base, "Build-13", "Not a build tag", want, want+".sha256"),
			release(base, "build-1234567890", "Too long a number", want, want+".sha256"),
		}
	})
	rel, ok, err := (&Updater{API: srv.URL}).Latest(context.Background())
	if err != nil || !ok || rel.Build != 12 || rel.Title != "Safety nets before several games" {
		t.Fatalf("latest = %+v %v %v, want build 12", rel, ok, err)
	}
	path := t.TempDir() + "/new"
	if err := (&Updater{API: srv.URL}).Download(context.Background(), rel, path); err != nil {
		t.Fatalf("download: %v", err)
	}
}
