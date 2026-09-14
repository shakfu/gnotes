// Package gitsync commits the event logs and exchanges them with a remote.
//
// git is the whole sync layer. Because each author writes only their own file,
// two people's changes merge as a union of paths rather than as a textual
// conflict, and ordering the merged result is the event package's job. Nothing
// here needs to understand notes, tasks or events.
package gitsync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/shakfu/gnotes/internal/store"
)

// ErrNotARepo reports that the project is not inside a git working tree.
var ErrNotARepo = errors.New("not inside a git repository")

// ErrNoRemote reports that the repository has no remote to exchange with.
var ErrNoRemote = errors.New("this repository has no remote")

// Timeout bounds each git command in an unattended sync.
const Timeout = 2 * time.Minute

// Repo is a git working tree containing a gnotes project.
type Repo struct {
	// Root is the top of the working tree.
	Root string

	// Paths are the repository-relative paths gnotes owns. Every command is
	// restricted to them, so gnotes never touches the user's own work.
	Paths []string

	// unattended forbids prompts and bounds each command by Timeout.
	unattended bool
}

// Open locates the git repository containing the project and works out which
// paths inside it belong to gnotes.
func Open(p *store.Project) (*Repo, error) {
	root, ok := Toplevel(p.Dir)
	if !ok {
		return nil, ErrNotARepo
	}

	// The logs may sit outside the project directory when EventsRoot has been
	// pointed elsewhere, so both are resolved rather than assumed adjacent.
	var paths []string
	for _, abs := range []string{p.Dir, p.EventsDir()} {
		rel, err := filepath.Rel(root, resolve(abs))
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			// Outside the working tree git cannot version it. For the logs
			// that is an error rather than a skip: committing only the
			// project file would report success while nothing was saved.
			if abs == p.EventsDir() {
				return nil, fmt.Errorf("the event logs at %s are outside the git repository at %s", p.EventsDir(), root)
			}
			continue
		}
		paths = appendUnique(paths, filepath.ToSlash(rel))
	}
	return &Repo{Root: root, Paths: paths}, nil
}

// Toplevel returns the root of the git working tree containing dir, with
// symlinks resolved, and false when dir is not inside one.
//
// git reports the working tree with symlinks resolved, while a path from the
// user's working directory may not be. Comparing them unresolved makes every
// path look outside the repository on a system with a symlinked temp or home
// directory, so callers should compare against resolved paths.
func Toplevel(dir string) (string, bool) {
	out, err := run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	return resolve(strings.TrimSpace(out)), true
}

// resolve follows symlinks where it can. A path that does not exist yet is
// returned unchanged, since there is nothing to resolve and the caller only
// needs it to compare consistently with its own ancestors.
func resolve(path string) string {
	if out, err := filepath.EvalSymlinks(path); err == nil {
		return out
	}
	return path
}

// appendUnique adds a path unless it is already covered by one present.
func appendUnique(paths []string, candidate string) []string {
	for _, p := range paths {
		if p == candidate || strings.HasPrefix(candidate, p+"/") {
			return paths
		}
	}
	return append(paths, candidate)
}

// Dirty reports whether the gnotes paths have uncommitted changes.
func (r *Repo) Dirty() (bool, error) {
	args := append([]string{"status", "--porcelain", "--"}, r.Paths...)
	out, err := r.git(args...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Commit records the current state of the gnotes paths, and reports whether
// there was anything to record.
//
// Both the staging and the commit are restricted to the gnotes paths by
// pathspec. That is deliberate: a plain add-and-commit would sweep up whatever
// the user had already staged for their own work into a commit labelled as a
// notes sync.
func (r *Repo) Commit(message string) (bool, error) {
	dirty, err := r.Dirty()
	if err != nil {
		return false, err
	}
	if !dirty {
		return false, nil
	}

	// Staging is needed because a path git has never seen cannot be named in a
	// pathspec commit.
	addArgs := append([]string{"add", "--"}, r.Paths...)
	if _, err := r.git(addArgs...); err != nil {
		return false, fmt.Errorf("stage the event logs: %w", err)
	}

	// Hooks run. This is a commit in the user's repository like any other, and
	// a pre-commit hook that enforces signing or an audit trail is exactly the
	// policy a notes commit should not be the one exception to. A hook that
	// refuses fails the sync with git's own message.
	commitArgs := append([]string{"commit", "-m", message, "--"}, r.Paths...)
	if _, err := r.git(commitArgs...); err != nil {
		return false, fmt.Errorf("commit the event logs: %w", err)
	}
	return true, nil
}

// HasRemote reports whether a remote named origin exists.
func (r *Repo) HasRemote() bool {
	_, err := r.git("remote", "get-url", "origin")
	return err == nil
}

// remoteHasBranch reports whether origin already carries the branch. A remote
// that has never been pushed to has nothing to pull, and asking anyway fails
// with a message about a missing ref that says nothing useful.
func (r *Repo) remoteHasBranch(branch string) bool {
	out, err := r.git("ls-remote", "--heads", "origin", branch)
	return err == nil && strings.TrimSpace(out) != ""
}

// Branch returns the current branch name.
//
// "branch --show-current" rather than "rev-parse --abbrev-ref HEAD", because
// the latter fails outright on a branch with no commits yet, which is exactly
// the state of a repository someone has just run "git init" in.
func (r *Repo) Branch() (string, error) {
	if name, ok := r.currentBranch(); ok {
		return name, nil
	}
	// A detached HEAD has no branch name; report the commit instead so the
	// caller still has something meaningful to print.
	out, err := r.git("rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("determine the current branch: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// currentBranch returns the checked-out branch, and false on a detached HEAD.
func (r *Repo) currentBranch() (string, bool) {
	out, err := r.git("branch", "--show-current")
	name := strings.TrimSpace(out)
	return name, err == nil && name != ""
}

// Options configure a sync.
type Options struct {
	// Push also pulls from and pushes to origin.
	Push bool

	// Unattended is for a front end that owns the terminal or has none: the
	// interactive interface, the browser view and the agent server. git and
	// ssh may not prompt, since the prompt would either corrupt the screen or
	// wait on a terminal nobody is watching, and each command is bounded by
	// Timeout. The command line leaves it unset, so a passphrase prompt still
	// works there.
	Unattended bool
}

// Result summarises what a sync did.
type Result struct {
	// Committed reports whether local changes were recorded.
	Committed bool

	// Pulled and Pushed report whether the remote was contacted.
	Pulled bool
	Pushed bool

	// Branch is the branch that was synchronised.
	Branch string
}

// Sync commits local log changes and, when opt.Push is set, exchanges them
// with origin.
//
// The remote exchange is opt-in rather than automatic. gnotes keeps its logs
// on the working branch alongside the code, so a pull would move the user's
// own branch and a push would publish whatever else is on it. Deciding to do
// that is the user's call, not a side effect of saving a note.
//
// On error the Result still reports what was done before the failure, so a
// caller can say that a commit landed even though the push did not.
func Sync(p *store.Project, message string, opt Options) (Result, error) {
	r, err := Open(p)
	if err != nil {
		return Result{}, err
	}
	r.unattended = opt.Unattended

	// Projects created before the attributes existed get them on their next
	// sync, which commits them with the logs.
	if err := store.EnsureAttributes(p); err != nil {
		return Result{}, err
	}

	var res Result
	if res.Committed, err = r.Commit(message); err != nil {
		return res, err
	}

	// After the commit, so that a repository whose first commit this is has a
	// branch to report rather than an unborn one.
	res.Branch, _ = r.Branch()

	if !opt.Push {
		return res, nil
	}
	if _, ok := r.currentBranch(); !ok {
		return res, errors.New("HEAD is detached; check out a branch before exchanging with origin")
	}
	if !r.HasRemote() {
		return res, ErrNoRemote
	}

	// Pull only when there is something to pull. The first sync of a project
	// to a fresh remote has no upstream branch yet.
	if r.remoteHasBranch(res.Branch) {
		// A merge rather than a rebase. Rebasing would rewrite the local
		// commits, changing their ids on every sync, and the merge is trivial
		// anyway: each author's log is a separate file, so the two sides
		// rarely touch the same path.
		if _, err := r.git("pull", "--no-rebase", "--no-edit", "origin", res.Branch); err != nil {
			return res, fmt.Errorf("pull from origin: %w", err)
		}
		res.Pulled = true
	}

	// -u so that a first push also sets the upstream, matching what a person
	// would have typed by hand.
	if _, err := r.git("push", "-u", "origin", res.Branch); err != nil {
		return res, fmt.Errorf("push to origin: %w", err)
	}
	res.Pushed = true
	return res, nil
}

// run executes a git command in dir and returns its standard output.
//
// git is invoked as a subprocess rather than through a library. It is already
// installed wherever this tool is useful, it is the only implementation that
// honours the user's own configuration, hooks and credential helpers, and it
// keeps a large dependency out of a program whose git needs amount to five
// commands.
func run(dir string, args ...string) (string, error) {
	return runWith(dir, false, args...)
}

// repoEnv drops the variables that point git at a repository other than the
// one in the working directory. They are set inside git hooks, often as
// relative paths, and would send every command here to the wrong place.
func repoEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR":
			continue
		}
		out = append(out, kv)
	}
	return out
}

// git runs a command in the repository with its sync options.
func (r *Repo) git(args ...string) (string, error) {
	return runWith(r.Root, r.unattended, args...)
}

func runWith(dir string, unattended bool, args ...string) (string, error) {
	ctx := context.Background()
	if unattended {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = repoEnv(os.Environ())
	if unattended {
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0")
		// A user who set their own ssh command keeps it.
		if os.Getenv("GIT_SSH_COMMAND") == "" {
			cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		if ctx.Err() != nil {
			msg = fmt.Sprintf("gave up after %s: %s", Timeout, msg)
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return stdout.String(), nil
}
