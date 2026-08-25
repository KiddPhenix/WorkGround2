package builtin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"workground2/internal/imageutil"
	"workground2/internal/tool"
)

// ViewImageName is the model-visible name of the built-in image-reading tool.
const ViewImageName = "view_image"

func init() { tool.RegisterBuiltin(viewImage{}) }

// viewImage reads a local image file and attaches its pixels to the
// conversation so a vision-capable model can see it. workDir, when non-empty,
// is the workspace the path resolves against and must stay within.
type viewImage struct {
	workDir     string
	forbidRoots []string
}

func (v viewImage) Name() string { return ViewImageName }

func (v viewImage) Description() string {
	return "Read a local image file and attach its pixels to the conversation so you can see it directly. The path may be workspace-relative, an @workspace reference, or an absolute path inside the workspace. Returns a short confirmation; the image itself is delivered to the model out-of-band, not as text. Read-only."
}

func (v viewImage) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Image file path (workspace-relative or absolute inside the workspace)"}
},
"required":["path"]
}`)
}

func (v viewImage) ReadOnly() bool     { return true }
func (v viewImage) PlanModeSafe() bool { return true }

// SnipHint: the confirmation is a short, single-source message; keep the head
// whole and drop the rest when a stale result is shortened.
func (v viewImage) SnipHint() tool.SnipHint {
	return tool.SnipHint{Head: 4, Tail: 0, HeadChars: 500, TailChars: 0}
}

func (v viewImage) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	text, _, err := v.ExecuteImages(ctx, args)
	return text, err
}

// ExecuteImages returns the confirmation and pixels from one stateless read.
func (v viewImage) ExecuteImages(_ context.Context, args json.RawMessage) (string, []string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", nil, fmt.Errorf("invalid args: %w", err)
	}
	p.Path = strings.TrimSpace(p.Path)
	p.Path = strings.TrimPrefix(p.Path, "@")
	if p.Path == "" {
		return "", nil, fmt.Errorf("path is required")
	}
	dataURL, mime, err := v.readImage(p.Path)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("Read image %s (%s). The pixels are attached to this conversation for your inspection.", p.Path, mime), []string{dataURL}, nil
}

// readImage resolves path within the workspace, validates it, reads it
// confined to the workspace root, detects the MIME from magic bytes, and
// returns a compressed data URL plus the detected MIME.
func (v viewImage) readImage(path string) (dataURL, mime string, err error) {
	root := v.workDir
	if root == "" {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			root = wd
		} else {
			return "", "", wdErr
		}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", err
	}

	absPath := filepath.FromSlash(path)
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(absRoot, absPath)
	}
	absPath = filepath.Clean(absPath)
	if confineRead(v.forbidRoots, absPath) {
		return "", "", &os.PathError{Op: "open", Path: absPath, Err: os.ErrNotExist}
	}

	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || !filepath.IsLocal(rel) || rel == ".." {
		return "", "", fmt.Errorf("image path must be inside the workspace")
	}

	fr, err := os.OpenRoot(absRoot)
	if err != nil {
		return "", "", err
	}
	defer fr.Close()

	var info os.FileInfo
	cur := ""
	for _, part := range strings.Split(filepath.Clean(rel), string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		info, err = fr.Lstat(cur)
		if err != nil {
			return "", "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", fmt.Errorf("image path must not contain symlinks")
		}
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("image path is a directory")
	}
	if info.Size() <= 0 {
		return "", "", fmt.Errorf("image file is empty")
	}
	if info.Size() > imageutil.MaxBytes {
		return "", "", fmt.Errorf("image file exceeds %d bytes", imageutil.MaxBytes)
	}

	f, err := fr.Open(rel)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return "", "", err
	}
	if !os.SameFile(info, opened) {
		return "", "", fmt.Errorf("image changed while opening")
	}

	raw, err := io.ReadAll(io.LimitReader(f, imageutil.MaxBytes+1))
	if err != nil {
		return "", "", err
	}
	if len(raw) == 0 || len(raw) > imageutil.MaxBytes {
		return "", "", fmt.Errorf("image must be between 1 byte and 10 MB")
	}
	if after, err := f.Stat(); err != nil {
		return "", "", err
	} else if !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return "", "", fmt.Errorf("image changed while reading")
	}

	mime = imageutil.Mime(raw)
	if mime == "" {
		return "", "", fmt.Errorf("%s is not a supported image", path)
	}
	raw, mime = imageutil.Compress(raw, mime)
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(raw), mime, nil
}
