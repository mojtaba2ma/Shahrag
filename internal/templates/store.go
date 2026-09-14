package templates

// The local library: templates that exist on this server.
//
// Two kinds live here, and the distinction matters:
//
//   - BUILT-IN templates are compiled into the binary (builtin.go). They are
//     always present, work with no network at all, and cannot be deleted —
//     so a server that can never reach GitHub still has a real site to serve
//     and a full set of error pages.
//
//   - INSTALLED templates were downloaded from the repository into
//     /var/lib/shahrag/templates/<id>/. Only the one the operator chose is
//     ever downloaded; the gallery itself costs nothing but a small JSON.
//
// Unpacking is the dangerous part. A .tar.gz from the network can contain
// absolute paths, "../" escapes, symlinks pointing at /etc/shadow, device
// nodes, and a compression ratio designed to fill the disk. Every one of
// those is refused explicitly below rather than trusted to be absent.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Installed describes a template present on this server.
type Installed struct {
	Meta
	// Dir is where the files live. Empty for a built-in, which is served
	// from the embedded filesystem until it is materialised.
	Dir string `json:"dir,omitempty"`
	// InstalledAt is when it was unpacked.
	InstalledAt time.Time `json:"installed_at,omitempty"`
	// Bytes is the size on disk.
	Bytes int64 `json:"bytes,omitempty"`
	// Files is the number of files on disk.
	Files int `json:"files,omitempty"`
}

func (c *Client) storeDir() string { return filepath.Join(c.cacheDir(), "store") }

func (c *Client) templateDir(id string) string { return filepath.Join(c.storeDir(), id) }

const metaFile = ".shahrag-template.json"

// List returns every template available locally: the built-ins first, then
// whatever has been installed, sorted by id so the UI order is stable.
func (c *Client) List() []Installed {
	out := make([]Installed, 0, len(builtinMetas)+4)
	seen := map[string]bool{}
	for _, m := range BuiltinMetas() {
		out = append(out, Installed{Meta: m})
		seen[m.ID] = true
	}
	entries, err := os.ReadDir(c.storeDir())
	if err != nil {
		sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) || seen[e.Name()] {
			continue
		}
		inst, err := c.readInstalled(e.Name())
		if err != nil {
			continue
		}
		out = append(out, inst)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Builtin != out[j].Builtin {
			return out[i].Builtin
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Has reports whether a template is usable right now, built-in or installed.
func (c *Client) Has(id string) bool {
	if _, ok := builtinByID(id); ok {
		return true
	}
	if !ValidID(id) {
		return false
	}
	st, err := os.Stat(filepath.Join(c.templateDir(id), metaFile))
	return err == nil && !st.IsDir()
}

func (c *Client) readInstalled(id string) (Installed, error) {
	var inst Installed
	b, err := os.ReadFile(filepath.Join(c.templateDir(id), metaFile))
	if err != nil {
		return inst, err
	}
	if err := json.Unmarshal(b, &inst); err != nil {
		return inst, err
	}
	inst.ID = id
	inst.Dir = c.templateDir(id)
	inst.Bytes, inst.Files = dirSize(inst.Dir)
	return inst, nil
}

func dirSize(dir string) (int64, int) {
	var total int64
	var n int
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
			n++
		}
		return nil
	})
	return total, n
}

// Install downloads one template and unpacks it. Only the chosen archive is
// transferred — that is the whole point of keeping the gallery out of the
// binary.
//
// The unpack goes to a temporary directory and is swapped into place only
// after it completes, so an interrupted download can never leave a half a
// website where a whole one used to be.
func (c *Client) Install(ctx context.Context, src Source, m Meta) (Installed, error) {
	var out Installed
	if !ValidID(m.ID) {
		return out, fmt.Errorf("%w: bad id", ErrNotFound)
	}
	if len(m.SHA256) != 64 {
		return out, fmt.Errorf("%w: index has no checksum", ErrChecksum)
	}
	if !safeRepoPath(m.Archive) || m.Archive == "" {
		return out, fmt.Errorf("%w: bad archive path", ErrNotFound)
	}

	blob, _, err := c.get(ctx, src, m.Archive, MaxArchiveBytes, archiveTimeout)
	if err != nil {
		return out, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	sum := sha256.Sum256(blob)
	if got := hex.EncodeToString(sum[:]); got != m.SHA256 {
		// Worth being loud: this is either a corrupted mirror or a
		// tampered archive, and both mean "do not put this on a web
		// server".
		return out, fmt.Errorf("%w: expected %s, got %s", ErrChecksum, m.SHA256, got)
	}

	if err := os.MkdirAll(c.storeDir(), 0o755); err != nil {
		return out, err
	}
	tmp, err := os.MkdirTemp(c.storeDir(), ".unpack-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(tmp)

	if err := Unpack(blob, tmp); err != nil {
		return out, err
	}
	if _, err := os.Stat(filepath.Join(tmp, "index.html")); err != nil {
		// Every template must have an entry page; without one nginx
		// would serve a directory listing or a 403.
		if m.Kind == KindSite {
			return out, errors.New("archive has no index.html")
		}
	}

	inst := Installed{Meta: m, InstalledAt: time.Now().UTC()}
	inst.Builtin = false
	mb, _ := json.MarshalIndent(inst, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, metaFile), mb, 0o644); err != nil {
		return out, err
	}

	dst := c.templateDir(m.ID)
	// Swap: move the old aside, move the new in, delete the old. Doing it
	// in this order means the window in which the directory does not
	// exist is a single rename, not a whole unpack.
	old := dst + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, old); err != nil {
			return out, err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Rename(old, dst) // put it back
		return out, err
	}
	_ = os.RemoveAll(old)

	return c.readInstalled(m.ID)
}

// Remove deletes an installed template. A built-in cannot be removed.
func (c *Client) Remove(id string) error {
	if _, ok := builtinByID(id); ok {
		return errors.New("built-in templates cannot be removed")
	}
	if !ValidID(id) {
		return ErrNotFound
	}
	dir := c.templateDir(id)
	if _, err := os.Stat(dir); err != nil {
		return ErrNotFound
	}
	return os.RemoveAll(dir)
}

// Materialise copies a template's files into dst, creating dst. Built-ins are
// copied out of the embedded filesystem; installed ones off disk. This is how
// a template becomes an actual document root for nginx.
func (c *Client) Materialise(id, dst string) error {
	if b, ok := builtinByID(id); ok {
		return copyBuiltin(b, dst)
	}
	if !ValidID(id) {
		return ErrNotFound
	}
	src := c.templateDir(id)
	if _, err := os.Stat(filepath.Join(src, metaFile)); err != nil {
		return ErrNotFound
	}
	return copyTree(src, dst)
}

// Unpack extracts a gzipped tar into dir, refusing everything unsafe.
//
// Refused, each for a concrete reason:
//   - absolute paths and "..", which write outside dir;
//   - symlinks and hardlinks, which point outside dir even when the path
//     inside the archive looks innocent;
//   - device nodes, fifos and sockets, which have no business in a website;
//   - more than MaxFiles entries or MaxUnpackedBytes of content, which is
//     how a small archive fills a small disk.
//
// A single leading directory (the usual "mytheme/..." wrapper produced by
// `tar czf x.tar.gz mytheme`) is stripped, so both layouts work.
func Unpack(blob []byte, dir string) error {
	// First pass: decide whether everything sits under one common
	// directory. Done on a cheap header-only scan so the decision is made
	// before a single byte is written.
	prefix, err := commonPrefix(blob)
	if err != nil {
		return err
	}

	zr, err := gzip.NewReader(strings.NewReader(string(blob)))
	if err != nil {
		return fmt.Errorf("not a gzip archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)

	var written int64
	var files int
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(h.Name), "./")
		if prefix != "" {
			if name == prefix {
				continue
			}
			if !strings.HasPrefix(name, prefix+"/") {
				continue
			}
			name = strings.TrimPrefix(name, prefix+"/")
		}
		if name == "" {
			continue
		}
		if !safeRepoPath(name) {
			return fmt.Errorf("archive: unsafe path %q", h.Name)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		// Belt and braces: even with safeRepoPath, verify the joined
		// path really is inside dir.
		if !within(dir, target) {
			return fmt.Errorf("archive: path escapes destination: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			if files > MaxFiles {
				return fmt.Errorf("archive: more than %d files", MaxFiles)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			remaining := MaxUnpackedBytes - written
			if remaining <= 0 {
				return fmt.Errorf("archive: unpacks to more than %d bytes", MaxUnpackedBytes)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			n, cerr := io.Copy(f, io.LimitReader(tr, remaining+1))
			closeErr := f.Close()
			if cerr != nil {
				return cerr
			}
			if closeErr != nil {
				return closeErr
			}
			if n > remaining {
				return fmt.Errorf("archive: unpacks to more than %d bytes", MaxUnpackedBytes)
			}
			written += n
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive: links are not allowed (%q)", h.Name)
		default:
			// Skip anything exotic quietly; refusing the whole
			// archive over a stray extended-header entry would be
			// unhelpful, but we never create the node.
			continue
		}
	}
	if files == 0 {
		return errors.New("archive contains no files")
	}
	return nil
}

// commonPrefix returns the single top-level directory shared by every entry,
// or "" when entries live at the archive root.
func commonPrefix(blob []byte) (string, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(blob)))
	if err != nil {
		return "", fmt.Errorf("not a gzip archive: %w", err)
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	prefix := ""
	first := true
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", fmt.Errorf("archive: %w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(h.Name), "./")
		name = strings.TrimSuffix(name, "/")
		if name == "" || strings.HasPrefix(name, "pax_global_header") {
			continue
		}
		top := name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			top = name[:i]
		} else if h.Typeflag != tar.TypeDir {
			// A regular file at the root: no common prefix.
			return "", nil
		}
		if first {
			prefix, first = top, false
			continue
		}
		if top != prefix {
			return "", nil
		}
	}
	return prefix, nil
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// copyTree copies src into dst, skipping the metadata file.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		if rel == metaFile {
			return nil
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
