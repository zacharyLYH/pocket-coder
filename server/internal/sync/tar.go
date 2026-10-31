package sync

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// WriteTarEntry adds one file or symlink under name to tw, reading src.
func WriteTarEntry(tw *tar.Writer, name, src, typ string, mode uint32) error {
	if typ == TypeSymlink {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: int64(mode)})
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: int64(mode), Size: fi.Size()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// copyTarMembers copies the wanted tar members under root, hash-checking
// each file. strict rejects unexpected members; the backup path skips them
// (tar can walk a directory), while an incoming payload must not carry
// anything the plan did not name.
func copyTarMembers(r io.Reader, root string, entries []Entry, strict bool) error {
	want := make(map[string]Entry, len(entries))
	for _, e := range entries {
		want[e.Path] = e
	}
	tr := tar.NewReader(r)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if strict {
				return BadPayload("Incoming archive is not readable.")
			}
			return err
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		e, ok := want[name]
		if !ok {
			if strict {
				return BadPayload("Incoming archive contains an unexpected path.")
			}
			continue
		}
		if seen[name] || !ValidPath(name) {
			return BadPayload("Incoming archive contains an unexpected path.")
		}
		seen[name] = true
		if err := writeTarMember(tr, h, root, e); err != nil {
			if strict {
				return BadPayload(err.Error())
			}
			return err
		}
	}
	for p := range want {
		if !seen[p] {
			if strict {
				return BadPayload("Incoming archive is missing files.")
			}
			return fmt.Errorf("missing %q in container tar", p)
		}
	}
	return nil
}

func writeTarMember(tr *tar.Reader, h *tar.Header, root string, e Entry) error {
	dst := filepath.Join(root, filepath.FromSlash(e.Path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if e.Type == TypeSymlink {
		if h.Typeflag != tar.TypeSymlink || filepath.IsAbs(h.Linkname) {
			return fmt.Errorf("%q changed type during transfer", e.Path)
		}
		_ = os.Remove(dst)
		return os.Symlink(h.Linkname, dst)
	}
	if h.Typeflag != tar.TypeReg {
		return fmt.Errorf("%q changed type during transfer", e.Path)
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(e.Mode))
	if err != nil {
		return err
	}
	defer f.Close()
	hsh := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hsh), tr)
	if err != nil {
		return err
	}
	if n != e.Size || hex.EncodeToString(hsh.Sum(nil)) != e.Hash {
		return fmt.Errorf("%q did not match its manifest hash", e.Path)
	}
	return nil
}
