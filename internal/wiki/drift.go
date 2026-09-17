package wiki

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Drift statuses. A line link drifts when the lines it names changed since the
// link was committed; see docs/dev/anchor-drift.md.
const (
	StatusLineMoved   = "line-moved"
	StatusLineChanged = "line-changed"
)

// ErrNoHistory means drift cannot be checked: the wiki is outside a git
// repository, git is unavailable, or the clone is shallow.
var ErrNoHistory = errors.New("no git history to check line anchors against")

// Drifted is a line link whose lines changed since it was committed.
type Drifted struct {
	Link

	// Since is the commit the lines are compared against.
	Since string `json:"since"`

	// Offer rewrites the anchor to where the lines moved; nil for
	// StatusLineChanged.
	Offer *Offer `json:"offer,omitempty"`
}

// driftTimeout bounds every git command Drift runs, together.
const driftTimeout = 30 * time.Second

// Drift compares each line link's lines in the commit that last added or
// removed the link's text on its page with the same file now. A link not yet
// committed is skipped, and so is every link sharing its text: it was written
// against the working tree, which git does not record. Links whose file is missing, or whose range is past its end,
// are Check's to report. A wiki with no line links has nothing to check, so
// it needs no history.
func (w *Wiki) Drift() ([]Drifted, error) { return w.drift(false) }

// DriftAll is Drift plus each line-out-of-range link whose lines moved within
// the shorter file, with StatusLineOutOfRange and the moved anchor as its
// Offer.
func (w *Wiki) DriftAll() ([]Drifted, error) { return w.drift(true) }

func (w *Wiki) drift(outOfRange bool) ([]Drifted, error) {
	links, err := w.links(`kind = ?`, KindLine)
	if err != nil || len(links) == 0 {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(w.Repo, ".git")); err != nil {
		return nil, fmt.Errorf("%w: not in a git repository", ErrNoHistory)
	}
	ctx, cancel := context.WithTimeout(context.Background(), driftTimeout)
	defer cancel()
	shallow, err := w.git(ctx, nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoHistory, err)
	}
	if strings.TrimSpace(string(shallow)) == "true" {
		return nil, fmt.Errorf("%w: the clone is shallow", ErrNoHistory)
	}
	pagesDir, err := filepath.Rel(w.Repo, w.PagesPath())
	if err != nil {
		return nil, err
	}

	// The baseline commit of each link, and each link's page as committed.
	files := map[string][]string{}
	committed := make([]string, len(links))
	for i, l := range links {
		file := filepath.ToSlash(filepath.Join(pagesDir, filepath.FromSlash(l.Page)+".md"))
		files[file] = append(files[file], l.Written())
		committed[i] = "HEAD:" + file
	}
	commits, err := w.lastChanged(ctx, filepath.ToSlash(pagesDir), files)
	if err != nil {
		return nil, err
	}
	since := make([]string, len(links))
	seen := map[string]int{}
	copies := map[[2]string]int{}
	for i, l := range links {
		file := committed[i][len("HEAD:"):]
		since[i] = commits[file][seen[file]]
		seen[file]++
		copies[[2]string{l.Page, l.Written()}]++
	}

	names := make([]string, len(links))
	for i, l := range links {
		names[i] = since[i] + ":" + l.Resolved
	}
	blobs, err := w.blobs(ctx, append(slices.Clone(names), committed...))
	if err != nil {
		return nil, err
	}

	current := map[string][][]byte{}
	var out []Drifted
	for i, l := range links {
		m := lineRange.FindStringSubmatch(l.Anchor)
		// Text the page holds more copies of than HEAD has an uncommitted copy,
		// which cannot be told from the committed ones.
		if m == nil || since[i] == "" || blobs[names[i]] == nil ||
			countDest(string(blobs[committed[i]]), l.Written()) < copies[[2]string{l.Page, l.Written()}] {
			continue
		}
		from, _ := strconv.Atoi(m[1])
		to := from
		if m[2] != "" {
			to, _ = strconv.Atoi(m[2])
		}
		old := splitLines(blobs[names[i]])
		if from < 1 || to < from || to > len(old) {
			continue
		}
		now, ok := current[l.Resolved]
		if !ok {
			raw, err := os.ReadFile(filepath.Join(w.Repo, filepath.FromSlash(l.Resolved)))
			if err == nil {
				now = splitLines(raw)
			}
			current[l.Resolved] = now
		}
		block := old[from-1 : to]
		moved := func(at int) *Offer {
			anchor := "#L" + strconv.Itoa(at)
			if m[2] != "" {
				anchor += "-L" + strconv.Itoa(at+to-from)
			}
			return &Offer{Label: "the lines moved to " + anchor[1:], New: l.Target + anchor}
		}
		if to > len(now) {
			if at := findBlock(now, block); outOfRange && at > 0 {
				d := Drifted{Link: l, Since: since[i], Offer: moved(at)}
				d.Status = StatusLineOutOfRange
				out = append(out, d)
			}
			continue
		}
		if linesEqual(now[from-1:to], block) {
			continue
		}

		d := Drifted{Link: l, Since: since[i]}
		d.Status = StatusLineChanged
		if at := findBlock(now, block); at > 0 {
			d.Status, d.Offer = StatusLineMoved, moved(at)
		}
		out = append(out, d)
	}
	return out, nil
}

// DriftStamp changes when Drift's result may have: HEAD moved, the index was
// written, or a file a line link names changed. A change to a page is Refresh's
// to notice.
func (w *Wiki) DriftStamp() (string, error) {
	rows, err := w.db.Query(`SELECT DISTINCT resolved FROM links WHERE kind = ? ORDER BY resolved`, KindLine)
	if err != nil {
		return "", err
	}
	var files []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			rows.Close()
			return "", err
		}
		files = append(files, filepath.Join(w.Repo, filepath.FromSlash(f)))
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(files) == 0 {
		return "", err
	}
	gitDir := filepath.Join(w.Repo, ".git")
	if raw, err := os.ReadFile(gitDir); err == nil {
		// A worktree's .git is a file naming its git directory.
		if dir, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir: "); ok {
			gitDir = dir
			if !filepath.IsAbs(dir) {
				gitDir = filepath.Join(w.Repo, dir)
			}
		}
	}
	var b strings.Builder
	for _, f := range append([]string{filepath.Join(gitDir, "HEAD"), filepath.Join(gitDir, "logs", "HEAD"), filepath.Join(gitDir, "index")}, files...) {
		if info, err := os.Stat(f); err == nil {
			fmt.Fprintf(&b, "%d.%d ", info.ModTime().UnixNano(), info.Size())
		} else {
			b.WriteString("- ")
		}
	}
	return b.String(), nil
}

// lastChanged returns, for each page file and each of its destinations, the
// newest commit that changed how many times the page contains it, or "" when
// none did. It reads the history of dir in one git process, following renames,
// and stops once every destination has a commit.
func (w *Wiki) lastChanged(ctx context.Context, dir string, files map[string][]string) (map[string][]string, error) {
	type page struct {
		dests []string
		out   []string
		net   []int
	}
	pages := map[string]*page{}
	left := 0
	out := map[string][]string{}
	for file, dests := range files {
		p := &page{dests: dests, out: make([]string, len(dests)), net: make([]int, len(dests))}
		pages[file], out[file] = p, p.out
		left += len(dests)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", w.Repo, "-c", "core.quotePath=false",
		"log", "-M", "--no-color", "--no-ext-diff", "-U0", "-p", "--format=%x1e%H", "--", dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}

	// alias maps a path in older commits to the page it was renamed to.
	alias := map[string]string{}
	named := func(path string) *page {
		if to, ok := alias[path]; ok {
			path = to
		}
		return pages[path]
	}
	var commit, from, minus string
	var renames [][2]string
	var cur *page
	var touched []*page
	inHunk := false
	settle := func() {
		for _, p := range touched {
			for k, n := range p.net {
				if n != 0 && p.out[k] == "" {
					p.out[k] = commit
					left--
				}
				p.net[k] = 0
			}
		}
		touched = touched[:0]
		// A rename applies to the commits older than this one.
		for _, r := range renames {
			if to, ok := alias[r[1]]; ok {
				alias[r[0]] = to
			} else {
				alias[r[0]] = r[1]
			}
		}
		renames = renames[:0]
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(nil, 16<<20)
	for left > 0 && sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "\x1e"):
			settle()
			commit, cur, inHunk = line[1:], nil, false
		case strings.HasPrefix(line, "diff --git "):
			cur, inHunk = nil, false
		case !inHunk && strings.HasPrefix(line, "rename from "):
			from = line[len("rename from "):]
		case !inHunk && strings.HasPrefix(line, "rename to "):
			renames = append(renames, [2]string{from, line[len("rename to "):]})
		case !inHunk && strings.HasPrefix(line, "--- "):
			minus = strings.TrimPrefix(line[4:], "a/")
		case !inHunk && strings.HasPrefix(line, "+++ "):
			path := strings.TrimPrefix(line[4:], "b/")
			if path == "/dev/null" {
				path = minus
			}
			cur = named(path)
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && cur != nil && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")):
			sign := 1
			if line[0] == '-' {
				sign = -1
			}
			changed := false
			for k, d := range cur.dests {
				if n := countDest(line[1:], d); n != 0 {
					cur.net[k] += sign * n
					changed = true
				}
			}
			if changed {
				touched = append(touched, cur)
			}
		}
	}
	if left > 0 {
		if err := sc.Err(); err != nil {
			return nil, err
		}
		settle()
		if err := cmd.Wait(); err != nil {
			return nil, fmt.Errorf("git log: %w", err)
		}
		return out, nil
	}
	// Every destination has its commit; the rest of the history is not needed.
	cancel()
	cmd.Wait()
	return out, nil
}

// countDest counts occurrences of a link destination in a line of markdown,
// skipping those inside a longer path or line range.
func countDest(line, dest string) int {
	n := 0
	for i := 0; ; {
		at := strings.Index(line[i:], dest)
		if at < 0 {
			return n
		}
		start, end := i+at, i+at+len(dest)
		before := start == 0 || !isPathByte(line[start-1])
		after := end == len(line) || !(line[end] == '-' || (line[end] >= '0' && line[end] <= '9'))
		if before && after {
			n++
		}
		i = start + 1
	}
}

func isPathByte(c byte) bool {
	return c == '/' || c == '.' || c == '_' || c == '-' || c == '~' ||
		(c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// blobs reads "rev:path" objects in one git process, each once. A missing
// object maps to nil.
func (w *Wiki) blobs(ctx context.Context, names []string) (map[string][]byte, error) {
	names = slices.Compact(slices.Sorted(slices.Values(names)))
	var in bytes.Buffer
	for _, n := range names {
		in.WriteString(n + "\n")
	}
	raw, err := w.git(ctx, &in, "cat-file", "--batch")
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	r := bufio.NewReader(bytes.NewReader(raw))
	for _, n := range names {
		header, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("git cat-file: %w", err)
		}
		// "<oid> <type> <size>", or "<name> missing" and "<name> ambiguous",
		// where the name may hold spaces.
		fields := strings.Fields(header)
		if len(fields) < 2 || fields[len(fields)-1] == "missing" || fields[len(fields)-1] == "ambiguous" {
			continue
		}
		size, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || len(fields) != 3 {
			return nil, fmt.Errorf("git cat-file: bad header %q", header)
		}
		body := make([]byte, size+1) // the content, then a newline
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, fmt.Errorf("git cat-file: %w", err)
		}
		if fields[1] == "blob" {
			out[n] = body[:size]
		}
	}
	return out, nil
}

// git runs a git command in the repository and returns its standard output.
func (w *Wiki) git(ctx context.Context, stdin *bytes.Buffer, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", w.Repo}, args...)...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

func splitLines(raw []byte) [][]byte {
	if len(raw) == 0 {
		return nil
	}
	return bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n"))
}

func linesEqual(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// findBlock returns the 1-based line where block occurs in lines, or 0 unless
// it occurs exactly once.
func findBlock(lines, block [][]byte) int {
	found := 0
	for i := 0; i+len(block) <= len(lines); i++ {
		if linesEqual(lines[i:i+len(block)], block) {
			if found > 0 {
				return 0
			}
			found = i + 1
		}
	}
	return found
}
