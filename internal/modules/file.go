package modules

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

func init() {
	register("file.managed", fileManaged)
	register("file.directory", fileDirectory)
	register("file.absent", fileAbsent)
}

// fileManaged ensures a file exists with the given contents/source and mode.
//
//	/usr/local/etc/motd:
//	  file.managed:
//	    - contents: hello
//	    - mode: "0644"
//	    - makedirs: true
func fileManaged(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	contents := Str(args, "contents", "")
	source := Str(args, "source", "")
	mode := Str(args, "mode", "")
	makedirs := Bool(args, "makedirs", false)

	var desired []byte
	haveDesired := false
	switch {
	case contents != "":
		desired = []byte(contents)
		if !bytes.HasSuffix(desired, []byte("\n")) {
			desired = append(desired, '\n')
		}
		haveDesired = true
	case source != "":
		src := source
		if !filepath.IsAbs(src) && c.BaseDir != "" {
			src = filepath.Join(c.BaseDir, src)
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return resFail("cannot read source %s: %v", src, err)
		}
		desired = b
		haveDesired = true
	}

	current, readErr := os.ReadFile(name)
	exists := readErr == nil

	changes := map[string]string{}
	needContent := haveDesired && (!exists || !bytes.Equal(current, desired))
	needCreate := !exists && !haveDesired

	var wantMode os.FileMode
	needMode := false
	if mode != "" && runtime.GOOS != "windows" {
		n, err := strconv.ParseUint(mode, 8, 32)
		if err != nil {
			return resFail("invalid mode %q: %v", mode, err)
		}
		wantMode = os.FileMode(n)
		if exists {
			if st, err := os.Stat(name); err == nil && st.Mode().Perm() != wantMode {
				needMode = true
			}
		} else {
			needMode = true
		}
	}

	if !needContent && !needCreate && !needMode {
		return resOK(fmt.Sprintf("file %s is in the correct state", name))
	}

	if c.Test {
		if needContent || needCreate {
			changes["content"] = "would be updated"
		}
		if needMode {
			changes["mode"] = "would be set to " + mode
		}
		return Result{Ok: true, Changed: true, Comment: fmt.Sprintf("file %s would be updated", name), Changes: changes}
	}

	if makedirs {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			return resFail("makedirs: %v", err)
		}
	}
	if needContent || needCreate {
		if !haveDesired {
			desired = []byte{}
		}
		perm := os.FileMode(0o644)
		if wantMode != 0 {
			perm = wantMode
		}
		if err := os.WriteFile(name, desired, perm); err != nil {
			return resFail("write %s: %v", name, err)
		}
		if !exists {
			changes["content"] = fmt.Sprintf("created (%d bytes)", len(desired))
		} else {
			changes["content"] = fmt.Sprintf("updated (%d -> %d bytes)", len(current), len(desired))
		}
	}
	if needMode {
		if err := os.Chmod(name, wantMode); err != nil {
			return resFail("chmod %s: %v", name, err)
		}
		changes["mode"] = mode
	}
	return resChanged(fmt.Sprintf("file %s updated", name), changes)
}

func fileDirectory(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	mode := Str(args, "mode", "")
	if st, err := os.Stat(name); err == nil && st.IsDir() {
		return resOK(fmt.Sprintf("directory %s exists", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("directory %s would be created", name))
	}
	perm := os.FileMode(0o755)
	if mode != "" && runtime.GOOS != "windows" {
		if n, err := strconv.ParseUint(mode, 8, 32); err == nil {
			perm = os.FileMode(n)
		}
	}
	if err := os.MkdirAll(name, perm); err != nil {
		return resFail("mkdir %s: %v", name, err)
	}
	return resChanged(fmt.Sprintf("directory %s created", name), map[string]string{"directory": "created"})
}

func fileAbsent(c *Ctx, id string, args map[string]any) Result {
	name := Str(args, "name", id)
	if _, err := os.Stat(name); os.IsNotExist(err) {
		return resOK(fmt.Sprintf("%s is already absent", name))
	}
	if c.Test {
		return resWould(fmt.Sprintf("%s would be removed", name))
	}
	if err := os.RemoveAll(name); err != nil {
		return resFail("remove %s: %v", name, err)
	}
	return resChanged(fmt.Sprintf("%s removed", name), map[string]string{"removed": name})
}
