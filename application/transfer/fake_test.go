package transfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	domainsftp "github.com/mslxl/natcatty/domain/transfer"
)

// fakeNode is one in-memory directory entry.
type fakeNode struct {
	dir     bool
	data    []byte
	mode    uint32
	modTime time.Time
	hidden  bool
	symlink bool
}

// fakeFS is an in-memory FileSystem for the application tests. Paths are
// always POSIX and rooted at "/".
type fakeFS struct {
	name string

	mu     sync.Mutex
	nodes  map[string]*fakeNode
	closed int
	// block, when non-nil, makes every file Read wait for a value or the
	// context, so a test can cancel mid-file.
	block chan struct{}
	// openGate, when non-nil, receives one value per file opened.
	openGate chan struct{}
	// failCreate reports a permission error for paths containing "denied".
	failCreate bool
}

func newFakeFS(name string) *fakeFS {
	return &fakeFS{
		name: name,
		nodes: map[string]*fakeNode{
			"/": {dir: true, mode: 0o755, modTime: time.Unix(0, 0)},
		},
	}
}

func (f *fakeFS) addDir(dir string) { f.add(dir, &fakeNode{dir: true, mode: 0o755}) }

func (f *fakeFS) addFile(dir, name string, data string, mode uint32) {
	f.add(path.Join(dir, name), &fakeNode{data: []byte(data), mode: mode})
}

func (f *fakeFS) add(full string, node *fakeNode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes[full] = node
	for current := full; ; {
		parent := path.Dir(current)
		if parent == current {
			break
		}
		if _, ok := f.nodes[parent]; !ok {
			f.nodes[parent] = &fakeNode{dir: true, mode: 0o755}
		}
		current = parent
	}
}

func (f *fakeFS) file(p string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.nodes[path.Clean(p)]
	if !ok || node.dir {
		return nil, false
	}
	return append([]byte(nil), node.data...), true
}

func (f *fakeFS) has(p string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.nodes[path.Clean(p)]
	return ok
}

func (f *fakeFS) Root(context.Context) (string, error) { return "/", nil }

func (f *fakeFS) entries(dir string) []domainsftp.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir = path.Clean(dir)
	var out []domainsftp.Entry
	for full, node := range f.nodes {
		if full == dir || path.Dir(full) != dir {
			continue
		}
		kind := domainsftp.KindFile
		if node.dir {
			kind = domainsftp.KindDir
		}
		out = append(out, domainsftp.Entry{
			Name:    path.Base(full),
			Path:    full,
			Kind:    kind,
			Size:    int64(len(node.data)),
			Mode:    node.mode,
			ModTime: node.modTime,
			Hidden:  node.hidden,
			Symlink: node.symlink,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (f *fakeFS) List(_ context.Context, dir string) ([]domainsftp.Entry, error) {
	f.mu.Lock()
	node, ok := f.nodes[path.Clean(dir)]
	f.mu.Unlock()
	if !ok {
		return nil, &fs.PathError{Op: "list", Path: dir, Err: fs.ErrNotExist}
	}
	if !node.dir {
		return nil, &fs.PathError{Op: "list", Path: dir, Err: fs.ErrInvalid}
	}
	return f.entries(dir), nil
}

func (f *fakeFS) Stat(_ context.Context, p string) (domainsftp.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p = path.Clean(p)
	node, ok := f.nodes[p]
	if !ok {
		return domainsftp.Entry{}, &fs.PathError{Op: "stat", Path: p, Err: fs.ErrNotExist}
	}
	kind := domainsftp.KindFile
	if node.dir {
		kind = domainsftp.KindDir
	}
	return domainsftp.Entry{
		Name:    path.Base(p),
		Path:    p,
		Kind:    kind,
		Size:    int64(len(node.data)),
		Mode:    node.mode,
		ModTime: node.modTime,
		Hidden:  node.hidden,
		Symlink: node.symlink,
	}, nil
}

func (f *fakeFS) Mkdir(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p = path.Clean(p)
	if _, ok := f.nodes[p]; ok {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrExist}
	}
	if _, ok := f.nodes[path.Dir(p)]; !ok {
		return &fs.PathError{Op: "mkdir", Path: p, Err: fs.ErrNotExist}
	}
	f.nodes[p] = &fakeNode{dir: true, mode: 0o755}
	return nil
}

func (f *fakeFS) MkdirAll(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for current := path.Clean(p); ; current = path.Dir(current) {
		if _, ok := f.nodes[current]; ok {
			break
		}
		f.nodes[current] = &fakeNode{dir: true, mode: 0o755}
		if current == "/" || path.Dir(current) == current {
			break
		}
	}
	return nil
}

func (f *fakeFS) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	f.mu.Lock()
	node, ok := f.nodes[path.Clean(p)]
	f.mu.Unlock()
	if !ok || node.dir {
		return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
	}
	if f.openGate != nil {
		select {
		case f.openGate <- struct{}{}:
		default:
		}
	}
	return &fakeReader{data: node.data, ctx: ctx, block: f.block}, nil
}

func (f *fakeFS) Create(_ context.Context, p string) (io.WriteCloser, error) {
	if f.failCreate && strings.Contains(p, "denied") {
		return nil, &fs.PathError{Op: "create", Path: p, Err: fs.ErrPermission}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p = path.Clean(p)
	if _, ok := f.nodes[path.Dir(p)]; !ok {
		return nil, &fs.PathError{Op: "create", Path: p, Err: fs.ErrNotExist}
	}
	node := &fakeNode{mode: 0o644, modTime: time.Unix(0, 0)}
	f.nodes[p] = node
	return &fakeWriter{fs: f, path: p, node: node}, nil
}

func (f *fakeFS) Remove(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p = path.Clean(p)
	node, ok := f.nodes[p]
	if !ok {
		return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrNotExist}
	}
	if node.dir {
		for full := range f.nodes {
			if full != p && path.Dir(full) == p {
				return &fs.PathError{Op: "remove", Path: p, Err: fs.ErrInvalid}
			}
		}
	}
	delete(f.nodes, p)
	return nil
}

func (f *fakeFS) RemoveAll(_ context.Context, p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p = path.Clean(p)
	for full := range f.nodes {
		if full == p || strings.HasPrefix(full, p+"/") {
			delete(f.nodes, full)
		}
	}
	return nil
}

func (f *fakeFS) Rename(_ context.Context, oldPath, newPath string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	oldPath, newPath = path.Clean(oldPath), path.Clean(newPath)
	node, ok := f.nodes[oldPath]
	if !ok {
		return &fs.PathError{Op: "rename", Path: oldPath, Err: fs.ErrNotExist}
	}
	if _, exists := f.nodes[newPath]; exists {
		return &fs.PathError{Op: "rename", Path: newPath, Err: fs.ErrExist}
	}
	delete(f.nodes, oldPath)
	f.nodes[newPath] = node
	for full := range f.nodes {
		if strings.HasPrefix(full, oldPath+"/") {
			moved := newPath + strings.TrimPrefix(full, oldPath)
			f.nodes[moved] = f.nodes[full]
			delete(f.nodes, full)
		}
	}
	return nil
}

func (f *fakeFS) Chmod(_ context.Context, p string, mode uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	node, ok := f.nodes[path.Clean(p)]
	if !ok {
		return &fs.PathError{Op: "chmod", Path: p, Err: fs.ErrNotExist}
	}
	node.mode = mode
	return nil
}

func (f *fakeFS) Join(parts ...string) string { return path.Join(parts...) }
func (f *fakeFS) Parent(p string) string      { return path.Dir(p) }
func (f *fakeFS) Base(p string) string        { return path.Base(p) }
func (f *fakeFS) Style() PathStyle            { return StylePOSIX }

func (f *fakeFS) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

// fakeReader is a file reader that honours the request context and an
// optional block channel.
type fakeReader struct {
	data  []byte
	at    int
	ctx   context.Context
	block chan struct{}
}

func (r *fakeReader) Read(p []byte) (int, error) {
	if r.block != nil {
		select {
		case <-r.block:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
	}
	if r.at >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.at:])
	r.at += n
	return n, nil
}

func (r *fakeReader) Close() error { return nil }

// fakeWriter accumulates writes and publishes them on Close.
type fakeWriter struct {
	fs     *fakeFS
	path   string
	node   *fakeNode
	buffer bytes.Buffer
	closed bool
}

func (w *fakeWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, errors.New("fake writer closed")
	}
	w.fs.mu.Lock()
	w.buffer.Write(p)
	w.node.data = append([]byte(nil), w.buffer.Bytes()...)
	w.fs.mu.Unlock()
	return len(p), nil
}

func (w *fakeWriter) Close() error {
	w.closed = true
	return nil
}
