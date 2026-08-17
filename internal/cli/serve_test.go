//go:build !noweb

package cli

import (
	"runtime"
	"strings"
	"testing"
)

// envOf returns a lookup over a fixed map, standing in for the environment of
// a machine the test is not running on.
func envOf(pairs map[string]string) func(string) string {
	return func(name string) string { return pairs[name] }
}

// A browser must not be launched where nobody could see it. Over SSH it would
// open on the far machine; on a runner it would hang.
func TestHasDesktopRefusesRemoteAndHeadlessSessions(t *testing.T) {
	cases := map[string]map[string]string{
		"ssh connection": {"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 22", "DISPLAY": ":0"},
		"ssh client":     {"SSH_CLIENT": "10.0.0.1 22 22", "DISPLAY": ":0"},
		"ssh tty":        {"SSH_TTY": "/dev/pts/0", "DISPLAY": ":0"},
		"ci":             {"CI": "true", "DISPLAY": ":0"},
	}

	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if hasDesktop(envOf(env)) {
				t.Error("hasDesktop said yes where there is no reachable desktop")
			}
		})
	}
}

func TestHasDesktopOnALocalSession(t *testing.T) {
	// The answer is platform-dependent: a macOS or Windows session is a
	// desktop session, while Unix needs a display server.
	local := map[string]string{}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		local["DISPLAY"] = ":0"
	}

	if !hasDesktop(envOf(local)) {
		t.Error("hasDesktop said no on a plain local session")
	}
}

// On Unix, no display server means nothing to open onto.
func TestHasDesktopNeedsADisplayOnUnix(t *testing.T) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("only Unix distinguishes a session from a display")
	}

	if hasDesktop(envOf(map[string]string{})) {
		t.Error("hasDesktop said yes with no display server")
	}
	if !hasDesktop(envOf(map[string]string{"WAYLAND_DISPLAY": "wayland-0"})) {
		t.Error("hasDesktop ignored a Wayland display")
	}
}

// The environment lookup defaults to the real one, so a caller that builds an
// App by hand does not get a nil dereference.
func TestEnvDefaultsWhenUnset(t *testing.T) {
	f := newFixture(t)

	// The fixture builds an App without Env; Run must fill it in.
	if _, _, code := f.run("info"); code != 0 {
		t.Fatal("a command failed with no Env configured")
	}
}

func TestServeHelpDescribesTheDefault(t *testing.T) {
	f := newFixture(t)

	out := f.mustRun("help", "serve")
	for _, want := range []string{"--no-open", "opens the page in your browser", "SSH"} {
		if !strings.Contains(out, want) {
			t.Errorf("help does not mention %q:\n%s", want, out)
		}
	}
}

// The flags must at least parse; actually serving would block the test.
func TestServeRejectsUnknownFlags(t *testing.T) {
	f := newFixture(t)

	_, stderr, code := f.run("serve", "--nonsense")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "usage:") {
		t.Fatalf("no usage line: %s", stderr)
	}
}
