package app

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"

	sshadapter "github.com/mslxl/natcatty/adapters/ssh"
	appconnection "github.com/mslxl/natcatty/application/connection"
	appvault "github.com/mslxl/natcatty/application/vault"
	"github.com/mslxl/natcatty/domain/model"
)

// defaultKeySource discovers the unencrypted OpenSSH identity files offered
// after the selected Key in automatic authentication. Key material stays in
// memory and is never persisted or logged by this layer.
type defaultKeySource interface {
	DefaultKeys() []appconnection.KeyMaterial
}

// preferredDefaultKeyNames mirrors the original's preference order. Other
// id_* files follow in name order.
var preferredDefaultKeyNames = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

// defaultKeyNamePattern is OpenSSH's id_* convention; it deliberately excludes
// public keys such as id_rsa.pub.
var defaultKeyNamePattern = regexp.MustCompile(`^id_[A-Za-z0-9_-]+$`)

const (
	// maxDefaultKeyBytes bounds a key read so a huge or unusual file cannot
	// stall authentication.
	maxDefaultKeyBytes = 1 << 20
	// maxDefaultKeys bounds the automatic candidate list.
	maxDefaultKeys = 10
)

// sshDefaultKeys scans the user's ~/.ssh directory. Only regular files are
// read (symlinks are skipped), and keys needing a passphrase are ignored
// because default-key attempts never prompt.
type sshDefaultKeys struct {
	dir     string
	read    func(string) ([]byte, error)
	inspect func(privateKey, passphrase, certificate string) (appvault.KeyInspection, error)
}

// newSSHDefaultKeys returns a source rooted at the current user's ~/.ssh. When
// the home directory is unavailable it reports no keys rather than failing
// authentication.
func newSSHDefaultKeys() *sshDefaultKeys {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return &sshDefaultKeys{}
	}
	return &sshDefaultKeys{
		dir:     filepath.Join(home, ".ssh"),
		read:    os.ReadFile,
		inspect: sshadapter.KeyParser{}.Inspect,
	}
}

func (s *sshDefaultKeys) DefaultKeys() []appconnection.KeyMaterial {
	if s == nil || s.dir == "" || s.read == nil || s.inspect == nil {
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || !defaultKeyNamePattern.MatchString(name) {
			continue
		}
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		left, right := defaultKeyRank(names[i]), defaultKeyRank(names[j])
		if left != right {
			return left < right
		}
		return names[i] < names[j]
	})

	keys := make([]appconnection.KeyMaterial, 0, len(names))
	for _, name := range names {
		if len(keys) >= maxDefaultKeys {
			break
		}
		data, err := s.read(filepath.Join(s.dir, name))
		if err != nil || len(data) == 0 || len(data) > maxDefaultKeyBytes {
			continue
		}
		inspection, err := s.inspect(string(data), "", "")
		if err != nil || inspection.Encrypted || inspection.NeedsPassphrase {
			continue
		}
		keys = append(keys, appconnection.KeyMaterial{
			ID:         model.KeyID("default:" + name),
			Label:      name,
			PrivateKey: string(data),
		})
	}
	return keys
}

func defaultKeyRank(name string) int {
	for index, preferred := range preferredDefaultKeyNames {
		if name == preferred {
			return index
		}
	}
	return len(preferredDefaultKeyNames)
}
