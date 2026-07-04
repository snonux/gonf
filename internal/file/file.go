package file

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"os/user"
	"strconv"
	"strings"
	"text/template"

	"codeberg.org/snonux/gonf/internal/resource"
)

type File struct {
	path   string
	param  string
	source string
	user   string
	group  string
	mode   os.FileMode
}

type Option func(*File)

func WithContent(content string) Option {
	return func(f *File) {
		f.param = content
		f.source = ""
	}
}

func WithSource(source string) Option {
	return func(f *File) {
		if !strings.HasPrefix(source, "source://") {
			source = "source://" + source
		}
		f.source = source
		f.param = source
	}
}

func WithUser(user string) Option {
	return func(f *File) {
		f.user = user
	}
}

func WithGroup(group string) Option {
	return func(f *File) {
		f.group = group
	}
}

func WithMode(mode os.FileMode) Option {
	return func(f *File) {
		f.mode = mode
	}
}

func Have(path string, opts ...Option) error {
	curr, err := user.Current()
	if err != nil {
		log.Fatalf("failed to get current user for default: %v", err)
	}

	f := &File{
		path:  path,
		mode:  0o640,
		user:  curr.Username,
		group: curr.Gid,
	}

	for _, opt := range opts {
		opt(f)
	}

	return f.Apply()
}

func (f *File) Apply() error {
	_ = resource.Register("File", f.path)

	content, err := f.resolveContent()
	if err != nil {
		log.Fatalf("failed to resolve content for %s: %v", f.path, err)
	}

	return f.have(content)
}

func (f *File) resolveContent() ([]byte, error) {
	var content []byte
	var err error

	if strings.HasPrefix(f.param, "source://") {
		sourcePath := strings.TrimPrefix(f.param, "source://")
		content, err = os.ReadFile(sourcePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read source file %s: %w", sourcePath, err)
		}
	} else {
		content = []byte(f.param)
	}

	if strings.HasSuffix(f.path, ".tmpl") || (strings.HasPrefix(f.param, "source://") && strings.HasSuffix(strings.TrimPrefix(f.param, "source://"), ".tmpl")) {
		return f.applyTemplate(content)
	}

	return content, nil
}

func (f *File) applyTemplate(content []byte) ([]byte, error) {
	data := make(map[string]string)
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if len(pair) == 2 {
			data[pair[0]] = pair[1]
		}
	}
	data["Param"] = f.param

	tmpl, err := template.New("resource").Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("template parse error: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("template execute error: %w", err)
	}
	return buf.Bytes(), nil
}

func (f *File) have(content []byte) error {
	log.Printf("processing file: %s", f.path)
	existingChecksum := getChecksum(f.path)
	newChecksum := sha256.Sum256(content)
	log.Printf("computed checksum for new content: %x", newChecksum)

	tmpPath := f.path + ".tmp"
	if err := writeTmpFile(tmpPath, content, f.mode); err != nil {
		return err
	}

	if err := updateFromTmp(tmpPath, f.path, existingChecksum != newChecksum); err != nil {
		return err
	}

	return f.applyAttributes()
}

func (f *File) applyAttributes() error {
	// Apply Mode
	if err := os.Chmod(f.path, f.mode); err != nil {
		return fmt.Errorf("failed to chmod %s to %v: %w", f.path, f.mode, err)
	}
	log.Printf("set mode %v for %s", f.mode, f.path)

	// Apply User and Group
	uid, gid := -1, -1

	if f.user != "" {
		u, err := user.Lookup(f.user)
		if err != nil {
			return fmt.Errorf("failed to lookup user %s: %w", f.user, err)
		}
		uid, _ = strconv.Atoi(u.Uid)
	}

	if f.group != "" {
		gidInt, err := strconv.Atoi(f.group)
		if err != nil {
			return fmt.Errorf("group must be numeric for now: %s", f.group)
		}
		gid = gidInt
	}

	if err := os.Chown(f.path, uid, gid); err != nil {
		return fmt.Errorf("failed to chown %s to %s:%s: %w", f.path, f.user, f.group, err)
	}
	log.Printf("set owner %s:%s for %s", f.user, f.group, f.path)

	return nil
}

func getChecksum(path string) [32]byte {
	var checksum [32]byte
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("reading %s: %v (file does not exist or cannot be read)", path, err)
		return checksum
	}
	checksum = sha256.Sum256(data)
	log.Printf("computed checksum for %s: %x", path, checksum)
	return checksum
}

func writeTmpFile(tmpPath string, content []byte, mode os.FileMode) error {
	log.Printf("writing %d bytes to temporary file %s with mode %v", len(content), tmpPath, mode)
	if err := os.WriteFile(tmpPath, content, mode); err != nil {
		log.Printf("failed to write temporary file %s: %v", tmpPath, err)
		return err
	}
	log.Printf("successfully wrote temporary file %s", tmpPath)
	return nil
}

func updateFromTmp(tmpPath, path string, checksumChanged bool) error {
	if !checksumChanged {
		log.Printf("checksums match, removing temporary file %s", tmpPath)
		if err := os.Remove(tmpPath); err != nil {
			log.Printf("failed to remove temporary file %s: %v", tmpPath, err)
			return err
		}
		log.Printf("no changes needed for %s", path)
		return nil
	}

	log.Printf("checksums differ, renaming %s to %s", tmpPath, path)
	if err := os.Rename(tmpPath, path); err != nil {
		log.Printf("failed to rename %s to %s: %v", tmpPath, path, err)
		os.Remove(tmpPath)
		return err
	}
	log.Printf("successfully updated %s", path)
	return nil
}
