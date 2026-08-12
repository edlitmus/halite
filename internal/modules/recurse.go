package modules

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/edlitmus/halite/internal/sls"
)

func init() {
	register("file.recurse", fileRecurse)
}

// fileRecurse copies a directory from the state tree onto the host,
// creating what is missing and rewriting what differs.
//
//	/usr/local/etc/nginx/conf.d:
//	  file.recurse:
//	    - source: files/nginx/conf.d
//	    - file_mode: "0644"
//	    - dir_mode: "0755"
//	    - user: www
//	    - template: true
//	    - clean: true
//
// The source is resolved relative to the SLS file, like file.managed's, so
// the tree that ships to a host is the tree it copies from.
func fileRecurse(c *Ctx, id string, args map[string]any) Result {
	dest := Str(args, "name", id)
	source := Str(args, "source", "")
	if source == "" {
		return resFail("file.recurse needs a source directory")
	}
	src := source
	if !filepath.IsAbs(src) && c.BaseDir != "" {
		src = filepath.Join(c.BaseDir, src)
	}
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return resFail("cannot read source directory %s", src)
	}

	fileMode, err := parseModeArg(args, "file_mode")
	if err != nil {
		return resFail("%v", err)
	}
	dirMode, err := parseModeArg(args, "dir_mode")
	if err != nil {
		return resFail("%v", err)
	}
	wantUID, wantGID, ownerErr := resolveOwner(Str(args, "user", ""), Str(args, "group", ""))
	if ownerErr != nil && !c.Test {
		return resFail("%v", ownerErr)
	}
	render := Str(args, "template", "") == "true" || Str(args, "template", "") == "go"

	plan, err := planRecurse(c, src, dest, render)
	if err != nil {
		return resFail("%v", err)
	}
	if Bool(args, "clean", false) {
		extra, err := unmanagedUnder(dest, plan.managed)
		if err != nil {
			return resFail("%v", err)
		}
		plan.remove = extra
	}
	// Ownership and modes drift independently of content, so a tree that is
	// byte-identical can still need work.
	plan.chown = driftedOwners(plan.managed, wantUID, wantGID, ownerErr != nil)
	plan.chmod = driftedModes(plan.managed, fileMode, dirMode)

	if plan.empty() {
		return resOK(fmt.Sprintf("%s matches %s (%d files)", dest, source, len(plan.files())))
	}
	if c.Test {
		return resWould(plan.summary("would be", dest))
	}
	if err := plan.apply(dest, fileMode, dirMode, wantUID, wantGID); err != nil {
		return resFail("%v", err)
	}
	return resChanged(plan.summary("were", dest), plan.changes())
}

// recurseEntry is one path the state manages, with the content it should
// hold (nil for a directory).
type recurseEntry struct {
	path    string // absolute destination path
	rel     string // path relative to the destination root
	dir     bool
	desired []byte
	differs bool // content differs, or the path does not exist yet
}

// recursePlan is the work one file.recurse run has to do.
type recursePlan struct {
	managed []recurseEntry
	remove  []string
	chown   []string
	chmod   []string
}

func (p recursePlan) files() []recurseEntry {
	var out []recurseEntry
	for _, e := range p.managed {
		if !e.dir {
			out = append(out, e)
		}
	}
	return out
}

func (p recursePlan) written() []recurseEntry {
	var out []recurseEntry
	for _, e := range p.managed {
		if e.differs {
			out = append(out, e)
		}
	}
	return out
}

func (p recursePlan) empty() bool {
	return len(p.written()) == 0 && len(p.remove) == 0 && len(p.chown) == 0 && len(p.chmod) == 0
}

// summary describes the plan in one line, in the tense the caller needs.
func (p recursePlan) summary(tense, dest string) string {
	var parts []string
	files, dirs := 0, 0
	for _, e := range p.written() {
		if e.dir {
			dirs++
			continue
		}
		files++
	}
	if files > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) %s written", files, tense))
	}
	if dirs > 0 {
		parts = append(parts, fmt.Sprintf("%d director(ies) %s created", dirs, tense))
	}
	if n := len(p.remove); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s removed", n, tense))
	}
	if n := len(p.chown); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s chowned", n, tense))
	}
	if n := len(p.chmod); n > 0 {
		parts = append(parts, fmt.Sprintf("%d %s chmodded", n, tense))
	}
	return fmt.Sprintf("%s: %s", dest, strings.Join(parts, ", "))
}

// changes lists the paths that moved, capped so that a large tree does not
// bury the run's output.
func (p recursePlan) changes() map[string]string {
	changes := map[string]string{}
	add := func(key string, paths []string) {
		if len(paths) == 0 {
			return
		}
		sort.Strings(paths)
		const cap = 10
		if len(paths) > cap {
			changes[key] = fmt.Sprintf("%s and %d more", strings.Join(paths[:cap], ", "), len(paths)-cap)
			return
		}
		changes[key] = strings.Join(paths, ", ")
	}
	var written, created []string
	for _, e := range p.written() {
		if e.dir {
			created = append(created, e.rel)
			continue
		}
		written = append(written, e.rel)
	}
	add("written", written)
	add("created", created)
	add("removed", p.remove)
	add("chowned", p.chown)
	add("chmodded", p.chmod)
	return changes
}

// planRecurse walks the source tree and works out what each destination
// path should hold.
func planRecurse(c *Ctx, src, dest string, render bool) (recursePlan, error) {
	var plan recursePlan
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			st, statErr := os.Stat(target)
			plan.managed = append(plan.managed, recurseEntry{
				path: target, rel: rel, dir: true,
				differs: statErr != nil || !st.IsDir(),
			})
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if render {
			out, err := sls.Render(filepath.Base(path), string(body),
				sls.TemplateData{Grains: c.Grains, Pillar: c.Pillar, Mine: c.Mine})
			if err != nil {
				return fmt.Errorf("render %s: %w", path, err)
			}
			body = []byte(out)
		}
		current, readErr := os.ReadFile(target)
		plan.managed = append(plan.managed, recurseEntry{
			path: target, rel: rel, desired: body,
			differs: readErr != nil || !bytes.Equal(current, body),
		})
		return nil
	})
	return plan, err
}

// unmanagedUnder lists the paths under dest that the source tree does not
// account for, deepest first so that directories empty before they go.
func unmanagedUnder(dest string, managed []recurseEntry) ([]string, error) {
	keep := map[string]bool{}
	for _, e := range managed {
		keep[e.path] = true
	}
	var extra []string
	err := filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // nothing to clean in a destination that is not there
			}
			return err
		}
		if path == dest || keep[path] {
			return nil
		}
		extra = append(extra, path)
		if d.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	sort.Sort(sort.Reverse(sort.StringSlice(extra)))
	return extra, err
}

// driftedOwners lists managed paths whose ownership does not match.
func driftedOwners(managed []recurseEntry, uid, gid int, pending bool) []string {
	if uid < 0 && gid < 0 {
		return nil
	}
	var out []string
	for _, e := range managed {
		if pending || e.differs || ownerDrift(e.path, uid, gid) {
			out = append(out, e.rel)
		}
	}
	return out
}

// driftedModes lists managed paths whose permissions do not match.
func driftedModes(managed []recurseEntry, fileMode, dirMode os.FileMode) []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	var out []string
	for _, e := range managed {
		want := fileMode
		if e.dir {
			want = dirMode
		}
		if want == 0 {
			continue
		}
		st, err := os.Stat(e.path)
		if err != nil || st.Mode().Perm() != want {
			out = append(out, e.rel)
		}
	}
	return out
}

// apply performs the planned work: directories first, then files, then the
// removals, then ownership and modes over everything managed.
func (p recursePlan) apply(dest string, fileMode, dirMode os.FileMode, uid, gid int) error {
	mkdirMode := dirMode
	if mkdirMode == 0 {
		mkdirMode = 0o755
	}
	if err := os.MkdirAll(dest, mkdirMode); err != nil {
		return fmt.Errorf("mkdir %s: %w", dest, err)
	}
	for _, e := range p.managed {
		if !e.dir {
			continue
		}
		if err := os.MkdirAll(e.path, mkdirMode); err != nil {
			return fmt.Errorf("mkdir %s: %w", e.path, err)
		}
	}
	for _, e := range p.managed {
		if e.dir || !e.differs {
			continue
		}
		mode := fileMode
		if mode == 0 {
			mode = 0o644
		}
		if err := atomicWrite(e.path, e.desired, mode); err != nil {
			return fmt.Errorf("write %s: %w", e.path, err)
		}
	}
	for _, path := range p.remove {
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	for _, e := range p.managed {
		if runtime.GOOS != "windows" {
			want := fileMode
			if e.dir {
				want = dirMode
			}
			if want != 0 {
				if err := os.Chmod(e.path, want); err != nil {
					return fmt.Errorf("chmod %s: %w", e.path, err)
				}
			}
		}
		if uid >= 0 || gid >= 0 {
			if err := chown(e.path, uid, gid); err != nil {
				return fmt.Errorf("chown %s: %w", e.path, err)
			}
		}
	}
	return nil
}

// parseModeArg reads an octal mode argument. An absent mode means "leave
// whatever is there", which is not the same as 0000.
func parseModeArg(args map[string]any, key string) (os.FileMode, error) {
	raw := Str(args, key, "")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(raw, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, raw, err)
	}
	return os.FileMode(n), nil
}
