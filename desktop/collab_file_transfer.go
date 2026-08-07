package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	_ "golang.org/x/image/webp"

	"workground2/internal/collab"
)

const (
	collaborationFileChunkSize        = int64(4 * 1024 * 1024)
	collaborationAutoReceiveLimit     = int64(1 << 20) // 1 MiB — files strictly below this are auto-received
	collaborationAutoReceiveMaxFiles  = 1024
	collaborationAutoReceiveMaxBytes  = int64(256 << 20)
	collaborationAutoReceiveRetryWait = 30 * time.Second
)

type collaborationFileManifest struct {
	FileID      string   `json:"fileId"`
	Size        int64    `json:"size"`
	ChunkSize   int64    `json:"chunkSize"`
	ChunkHashes []string `json:"chunkHashes"`
}

type collaborationSharedFile struct {
	FileID         string   `json:"fileId"`
	Room           string   `json:"room"`
	ShareAuthority string   `json:"shareAuthority,omitempty"`
	OwnerID        string   `json:"ownerId"`
	Path           string   `json:"path"`
	Name           string   `json:"name"`
	Size           int64    `json:"size"`
	MIME           string   `json:"mime,omitempty"`
	SHA256         string   `json:"sha256"`
	ManifestHash   string   `json:"manifestHash"`
	ChunkSize      int64    `json:"chunkSize"`
	ChunkHashes    []string `json:"chunkHashes"`
	OfferRevision  uint64   `json:"offerRevision"`
	ModTimeUnix    int64    `json:"modTimeUnix"`
	Status         string   `json:"status"`
	Error          string   `json:"error,omitempty"`
}

type collaborationFileOrigin struct {
	server   *http.Server
	listener net.Listener
	secret   string
	port     int
	hosts    []string
}

func (a *App) ShareCollaborationFiles(input ShareCollaborationFilesInput) ([]CollaborationFileTransfer, error) {
	c, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return nil, err
	}
	return c.shareFiles(a.bootContext(), input.Paths)
}

func (a *App) ReceiveCollaborationFile(input ReceiveCollaborationFileInput) (CollaborationFileTransfer, error) {
	c, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationFileTransfer{}, err
	}
	return c.receiveFile(a.bootContext(), input.FileID, input.Destination)
}

func (a *App) PauseCollaborationFile(input CollaborationFileActionInput) (CollaborationFileTransfer, error) {
	c, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationFileTransfer{}, err
	}
	return c.pauseFile(input.FileID)
}

func (a *App) ResumeCollaborationFile(input CollaborationFileActionInput) (CollaborationFileTransfer, error) {
	c, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationFileTransfer{}, err
	}
	return c.resumeFile(input.FileID)
}

func (a *App) RevokeCollaborationFile(input CollaborationFileActionInput) (CollaborationActionResult, error) {
	c, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationActionResult{}, err
	}
	return c.revokeFile(a.bootContext(), input.FileID)
}

var openCollaborationFilePath = openWorkspacePath
var revealCollaborationFilePath = revealPath

// OpenCollaborationFile opens a completed received file with its OS default app.
func (a *App) OpenCollaborationFile(input CollaborationFileActionInput) error {
	c, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return err
	}
	path, err := c.completedReceivedFilePath(input.FileID)
	if err != nil {
		return err
	}
	return openCollaborationFilePath(path)
}

// RevealCollaborationFile shows a completed received file in the native file manager.
func (a *App) RevealCollaborationFile(input CollaborationFileActionInput) error {
	c, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return err
	}
	path, err := c.completedReceivedFilePath(input.FileID)
	if err != nil {
		return err
	}
	return revealCollaborationFilePath(path)
}

// CollaborationFilePreview is returned by PreviewCollaborationFile for image
// files that pass server-side validation.
type CollaborationFilePreview struct {
	MIME    string `json:"mime"`
	DataURL string `json:"dataUrl"`
}

const (
	maxCollaborationImagePreviewBytes  = 10 * 1024 * 1024
	maxCollaborationImagePreviewPixels = 12_000_000
)

var errCollaborationFileNotImage = errors.New("collaboration file is not an image")

// supportedImageMIMEs lists the MIME types the preview endpoint serves.
var supportedImageMIMEs = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// PreviewCollaborationFile resolves a completed received file by sessionID +
// fileID, reads its content, and — only when the actual content is a supported
// image — returns a base64 data URL. Non-images and files that are not
// completed return an error so the frontend can fall back to a plain file card.
func (a *App) PreviewCollaborationFile(input CollaborationFileActionInput) (CollaborationFilePreview, error) {
	c, err := a.collaborationRuntime(input.SessionID)
	if err != nil {
		return CollaborationFilePreview{}, err
	}
	preview, err := c.previewFile(input.FileID)
	if errors.Is(err, errCollaborationFileNotImage) {
		return CollaborationFilePreview{}, nil
	}
	return preview, err
}

func (c *desktopCollaboration) previewFile(fileID string) (CollaborationFilePreview, error) {
	source, err := c.previewFilePath(fileID)
	if err != nil {
		return CollaborationFilePreview{}, err
	}
	var raw []byte
	var mimeType string
	if source.WorkspacePath != "" {
		raw, mimeType, err = readCollaborationImageRoot(c.ownerWorkspaceRoot, source.WorkspacePath, source.Size, source.SHA256)
	} else {
		raw, mimeType, err = readCollaborationImage(source.Path, source.Size, source.SHA256)
	}
	if err != nil {
		return CollaborationFilePreview{}, err
	}
	return CollaborationFilePreview{
		MIME:    mimeType,
		DataURL: "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(raw),
	}, nil
}

type collaborationPreviewSource struct {
	Path          string
	WorkspacePath string
	Size          int64
	SHA256        string
}

func (c *desktopCollaboration) previewFilePath(fileID string) (collaborationPreviewSource, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return collaborationPreviewSource{}, fmt.Errorf("fileId is required")
	}
	c.mu.RLock()
	offer, found := c.fileOfferLocked(fileID)
	room, roomInstance, shareAuthority := c.state.Room, c.roomInstance, c.shareAuthority
	share, shared := c.shares[fileID]
	transfer := cloneCollaborationTransferPtr(c.transfers[fileID])
	c.mu.RUnlock()
	if !found || offer.RevokedAt != nil {
		return collaborationPreviewSource{}, fmt.Errorf("file offer is unavailable")
	}
	if shared && share.Room == room && share.ShareAuthority == shareAuthority && share.Status == "available" && share.FileID == offer.ID && share.OwnerID == offer.OwnerID &&
		share.Size == offer.Size && strings.EqualFold(share.SHA256, offer.SHA256) && strings.EqualFold(share.ManifestHash, offer.ManifestHash) &&
		share.ChunkSize == offer.ChunkSize && len(share.ChunkHashes) == offer.ChunkCount && share.OfferRevision == offer.Revision {
		info, statErr := os.Stat(share.Path)
		if statErr != nil || !info.Mode().IsRegular() || info.Size() != share.Size || info.ModTime().UnixNano() != share.ModTimeUnix {
			return collaborationPreviewSource{}, fmt.Errorf("shared image source changed")
		}
		return collaborationPreviewSource{Path: share.Path, Size: offer.Size, SHA256: offer.SHA256}, nil
	}
	if transfer == nil || transfer.Direction != "receive" || transfer.Status != "completed" || !transferMatchesOffer(transfer, room, roomInstance, offer) {
		return collaborationPreviewSource{}, fmt.Errorf("received file is not completed")
	}
	if strings.TrimSpace(transfer.Destination) == "" {
		return collaborationPreviewSource{}, fmt.Errorf("received file destination is unavailable")
	}
	if transfer.Automatic {
		rel, err := validateRoomAttachmentRel(transfer.WorkspacePath)
		if err != nil {
			return collaborationPreviewSource{}, err
		}
		expected := filepath.Join(c.ownerWorkspaceRoot, rel)
		if c.ownerWorkspaceRoot == "" || !sameDesktopPath(expected, transfer.Destination) {
			return collaborationPreviewSource{}, fmt.Errorf("received file is outside its Session workspace")
		}
		return collaborationPreviewSource{Path: expected, WorkspacePath: filepath.ToSlash(rel), Size: offer.Size, SHA256: offer.SHA256}, nil
	}
	return collaborationPreviewSource{Path: transfer.Destination, Size: offer.Size, SHA256: offer.SHA256}, nil
}

func readCollaborationImage(path string, expectedSize int64, expectedSHA256 string) ([]byte, string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || !filepath.IsAbs(path) {
		return nil, "", fmt.Errorf("preview file path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, "", fmt.Errorf("preview file is unavailable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, "", fmt.Errorf("preview file must not be a symlink")
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCollaborationImagePreviewBytes {
		return nil, "", fmt.Errorf("preview file must be a regular file between 1 byte and 10 MB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("open preview file: %w", err)
	}
	defer f.Close()
	return readCollaborationImageFile(f, info, expectedSize, expectedSHA256)
}

func readCollaborationImageRoot(workspaceRoot, workspacePath string, expectedSize int64, expectedSHA256 string) ([]byte, string, error) {
	root, _, err := openRoomWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	rel, err := validateRoomAttachmentRel(workspacePath)
	if err != nil {
		return nil, "", err
	}
	if err := checkRoomAttachmentDirs(root, filepath.Dir(rel)); err != nil {
		return nil, "", err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return nil, "", fmt.Errorf("preview file is unavailable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxCollaborationImagePreviewBytes {
		return nil, "", fmt.Errorf("preview file must be a regular non-symlink file between 1 byte and 10 MB")
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, "", fmt.Errorf("open preview file: %w", err)
	}
	defer f.Close()
	return readCollaborationImageFile(f, info, expectedSize, expectedSHA256)
}

func readCollaborationImageFile(f *os.File, info os.FileInfo, expectedSize int64, expectedSHA256 string) ([]byte, string, error) {
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, "", fmt.Errorf("preview file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxCollaborationImagePreviewBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read preview file: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxCollaborationImagePreviewBytes {
		return nil, "", fmt.Errorf("preview file size is out of range")
	}
	if int64(len(raw)) != expectedSize || !validCollaborationSHA256(expectedSHA256) {
		return nil, "", fmt.Errorf("preview file no longer matches its Room offer")
	}
	sum := sha256.Sum256(raw)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), expectedSHA256) {
		return nil, "", fmt.Errorf("preview file no longer matches its Room offer")
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(opened, after) || after.Size() != opened.Size() {
		return nil, "", fmt.Errorf("preview file changed while reading")
	}
	mimeType := http.DetectContentType(raw[:min(len(raw), 512)])
	if !supportedImageMIMEs[mimeType] {
		return nil, "", errCollaborationFileNotImage
	}
	if collaborationImageAnimated(raw, mimeType) {
		return nil, "", fmt.Errorf("animated image previews are not supported")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("decode preview image: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxCollaborationImagePreviewPixels {
		return nil, "", fmt.Errorf("preview image dimensions are out of range")
	}
	return raw, mimeType, nil
}

func collaborationImageAnimated(raw []byte, mimeType string) bool {
	switch mimeType {
	case "image/png":
		return bytes.Contains(raw, []byte("acTL"))
	case "image/webp":
		return bytes.Contains(raw, []byte("ANIM")) || bytes.Contains(raw, []byte("ANMF"))
	case "image/gif":
		return gifImageBlocks(raw) > 1
	default:
		return false
	}
}

func gifImageBlocks(raw []byte) int {
	if len(raw) < 13 || string(raw[:6]) != "GIF87a" && string(raw[:6]) != "GIF89a" {
		return 0
	}
	offset := 13
	if raw[10]&0x80 != 0 {
		offset += 3 * (1 << ((raw[10] & 0x07) + 1))
	}
	frames := 0
	skipBlocks := func() bool {
		for offset < len(raw) {
			size := int(raw[offset])
			offset++
			if size == 0 {
				return true
			}
			if size > len(raw)-offset {
				return false
			}
			offset += size
		}
		return false
	}
	for offset < len(raw) {
		switch raw[offset] {
		case 0x3b:
			return frames
		case 0x21:
			offset += 2
			if !skipBlocks() {
				return frames
			}
		case 0x2c:
			frames++
			if frames > 1 || offset+10 > len(raw) {
				return frames
			}
			packed := raw[offset+9]
			offset += 10
			if packed&0x80 != 0 {
				offset += 3 * (1 << ((packed & 0x07) + 1))
			}
			if offset >= len(raw) {
				return frames
			}
			offset++ // LZW minimum code size.
			if !skipBlocks() {
				return frames
			}
		default:
			return frames
		}
	}
	return frames
}

func (c *desktopCollaboration) completedReceivedFilePath(fileID string) (string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return "", fmt.Errorf("fileId is required")
	}
	c.mu.RLock()
	transfer := cloneCollaborationTransferPtr(c.transfers[fileID])
	room, roomInstance := c.state.Room, c.roomInstance
	offer, found := c.fileOfferLocked(fileID)
	if transfer == nil || transfer.Direction != "receive" || transfer.Status != "completed" {
		c.mu.RUnlock()
		return "", fmt.Errorf("received file is not completed")
	}
	if !found || offer.RevokedAt != nil || !transferMatchesOffer(transfer, room, roomInstance, offer) {
		c.mu.RUnlock()
		return "", fmt.Errorf("received file no longer matches the Room offer")
	}
	path := strings.TrimSpace(transfer.Destination)
	c.mu.RUnlock()
	if path == "" {
		return "", fmt.Errorf("received file destination is unavailable")
	}
	if transfer.Automatic {
		root, absRoot, err := openRoomWorkspaceRoot(c.ownerWorkspaceRoot)
		if err != nil {
			return "", err
		}
		defer root.Close()
		rel, err := validateRoomAttachmentRel(transfer.WorkspacePath)
		if err != nil || checkRoomAttachmentDirs(root, filepath.Dir(rel)) != nil {
			return "", fmt.Errorf("received file attachment path is invalid")
		}
		expected := filepath.Join(absRoot, rel)
		if !sameDesktopPath(expected, path) {
			return "", fmt.Errorf("received file is outside its Session workspace")
		}
		_, matches, err := regularFileMatchesRoot(root, rel, offer.Size, offer.SHA256)
		if err != nil || !matches {
			return "", fmt.Errorf("received file is unavailable or changed")
		}
		return expected, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve received file: %w", err)
	}
	_, matches, err := regularFileMatchesPath(abs, offer.Size, offer.SHA256)
	if err != nil || !matches {
		return "", fmt.Errorf("received file is unavailable or changed")
	}
	return abs, nil
}

func (c *desktopCollaboration) shareFiles(ctx context.Context, paths []string) ([]CollaborationFileTransfer, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one file is required")
	}
	result := make([]CollaborationFileTransfer, 0, len(paths))
	for _, path := range paths {
		share, err := c.prepareSharedFile(path)
		if err != nil {
			return result, err
		}
		c.mu.RLock()
		room, ownerID, shareAuthority := c.state.Room, c.state.MemberID, c.shareAuthority
		c.mu.RUnlock()
		if room == "" || ownerID == "" || shareAuthority == "" {
			return result, fmt.Errorf("collaboration Room is unavailable")
		}
		share.Room, share.ShareAuthority, share.OwnerID = room, shareAuthority, ownerID
		c.mu.Lock()
		c.shares[share.FileID] = share
		c.persistLocked()
		c.mu.Unlock()
		action, submitErr := c.submit(ctx, share.FileID+":offer", collab.Command{Type: collab.CommandOfferFile, FileOffer: &collab.OfferFileInput{
			FileID: share.FileID, Name: share.Name, Size: share.Size, MIME: share.MIME, SHA256: share.SHA256, ManifestHash: share.ManifestHash, ChunkSize: share.ChunkSize, ChunkCount: len(share.ChunkHashes),
		}})
		if submitErr != nil {
			c.setShareStatus(share.FileID, "failed", submitErr.Error())
			return result, submitErr
		}
		status := "available"
		if action.Queued {
			status = "pending"
		}
		c.setShareStatus(share.FileID, status, "")
		if !action.Queued {
			c.mu.RLock()
			conn := c.conn
			c.mu.RUnlock()
			if conn != nil {
				_ = c.ensureFileOrigin(conn)
			}
			if err := c.registerFileOrigin(ctx, share.FileID); err != nil {
				c.setShareStatus(share.FileID, "unavailable", err.Error())
			}
		}
		result = append(result, c.fileTransferForShare(share.FileID))
	}
	c.emitState()
	return result, nil
}

func (c *desktopCollaboration) prepareSharedFile(path string) (collaborationSharedFile, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	info, err := os.Stat(path)
	if err != nil {
		return collaborationSharedFile{}, fmt.Errorf("inspect shared file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return collaborationSharedFile{}, fmt.Errorf("only regular files can be shared")
	}
	if info.Size() > collab.MaxSharedFileSize {
		return collaborationSharedFile{}, fmt.Errorf("shared file exceeds 1 TiB")
	}
	fileID := newCollaborationRequestID("file")
	f, err := os.Open(path)
	if err != nil {
		return collaborationSharedFile{}, fmt.Errorf("open shared file: %w", err)
	}
	defer f.Close()
	whole := sha256.New()
	chunkHashes := make([]string, 0, int((info.Size()+collaborationFileChunkSize-1)/collaborationFileChunkSize))
	buffer := make([]byte, collaborationFileChunkSize)
	for {
		n, readErr := io.ReadFull(f, buffer)
		if n > 0 {
			_, _ = whole.Write(buffer[:n])
			sum := sha256.Sum256(buffer[:n])
			chunkHashes = append(chunkHashes, hex.EncodeToString(sum[:]))
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil {
			return collaborationSharedFile{}, fmt.Errorf("hash shared file: %w", readErr)
		}
	}
	manifest, _ := json.Marshal(collaborationFileManifest{FileID: fileID, Size: info.Size(), ChunkSize: collaborationFileChunkSize, ChunkHashes: chunkHashes})
	manifestSum := sha256.Sum256(manifest)
	return collaborationSharedFile{
		FileID: fileID, Path: path, Name: info.Name(), Size: info.Size(), MIME: mime.TypeByExtension(filepath.Ext(info.Name())),
		SHA256: hex.EncodeToString(whole.Sum(nil)), ManifestHash: hex.EncodeToString(manifestSum[:]), ChunkSize: collaborationFileChunkSize,
		ChunkHashes: chunkHashes, OfferRevision: 1, ModTimeUnix: info.ModTime().UnixNano(), Status: "preparing",
	}, nil
}

func (c *desktopCollaboration) ensureFileOrigin(conn *collaborationConnection) error {
	if conn == nil {
		return fmt.Errorf("collaboration connection changed")
	}
	if !collaborationFilePeerNeedsOrigin(conn.filePeer) {
		return nil
	}
	c.mu.Lock()
	if conn == nil || c.conn != conn {
		c.mu.Unlock()
		return fmt.Errorf("collaboration connection changed")
	}
	if c.fileOrigin != nil {
		c.mu.Unlock()
		return nil
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		c.mu.Unlock()
		return err
	}
	bindHost := collaborationFileOriginBindHost(conn)
	listener, err := net.Listen("tcp", net.JoinHostPort(bindHost, "0"))
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("listen file origin: %w", err)
	}
	origin := &collaborationFileOrigin{
		listener: listener,
		secret:   base64.RawURLEncoding.EncodeToString(secretBytes),
		port:     listener.Addr().(*net.TCPAddr).Port,
		hosts:    collaborationLocalHosts(bindHost),
	}
	origin.server = &http.Server{Handler: http.HandlerFunc(c.serveSharedFile), ReadHeaderTimeout: 10 * time.Second}
	c.fileOrigin = origin
	c.mu.Unlock()
	go func() {
		if err := origin.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.mu.Lock()
			for id, share := range c.shares {
				share.Status, share.Error = "unavailable", err.Error()
				c.shares[id] = share
			}
			c.persistLocked()
			c.mu.Unlock()
			c.emitState()
		}
	}()
	return nil
}

func collaborationFileOriginBindHost(conn *collaborationConnection) string {
	if conn != nil && conn.lanEnabled {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

func collaborationFilePeerNeedsOrigin(peer collaborationFilePeer) bool {
	switch value := peer.(type) {
	case *httpCollaborationPeer:
		return true
	case *fallbackCollaborationFilePeer:
		return collaborationFilePeerNeedsOrigin(value.primary) || collaborationFilePeerNeedsOrigin(value.fallback)
	default:
		return false
	}
}

func (c *desktopCollaboration) serveSharedFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	const prefix = "/collab-file/v1/rooms/"
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, prefix), "/")
	if !strings.HasPrefix(r.URL.Path, prefix) || len(parts) < 4 || parts[1] != "files" {
		http.NotFound(w, r)
		return
	}
	room, roomErr := url.PathUnescape(parts[0])
	fileID, fileErr := url.PathUnescape(parts[2])
	if roomErr != nil || fileErr != nil {
		http.Error(w, "invalid file path", http.StatusBadRequest)
		return
	}
	c.mu.RLock()
	share, ok := c.shares[fileID]
	origin := c.fileOrigin
	shareAuthority := c.shareAuthority
	c.mu.RUnlock()
	if !ok || origin == nil || share.Room != room || share.ShareAuthority != shareAuthority || share.Status == "revoked" {
		http.Error(w, "file is unavailable", http.StatusNotFound)
		return
	}
	if err := collab.VerifyFileTicket(origin.secret, r.URL.Query().Get("ticket"), room, fileID, share.OwnerID, time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	info, err := os.Stat(share.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != share.Size || info.ModTime().UnixNano() != share.ModTimeUnix {
		c.setShareStatus(fileID, "source_changed", "源文件已移动或发生变化")
		http.Error(w, "source_changed", http.StatusConflict)
		return
	}
	switch {
	case len(parts) == 4 && parts[3] == "manifest":
		data, _ := json.Marshal(collaborationFileManifest{FileID: share.FileID, Size: share.Size, ChunkSize: share.ChunkSize, ChunkHashes: share.ChunkHashes})
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	case len(parts) == 5 && parts[3] == "chunks":
		index, err := strconv.Atoi(parts[4])
		if err != nil || index < 0 || index >= len(share.ChunkHashes) {
			http.Error(w, "invalid chunk", http.StatusBadRequest)
			return
		}
		c.serveSharedChunk(w, share, index)
	default:
		http.NotFound(w, r)
	}
}

func (c *desktopCollaboration) serveSharedChunk(w http.ResponseWriter, share collaborationSharedFile, index int) {
	f, err := os.Open(share.Path)
	if err != nil {
		http.Error(w, "file is unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()
	offset := int64(index) * share.ChunkSize
	length := min(share.ChunkSize, share.Size-offset)
	data := make([]byte, length)
	if _, err := io.ReadFull(io.NewSectionReader(f, offset, length), data); err != nil {
		http.Error(w, "read file chunk failed", http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != share.ChunkHashes[index] {
		c.setShareStatus(share.FileID, "source_changed", "源文件内容发生变化")
		http.Error(w, "source_changed", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (c *desktopCollaboration) registerFileOrigin(ctx context.Context, fileID string) error {
	c.mu.RLock()
	conn, origin := c.conn, c.fileOrigin
	share, ok := c.shares[fileID]
	shareAuthority := c.shareAuthority
	c.mu.RUnlock()
	if conn == nil || conn.filePeer == nil || origin == nil || !ok || share.ShareAuthority == "" || share.ShareAuthority != shareAuthority || shareAuthority != collaborationShareAuthorityKey(conn) {
		return fmt.Errorf("file origin is unavailable")
	}
	return conn.filePeer.RegisterFileOrigin(ctx, fileID, collab.RegisterFileOriginInput{Port: origin.port, Secret: origin.secret, Hosts: append([]string(nil), origin.hosts...)})
}

func (c *desktopCollaboration) restoreFileOrigins(conn *collaborationConnection) {
	c.mu.RLock()
	shareAuthority := collaborationShareAuthorityKey(conn)
	current := c.conn == conn && c.shareAuthority == shareAuthority
	c.mu.RUnlock()
	if !current {
		return
	}
	if err := c.ensureFileOrigin(conn); err != nil {
		return
	}
	c.mu.RLock()
	ids := make([]string, 0, len(c.shares))
	for id, share := range c.shares {
		if share.Room == conn.room && share.ShareAuthority == shareAuthority && share.OwnerID == conn.memberID && share.Status != "revoked" {
			ids = append(ids, id)
		}
	}
	c.mu.RUnlock()
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(c.app.bootContext(), 15*time.Second)
		err := c.registerFileOrigin(ctx, id)
		cancel()
		if err != nil {
			c.setShareStatus(id, "unavailable", err.Error())
		} else {
			c.setShareStatus(id, "available", "")
		}
	}
	c.emitState()
}

func (c *desktopCollaboration) hasPendingFileOrigins(conn *collaborationConnection) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn != conn || len(c.outbox) != 0 {
		return false
	}
	for _, share := range c.shares {
		if share.Room == conn.room && share.ShareAuthority == c.shareAuthority && share.OwnerID == conn.memberID && (share.Status == "pending" || share.Status == "unavailable" || share.Status == "failed") {
			return true
		}
	}
	return false
}

func (c *desktopCollaboration) receiveFile(ctx context.Context, fileID, destination string) (CollaborationFileTransfer, error) {
	fileID = strings.TrimSpace(fileID)
	c.mu.RLock()
	file, ok := c.fileOfferLocked(fileID)
	c.mu.RUnlock()
	if !ok || file.RevokedAt != nil {
		return CollaborationFileTransfer{}, fmt.Errorf("file offer is unavailable")
	}
	if err := validateCollaborationFileOffer(file, collab.MaxSharedFileSize); err != nil {
		return CollaborationFileTransfer{}, fmt.Errorf("invalid file offer: %w", err)
	}
	if strings.TrimSpace(destination) == "" {
		path, err := runtime.SaveFileDialog(c.app.ctx, runtime.SaveDialogOptions{DefaultFilename: file.Name, Title: "接收共享文件"})
		if err != nil {
			return CollaborationFileTransfer{}, err
		}
		destination = path
	}
	destination = filepath.Clean(strings.TrimSpace(destination))
	if destination == "." || destination == "" {
		return CollaborationFileTransfer{}, fmt.Errorf("未选择保存位置")
	}
	c.mu.Lock()
	if old := c.transfers[fileID]; old != nil && old.Status == "completed" && transferMatchesOffer(old, c.state.Room, c.roomInstance, file) {
		view := cloneCollaborationTransfer(*old)
		c.mu.Unlock()
		return view, nil
	}
	c.cancelFileTransferLocked(fileID)
	transfer := &CollaborationFileTransfer{ID: stableCollaborationID("transfer", c.roomInstance+"\x00"+fileID+"\x00"+c.state.MemberID), FileID: fileID, Room: c.state.Room, RoomInstance: c.roomInstance, OwnerID: file.OwnerID, SHA256: file.SHA256, ManifestHash: file.ManifestHash, ChunkSize: file.ChunkSize, ChunkCount: file.ChunkCount, OfferRevision: file.Revision, Direction: "receive", Name: file.Name, Status: "negotiating", Total: file.Size, Destination: destination, Completed: make([]bool, file.ChunkCount)}
	if old := c.transfers[fileID]; old != nil && old.Destination == destination && old.Total == file.Size && len(old.Completed) == file.ChunkCount {
		transfer = old
		transfer.Room, transfer.RoomInstance, transfer.OwnerID, transfer.SHA256, transfer.ManifestHash = c.state.Room, c.roomInstance, file.OwnerID, file.SHA256, file.ManifestHash
		transfer.ChunkSize, transfer.ChunkCount, transfer.OfferRevision = file.ChunkSize, file.ChunkCount, file.Revision
		transfer.Status, transfer.Error, transfer.Retryable, transfer.PausedByUser, transfer.AutoBlocked = "negotiating", "", false, false, false
	}
	c.transfers[fileID] = transfer
	c.persistLocked()
	view := cloneCollaborationTransfer(*transfer)
	c.mu.Unlock()
	c.emitState()
	c.startFileDownload(fileID)
	return view, nil
}

const collaborationAutoReceiveAttempts = 7

var errCollaborationDestinationConflict = errors.New("collaboration destination already exists with different content")
var errCollaborationAttachmentPolicy = errors.New("unsafe collaboration attachment path")

type collaborationDownloadTarget struct {
	file      *os.File
	root      *os.Root
	partPath  string
	destPath  string
	partRel   string
	destRel   string
	automatic bool
}

// collaborationVerifiedFile memoizes a content verification only while the
// path still names the same regular file with unchanged size and mtime. The
// cache is reset for every installed Room connection.
type collaborationVerifiedFile struct {
	Path    string
	SHA256  string
	Info    os.FileInfo
	Matches bool
}

func (c *desktopCollaboration) startFileDownload(fileID string) {
	c.mu.Lock()
	transfer := c.transfers[fileID]
	if transfer == nil || transfer.PausedByUser || transfer.Automatic && (c.autoScanClosed || c.conn == nil) {
		c.mu.Unlock()
		return
	}
	c.cancelFileTransferLocked(fileID)
	base := context.Background()
	if c.app != nil {
		base = c.app.bootContext()
	}
	ctx, cancel := context.WithCancel(base)
	if c.transferLocks == nil {
		c.transferLocks = map[string]*sync.Mutex{}
	}
	if c.autoReceiveSem == nil {
		c.autoReceiveSem = make(chan struct{}, 2)
	}
	run := c.transferRun[fileID]
	fileLock := c.transferLocks[fileID]
	if fileLock == nil {
		fileLock = &sync.Mutex{}
		c.transferLocks[fileID] = fileLock
	}
	automatic := transfer.Automatic
	sem := c.autoReceiveSem
	retryDelay := c.autoRetryDelay
	c.transferCancel[fileID] = cancel
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			if c.transferRun[fileID] == run {
				delete(c.transferCancel, fileID)
			}
			c.mu.Unlock()
			if automatic {
				c.signalAutoReceiveFiles()
			}
		}()
		if automatic {
			c.downloadAutomaticFile(ctx, fileID, run, fileLock, sem, retryDelay)
			return
		}
		fileLock.Lock()
		defer fileLock.Unlock()
		if ctx.Err() == nil {
			c.downloadFile(ctx, fileID, run)
		}
	}()
}

func (c *desktopCollaboration) downloadAutomaticFile(ctx context.Context, fileID string, run uint64, fileLock *sync.Mutex, sem chan struct{}, retryDelay func(int) time.Duration) {
	fileLock.Lock()
	select {
	case sem <- struct{}{}:
	case <-ctx.Done():
		fileLock.Unlock()
		return
	}
	c.downloadFile(ctx, fileID, run)
	<-sem
	fileLock.Unlock()
	if ctx.Err() != nil {
		return
	}
	c.deferAutomaticFileRetry(fileID, run, retryDelay)
}

func autoReceiveRetryDelay(attempt int) time.Duration {
	shift := min(max(attempt-1, 0), 6)
	return 500 * time.Millisecond * time.Duration(1<<shift)
}

func (c *desktopCollaboration) downloadFile(ctx context.Context, fileID string, run uint64) {
	c.mu.RLock()
	transfer := cloneCollaborationTransferPtr(c.transfers[fileID])
	conn := c.conn
	file, found := c.fileOfferLocked(fileID)
	room, roomInstance := c.state.Room, c.roomInstance
	currentRun := c.transferRun[fileID]
	c.mu.RUnlock()
	if currentRun != run || transfer == nil {
		return
	}
	if conn == nil || conn.filePeer == nil || !found {
		c.failFileTransferRun(fileID, run, "waiting_sender", "分享者或房间连接当前不可用", true)
		return
	}
	if file.RevokedAt != nil || !transferMatchesOffer(transfer, room, roomInstance, file) {
		c.failFileTransferRun(fileID, run, "failed", "文件分享已撤销或发生变化", false)
		return
	}
	maxSize := collab.MaxSharedFileSize
	if transfer.Automatic {
		maxSize = collaborationAutoReceiveLimit - 1
	}
	if err := validateCollaborationFileOffer(file, maxSize); err != nil {
		c.failFileTransferRun(fileID, run, "failed", "文件声明校验失败: "+err.Error(), false)
		return
	}
	ticket, manifest, err := conn.filePeer.fetchFileManifest(ctx, fileID, collaborationManifestLimit(file), !transfer.Automatic)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		status, retryable := fileTransferFailure(err)
		c.failFileTransferRun(fileID, run, status, err.Error(), retryable)
		return
	}
	if !fileTicketMatchesOffer(ticket, file) {
		c.failFileTransferRun(fileID, run, "failed", "文件票据与分享声明不一致", false)
		return
	}
	for _, expected := range manifest.ChunkHashes {
		if !validCollaborationSHA256(expected) {
			c.failFileTransferRun(fileID, run, "failed", "文件清单包含无效分块摘要", false)
			return
		}
	}
	manifestData, _ := json.Marshal(manifest)
	manifestSum := sha256.Sum256(manifestData)
	if manifest.FileID != file.ID || manifest.Size != file.Size || manifest.ChunkSize != file.ChunkSize || manifest.ChunkSize <= 0 || len(manifest.ChunkHashes) != file.ChunkCount || hex.EncodeToString(manifestSum[:]) != file.ManifestHash {
		c.failFileTransferRun(fileID, run, "failed", "文件清单校验失败", false)
		return
	}
	transfer = c.prepareFileTransferPart(fileID, run)
	if transfer == nil {
		return
	}
	target, err := c.openDownloadTarget(*transfer)
	if err != nil {
		c.failFileTransferRun(fileID, run, "failed", err.Error(), true)
		return
	}
	defer target.close()
	f := target.file
	if err := f.Truncate(file.Size); err != nil {
		c.failFileTransferRun(fileID, run, "failed", err.Error(), true)
		return
	}
	if !c.updateFileTransferRun(fileID, run, func(value *CollaborationFileTransfer) {
		value.Status, value.Error, value.Retryable = "downloading", "", false
	}) {
		return
	}
	for index, expected := range manifest.ChunkHashes {
		if ctx.Err() != nil {
			return
		}
		c.mu.RLock()
		current := c.transfers[fileID]
		done := c.transferRun[fileID] == run && current != nil && index < len(current.Completed) && current.Completed[index]
		c.mu.RUnlock()
		if done {
			continue
		}
		if time.Until(ticket.ExpiresAt) < 30*time.Second {
			ticket, err = conn.filePeer.fileTicket(ctx, fileID)
			if err != nil {
				if ctx.Err() == nil {
					c.failFileTransferRun(fileID, run, "waiting_sender", err.Error(), true)
				}
				return
			}
			if !fileTicketMatchesOffer(ticket, file) {
				c.failFileTransferRun(fileID, run, "failed", "文件票据与分享声明不一致", false)
				return
			}
		}
		if transfer.Automatic {
			ticket.DirectURLs = nil
		}
		data, err := conn.filePeer.fetchFileChunk(ctx, ticket, index)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			status, retryable := fileTransferFailure(err)
			c.failFileTransferRun(fileID, run, status, err.Error(), retryable)
			return
		}
		offset := int64(index) * file.ChunkSize
		if offset < 0 || offset >= file.Size && file.Size != 0 {
			c.failFileTransferRun(fileID, run, "failed", "文件分块范围无效", false)
			return
		}
		expectedLength := min(file.ChunkSize, file.Size-offset)
		if expectedLength < 0 || int64(len(data)) != expectedLength {
			c.failFileTransferRun(fileID, run, "failed", "文件分块长度校验失败", false)
			return
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			c.failFileTransferRun(fileID, run, "failed", "文件分块校验失败", true)
			return
		}
		if _, err := f.WriteAt(data, offset); err != nil {
			c.failFileTransferRun(fileID, run, "failed", err.Error(), true)
			return
		}
		if !c.updateFileTransferRun(fileID, run, func(value *CollaborationFileTransfer) {
			if index < len(value.Completed) {
				value.Completed[index] = true
			}
			value.Transferred = completedFileBytes(value.Completed, value.Total, file.ChunkSize)
		}) {
			return
		}
	}
	if err := f.Sync(); err != nil {
		c.failFileTransferRun(fileID, run, "failed", err.Error(), true)
		return
	}
	info, err := f.Stat()
	if err != nil || info.Size() != file.Size {
		c.failFileTransferRun(fileID, run, "failed", "完整文件大小校验失败", false)
		return
	}
	if ctx.Err() != nil {
		return
	}
	if !c.updateFileTransferRun(fileID, run, func(value *CollaborationFileTransfer) { value.Status = "verifying" }) {
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		c.failFileTransferRun(fileID, run, "failed", err.Error(), true)
		return
	}
	whole := sha256.New()
	written, copyErr := io.Copy(whole, io.LimitReader(f, file.Size+1))
	if copyErr != nil || written != file.Size || hex.EncodeToString(whole.Sum(nil)) != file.SHA256 {
		c.updateFileTransferRun(fileID, run, func(value *CollaborationFileTransfer) {
			for index := range value.Completed {
				value.Completed[index] = false
			}
			value.Transferred = 0
		})
		c.failFileTransferRun(fileID, run, "failed", "完整文件校验失败", true)
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := target.verifyPart(); err != nil {
		c.failFileTransferRun(fileID, run, "failed", err.Error(), false)
		return
	}
	if err := target.closeFile(); err != nil {
		c.failFileTransferRun(fileID, run, "failed", err.Error(), true)
		return
	}
	if ctx.Err() != nil {
		return
	}
	if _, err := c.publishFileTransferRun(fileID, run, target, file.Size, file.SHA256); err != nil {
		return
	}
}

func (c *desktopCollaboration) openDownloadTarget(transfer CollaborationFileTransfer) (*collaborationDownloadTarget, error) {
	if transfer.Automatic {
		root, absRoot, err := openRoomWorkspaceRoot(c.ownerWorkspaceRoot)
		if err != nil {
			return nil, err
		}
		destRel, err := validateRoomAttachmentRel(transfer.WorkspacePath)
		if err != nil {
			root.Close()
			return nil, err
		}
		if err := ensureRoomAttachmentDirs(root, filepath.Dir(destRel)); err != nil {
			root.Close()
			return nil, err
		}
		destPath := filepath.Join(absRoot, destRel)
		if strings.TrimSpace(transfer.Destination) == "" || !sameDesktopPath(destPath, transfer.Destination) {
			root.Close()
			return nil, fmt.Errorf("auto-receive destination no longer matches its workspace")
		}
		partPath := strings.TrimSpace(transfer.PartPath)
		if partPath == "" {
			partPath = destPath + ".wg2part"
		}
		partRel, err := filepath.Rel(absRoot, filepath.Clean(partPath))
		if err != nil || filepath.Dir(partRel) != filepath.Dir(destRel) || !strings.HasSuffix(filepath.Base(partRel), ".wg2part") {
			root.Close()
			return nil, fmt.Errorf("auto-receive partial file is outside its attachment directory")
		}
		expected := c.ownedPart(partPath)
		f, identity, err := openRootRegularFile(root, partRel, expected)
		if err != nil {
			root.Close()
			return nil, err
		}
		c.rememberOwnedPart(partPath, identity)
		return &collaborationDownloadTarget{file: f, root: root, partPath: partPath, destPath: destPath, partRel: partRel, destRel: destRel, automatic: true}, nil
	}
	partPath := filepath.Clean(strings.TrimSpace(transfer.PartPath))
	destPath := filepath.Clean(strings.TrimSpace(transfer.Destination))
	if partPath == "." || destPath == "." || partPath == "" || destPath == "" {
		return nil, fmt.Errorf("file transfer destination is unavailable")
	}
	expected := c.ownedPart(partPath)
	f, identity, err := openPathRegularFile(partPath, expected)
	if err != nil {
		return nil, err
	}
	c.rememberOwnedPart(partPath, identity)
	return &collaborationDownloadTarget{file: f, partPath: partPath, destPath: destPath}, nil
}

func openRootRegularFile(root *os.Root, name string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	flags := os.O_RDWR
	if expected == nil {
		flags |= os.O_CREATE | os.O_EXCL
	} else if linked, err := root.Lstat(name); err != nil || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(expected, linked) {
		return nil, nil, fmt.Errorf("partial file identity changed")
	}
	f, err := root.OpenFile(name, flags, 0o600)
	if err != nil {
		return nil, nil, err
	}
	opened, openErr := f.Stat()
	linked, linkErr := root.Lstat(name)
	if openErr != nil || linkErr != nil || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) || expected != nil && !os.SameFile(expected, opened) {
		f.Close()
		return nil, nil, fmt.Errorf("partial file changed while opening")
	}
	return f, opened, nil
}

func openPathRegularFile(path string, expected os.FileInfo) (*os.File, os.FileInfo, error) {
	flags := os.O_RDWR
	if expected == nil {
		flags |= os.O_CREATE | os.O_EXCL
	} else if linked, err := os.Lstat(path); err != nil || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(expected, linked) {
		return nil, nil, fmt.Errorf("partial file identity changed")
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, nil, err
	}
	opened, openErr := f.Stat()
	linked, linkErr := os.Lstat(path)
	if openErr != nil || linkErr != nil || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) || expected != nil && !os.SameFile(expected, opened) {
		f.Close()
		return nil, nil, fmt.Errorf("partial file changed while opening")
	}
	return f, opened, nil
}

func (target *collaborationDownloadTarget) verifyPart() error {
	if target.file == nil {
		return fmt.Errorf("partial file is closed")
	}
	opened, err := target.file.Stat()
	if err != nil {
		return err
	}
	var linked os.FileInfo
	if target.root != nil {
		linked, err = target.root.Lstat(target.partRel)
	} else {
		linked, err = os.Lstat(target.partPath)
	}
	if err != nil || linked.Mode()&os.ModeSymlink != 0 || !linked.Mode().IsRegular() || !os.SameFile(opened, linked) {
		return fmt.Errorf("partial file changed while downloading")
	}
	return nil
}

func (target *collaborationDownloadTarget) closeFile() error {
	if target.file == nil {
		return nil
	}
	err := target.file.Close()
	target.file = nil
	return err
}

func (target *collaborationDownloadTarget) close() {
	_ = target.closeFile()
	if target.root != nil {
		_ = target.root.Close()
		target.root = nil
	}
}

func (target *collaborationDownloadTarget) publish(size int64, expectedSHA256 string) error {
	exists, matches, err := target.destinationMatches(size, expectedSHA256)
	if err != nil {
		return err
	}
	if exists {
		if !matches {
			return fmt.Errorf("%w: 目标文件已存在且内容不同，拒绝覆盖", errCollaborationDestinationConflict)
		}
		target.removePart()
		return nil
	}
	if target.root != nil {
		err = target.root.Link(target.partRel, target.destRel)
	} else {
		err = os.Link(target.partPath, target.destPath)
	}
	if err != nil {
		// A competing publisher may have won after the first check. Accept it
		// only when it is exactly the expected file.
		exists, matches, checkErr := target.destinationMatches(size, expectedSHA256)
		if checkErr == nil && exists && matches {
			target.removePart()
			return nil
		}
		if checkErr == nil && exists {
			return fmt.Errorf("%w: 目标文件已存在且内容不同，拒绝覆盖", errCollaborationDestinationConflict)
		}
		return fmt.Errorf("publish received file without overwrite: %w", err)
	}
	target.removePart()
	return nil
}

func (target *collaborationDownloadTarget) destinationMatches(size int64, expectedSHA256 string) (bool, bool, error) {
	if target.root != nil {
		return regularFileMatchesRoot(target.root, target.destRel, size, expectedSHA256)
	}
	return regularFileMatchesPath(target.destPath, size, expectedSHA256)
}

func (target *collaborationDownloadTarget) removePart() {
	if target.root != nil {
		_ = target.root.Remove(target.partRel)
		return
	}
	_ = os.Remove(target.partPath)
}

func regularFileMatchesRoot(root *os.Root, name string, size int64, expectedSHA256 string) (bool, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, false, fmt.Errorf("destination must be a regular non-symlink file")
	}
	f, err := root.Open(name)
	if err != nil {
		return true, false, err
	}
	defer f.Close()
	return openedFileMatches(f, info, size, expectedSHA256)
}

func regularFilePresentRoot(root *os.Root, name string, size int64) (bool, bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, false, fmt.Errorf("destination must be a regular non-symlink file")
	}
	return true, info.Size() == size, nil
}

func regularFileMatchesRootCached(root *os.Root, name string, size int64, expectedSHA256 string, cached *collaborationVerifiedFile) (bool, bool, *collaborationVerifiedFile, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil, nil
	}
	if err != nil {
		return false, false, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, false, nil, fmt.Errorf("destination must be a regular non-symlink file")
	}
	if cached != nil && cached.Path == name && strings.EqualFold(cached.SHA256, expectedSHA256) && cached.Info != nil &&
		os.SameFile(cached.Info, info) && cached.Info.Size() == info.Size() && cached.Info.ModTime().Equal(info.ModTime()) {
		return true, cached.Matches, cached, nil
	}
	f, err := root.Open(name)
	if err != nil {
		return true, false, nil, err
	}
	matches, stable, err := openedFileMatches(f, info, size, expectedSHA256)
	closeErr := f.Close()
	if err != nil {
		return true, false, nil, err
	}
	if closeErr != nil {
		return true, false, nil, closeErr
	}
	pathInfo, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, pathInfo) || pathInfo.Size() != info.Size() || !pathInfo.ModTime().Equal(info.ModTime()) {
		return true, false, nil, fmt.Errorf("destination changed while verifying")
	}
	verified := &collaborationVerifiedFile{Path: name, SHA256: expectedSHA256, Info: pathInfo, Matches: matches && stable}
	return true, verified.Matches, verified, nil
}

func regularFileMatchesPath(path string, size int64, expectedSHA256 string) (bool, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return true, false, fmt.Errorf("destination must be a regular non-symlink file")
	}
	f, err := os.Open(path)
	if err != nil {
		return true, false, err
	}
	defer f.Close()
	return openedFileMatches(f, info, size, expectedSHA256)
}

func openedFileMatches(f *os.File, expectedInfo os.FileInfo, size int64, expectedSHA256 string) (bool, bool, error) {
	opened, err := f.Stat()
	if err != nil || !os.SameFile(expectedInfo, opened) {
		return true, false, fmt.Errorf("destination changed while opening")
	}
	if opened.Size() != size {
		return true, false, nil
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(f, size+1))
	if err != nil {
		return true, false, err
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(opened, after) || after.Size() != size {
		return true, false, fmt.Errorf("destination changed while reading")
	}
	return true, written == size && hex.EncodeToString(hash.Sum(nil)) == expectedSHA256, nil
}

func (p *httpCollaborationPeer) fileTicket(ctx context.Context, fileID string) (collab.FileTransferTicket, error) {
	var ticket collab.FileTransferTicket
	path := p.roomPath("files/" + url.PathEscape(fileID) + "/ticket")
	err := p.doJSON(ctx, http.MethodGet, path, nil, &ticket, true)
	if err == nil && p.protocolVersion >= collaborationProtocolV2 {
		ticket.ProxyPath = p.roomPath("files/" + url.PathEscape(fileID))
	}
	return ticket, err
}

func (p *httpCollaborationPeer) fetchFileManifest(ctx context.Context, fileID string, limit int64, allowDirect bool) (collab.FileTransferTicket, collaborationFileManifest, error) {
	ticket, err := p.fileTicket(ctx, fileID)
	if err != nil {
		return ticket, collaborationFileManifest{}, err
	}
	fetchTicket := ticket
	if !allowDirect {
		ticket.DirectURLs = nil
		fetchTicket = ticket
	}
	data, err := p.fetchFileData(ctx, fetchTicket, "/manifest", limit)
	if err != nil {
		return ticket, collaborationFileManifest{}, err
	}
	var manifest collaborationFileManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ticket, collaborationFileManifest{}, fmt.Errorf("decode file manifest: %w", err)
	}
	return ticket, manifest, nil
}

func (p *httpCollaborationPeer) RegisterFileOrigin(ctx context.Context, fileID string, input collab.RegisterFileOriginInput) error {
	path := p.roomPath("files/" + url.PathEscape(fileID) + "/origin")
	var result map[string]any
	return p.doJSON(ctx, http.MethodPost, path, input, &result, true)
}

func (p *httpCollaborationPeer) fetchFileChunk(ctx context.Context, ticket collab.FileTransferTicket, index int) ([]byte, error) {
	return p.fetchFileData(ctx, ticket, "/chunks/"+strconv.Itoa(index), ticket.File.ChunkSize+1)
}

func (p *httpCollaborationPeer) fetchFileData(ctx context.Context, ticket collab.FileTransferTicket, suffix string, limit int64) ([]byte, error) {
	if limit <= 0 || limit > 32<<20 {
		return nil, fmt.Errorf("invalid file read limit")
	}
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("file transfer redirects are disabled") }}
	type fileSource struct {
		base   string
		direct bool
	}
	candidates := make([]fileSource, 0, len(ticket.DirectURLs)+1)
	for _, candidate := range ticket.DirectURLs {
		if safeCollaborationDirectURL(candidate) {
			candidates = append(candidates, fileSource{base: candidate, direct: true})
		}
	}
	candidates = append(candidates, fileSource{base: p.baseURL + ticket.ProxyPath})
	var lastErr error
	for _, source := range candidates {
		endpoint := source.base + suffix
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if source.direct {
			query := req.URL.Query()
			query.Set("ticket", ticket.Ticket)
			req.URL.RawQuery = query.Encode()
		} else {
			req.Header.Set("Authorization", "Bearer "+p.session)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		readLimit := limit
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			readLimit = 4097
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, readLimit))
		_ = resp.Body.Close()
		if readErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && int64(len(data)) < limit {
			return data, nil
		}
		if readErr != nil {
			lastErr = readErr
		} else {
			body := strings.TrimSpace(string(data))
			if len(data) > 4096 {
				body = strings.TrimSpace(string(data[:4096])) + "…(truncated)"
			}
			lastErr = fmt.Errorf("file source returned %s: %s", resp.Status, body)
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("file owner is unavailable")
	}
	return nil, lastErr
}

func safeCollaborationDirectURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	host := strings.Trim(strings.ToLower(parsed.Hostname()), "[]")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()) {
		return false
	}
	return true
}

func (c *desktopCollaboration) pauseFile(fileID string) (CollaborationFileTransfer, error) {
	c.mu.Lock()
	transfer := c.transfers[fileID]
	if transfer == nil {
		c.mu.Unlock()
		return CollaborationFileTransfer{}, fmt.Errorf("file transfer does not exist")
	}
	c.cancelFileTransferLocked(fileID)
	if transfer.Status != "completed" {
		transfer.Status, transfer.Error, transfer.Retryable, transfer.PausedByUser = "paused", "", true, true
	}
	c.persistLocked()
	view := cloneCollaborationTransfer(*transfer)
	c.mu.Unlock()
	c.emitState()
	return view, nil
}

func (c *desktopCollaboration) resumeFile(fileID string) (CollaborationFileTransfer, error) {
	c.mu.Lock()
	transfer := c.transfers[fileID]
	if transfer == nil {
		c.mu.Unlock()
		return CollaborationFileTransfer{}, fmt.Errorf("file transfer does not exist")
	}
	if transfer.Status == "completed" {
		view := cloneCollaborationTransfer(*transfer)
		c.mu.Unlock()
		return view, nil
	}
	offer, found := c.fileOfferLocked(fileID)
	if !found || offer.RevokedAt != nil || !transferMatchesOffer(transfer, c.state.Room, c.roomInstance, offer) {
		c.mu.Unlock()
		return CollaborationFileTransfer{}, fmt.Errorf("file offer is unavailable or changed")
	}
	transfer.Status, transfer.Error, transfer.Retryable, transfer.PausedByUser, transfer.AutoBlocked, transfer.AutoAttempts = "negotiating", "", false, false, false, 0
	delete(c.autoRetryAfter, fileID)
	c.persistLocked()
	view := cloneCollaborationTransfer(*transfer)
	c.mu.Unlock()
	c.emitState()
	c.startFileDownload(fileID)
	return view, nil
}

func (c *desktopCollaboration) revokeFile(ctx context.Context, fileID string) (CollaborationActionResult, error) {
	result, err := c.submit(ctx, fileID+":revoke", collab.Command{Type: collab.CommandRevokeFile, FileRevoke: &collab.RevokeFileInput{FileID: fileID}})
	if err != nil {
		return result, err
	}
	c.setShareStatus(fileID, "revoked", "")
	c.emitState()
	return result, nil
}

// roomAttachmentsRelPath is the workspace-relative directory where auto-received
// Room files land so they stay inside the Session workspace and can be
// @-referenced by the Agent.
const roomAttachmentsRelPath = ".workground2/attachments/room"

const maxRoomAttachmentNameBytes = 180

// sanitizeRoomAttachmentName returns a safe base name for auto-received files.
// It rejects empty names and path separators. Whitespace, control characters,
// and platform-reserved punctuation are replaced so the resulting @reference
// is a single token on every platform.
func sanitizeRoomAttachmentName(name string) (string, error) {
	clean := strings.TrimSpace(name)
	if clean == "" || clean == "." || clean == ".." {
		return "", fmt.Errorf("auto-receive: empty or reserved file name")
	}
	if strings.ContainsRune(clean, 0) || strings.ContainsAny(clean, "/\\") {
		return "", fmt.Errorf("auto-receive: file name contains path separators or null bytes")
	}
	var builder strings.Builder
	for _, r := range clean {
		if r == utf8.RuneError || unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("<>:\"|?*", r) {
			r = '_'
		}
		builder.WriteRune(r)
	}
	result := strings.Trim(builder.String(), " .")
	if result == "" || result == "." || result == ".." {
		return "", fmt.Errorf("auto-receive: empty or reserved file name")
	}
	result = trimUTF8Bytes(result, maxRoomAttachmentNameBytes)
	runes := []rune(result)
	for index := len(runes) - 1; index >= 0 && strings.ContainsRune(".,;!?)]}'\"", runes[index]); index-- {
		runes[index] = '_'
	}
	result = string(runes)
	return result, nil
}

func trimUTF8Bytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for len(value) > limit {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			return ""
		}
		value = value[:len(value)-size]
	}
	return value
}

func shortCollaborationHash(value string, bytes int) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:bytes])
}

// roomAttachmentDestination returns a deterministic, workspace-confined path.
// Remote identifiers are represented only by fixed-length local hashes.
func roomAttachmentDestination(workspaceRoot, roomID, fileID, safeName, expectedSHA256 string) (string, string, error) {
	if strings.TrimSpace(roomID) == "" || strings.TrimSpace(fileID) == "" || strings.TrimSpace(expectedSHA256) == "" {
		return "", "", fmt.Errorf("%w: room and file identity are required", errCollaborationAttachmentPolicy)
	}
	root, absRoot, err := openRoomWorkspaceRoot(workspaceRoot)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	roomDir := filepath.Join(filepath.FromSlash(roomAttachmentsRelPath), shortCollaborationHash(roomID, 6))
	if err := ensureRoomAttachmentDirs(root, roomDir); err != nil {
		return "", "", err
	}
	name := shortCollaborationHash(roomID+"\x00"+fileID+"\x00"+expectedSHA256, 8) + "-" + safeName
	rel := filepath.Join(roomDir, name)
	rel, err = validateRoomAttachmentRel(rel)
	if err != nil {
		return "", "", err
	}
	if info, statErr := root.Lstat(rel); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", "", fmt.Errorf("%w: destination must be a regular non-symlink file", errCollaborationAttachmentPolicy)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", "", fmt.Errorf("auto-receive: inspect destination: %w", statErr)
	}
	return filepath.Join(absRoot, rel), filepath.ToSlash(rel), nil
}

func openRoomWorkspaceRoot(workspaceRoot string) (*os.Root, string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return nil, "", fmt.Errorf("auto-receive workspace is unavailable")
	}
	absRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return nil, "", fmt.Errorf("auto-receive: resolve workspace: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		return nil, "", fmt.Errorf("auto-receive workspace is not an available directory")
	}
	root, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, "", fmt.Errorf("auto-receive: open workspace: %w", err)
	}
	return root, absRoot, nil
}

func ensureRoomAttachmentDirs(root *os.Root, relDir string) error {
	relDir = filepath.Clean(relDir)
	if filepath.IsAbs(relDir) || relDir == "." || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: room attachments directory is outside workspace", errCollaborationAttachmentPolicy)
	}
	current := ""
	for _, part := range strings.Split(relDir, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("%w: room attachments directory is invalid", errCollaborationAttachmentPolicy)
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create room attachments directory: %w", err)
			}
			info, err = root.Lstat(current)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("%w: room attachments directory must not contain symlinks", errCollaborationAttachmentPolicy)
		}
	}
	return nil
}

func checkRoomAttachmentDirs(root *os.Root, relDir string) error {
	relDir = filepath.Clean(relDir)
	if filepath.IsAbs(relDir) || relDir == "." || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) {
		return fmt.Errorf("room attachments directory is outside workspace")
	}
	current := ""
	for _, part := range strings.Split(relDir, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("room attachments directory is invalid")
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("room attachments directory must not contain symlinks")
		}
	}
	return nil
}

func validateRoomAttachmentRel(value string) (string, error) {
	value = filepath.Clean(filepath.FromSlash(strings.TrimSpace(value)))
	prefix := filepath.Clean(filepath.FromSlash(roomAttachmentsRelPath)) + string(filepath.Separator)
	if value == "." || filepath.IsAbs(value) || !strings.HasPrefix(value, prefix) || strings.Contains(value, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: room attachment path is outside its workspace directory", errCollaborationAttachmentPolicy)
	}
	return value, nil
}

func newRoomPartPath(destination string) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("create partial file identity: %w", err)
	}
	return destination + "." + hex.EncodeToString(nonce[:]) + ".wg2part", nil
}

func (c *desktopCollaboration) prepareFileTransferPart(fileID string, run uint64) *CollaborationFileTransfer {
	c.mu.Lock()
	defer c.mu.Unlock()
	transfer := c.transfers[fileID]
	if !c.fileTransferRunCurrentLocked(fileID, run) || transfer == nil {
		return nil
	}
	if transfer.PartPath != "" && c.ownedParts[transfer.PartPath] != nil {
		return cloneCollaborationTransferPtr(transfer)
	}
	partPath, err := newRoomPartPath(transfer.Destination)
	if err != nil {
		transfer.Status, transfer.Error, transfer.Retryable = "failed", err.Error(), true
		c.persistLocked()
		return nil
	}
	transfer.PartPath = partPath
	for index := range transfer.Completed {
		transfer.Completed[index] = false
	}
	transfer.Transferred = 0
	c.persistLocked()
	return cloneCollaborationTransferPtr(transfer)
}

func (c *desktopCollaboration) ownedPart(path string) os.FileInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ownedParts[path]
}

func (c *desktopCollaboration) rememberOwnedPart(path string, identity os.FileInfo) {
	if identity == nil {
		return
	}
	c.mu.Lock()
	if c.ownedParts == nil {
		c.ownedParts = map[string]os.FileInfo{}
	}
	c.ownedParts[path] = identity
	c.mu.Unlock()
}

const collaborationAutoReceiveNotice = "Room 自动接收："

// signalAutoReceiveFiles coalesces arbitrary Snapshot/retry signals into one
// scanner. This prevents an event burst from creating an unbounded goroutine
// backlog while still guaranteeing one final scan after a concurrent update.
func (c *desktopCollaboration) signalAutoReceiveFiles() {
	c.mu.Lock()
	if c.autoScanClosed {
		c.mu.Unlock()
		return
	}
	if c.autoScanActive {
		c.autoScanAgain = true
		c.mu.Unlock()
		return
	}
	c.autoScanActive = true
	c.mu.Unlock()
	go func() {
		for {
			c.maybeAutoReceiveFiles()
			c.mu.Lock()
			if c.autoScanAgain && !c.autoScanClosed {
				c.autoScanAgain = false
				c.mu.Unlock()
				continue
			}
			c.autoScanActive = false
			c.mu.Unlock()
			return
		}
	}()
}

func (c *desktopCollaboration) scheduleAutoReceiveRetry(at time.Time) {
	if at.IsZero() {
		return
	}
	c.mu.Lock()
	if c.autoScanClosed || c.autoRetryTimer != nil && !at.Before(c.autoRetryAt) {
		c.mu.Unlock()
		return
	}
	if c.autoRetryTimer != nil {
		c.autoRetryTimer.Stop()
	}
	c.autoRetryAt = at
	delay := max(time.Until(at), time.Millisecond)
	c.autoRetryTimer = time.AfterFunc(delay, func() {
		c.mu.Lock()
		if !c.autoRetryAt.Equal(at) || c.autoScanClosed {
			c.mu.Unlock()
			return
		}
		c.autoRetryTimer = nil
		c.autoRetryAt = time.Time{}
		c.mu.Unlock()
		c.signalAutoReceiveFiles()
	})
	c.mu.Unlock()
}

func (c *desktopCollaboration) deferAutomaticFileRetry(fileID string, run uint64, retryDelay func(int) time.Duration) {
	c.mu.Lock()
	transfer := c.transfers[fileID]
	if transfer == nil || !transfer.Automatic || transfer.PausedByUser || transfer.AutoBlocked || !transfer.Retryable || !c.fileTransferRunCurrentLocked(fileID, run) {
		c.mu.Unlock()
		return
	}
	transfer.AutoAttempts++
	retryWait := autoReceiveRetryDelay(transfer.AutoAttempts)
	if transfer.AutoAttempts >= collaborationAutoReceiveAttempts {
		retryWait = collaborationAutoReceiveRetryWait
	}
	if retryDelay != nil {
		retryWait = max(retryDelay(transfer.AutoAttempts), time.Millisecond)
	}
	retryAt := time.Now().Add(retryWait)
	if c.autoRetryAfter == nil {
		c.autoRetryAfter = map[string]time.Time{}
	}
	c.autoRetryAfter[fileID] = retryAt
	transfer.Status = "waiting_sender"
	if transfer.AutoAttempts >= collaborationAutoReceiveAttempts {
		transfer.Error = "发送者持续不可用，稍后将重新尝试"
	} else {
		transfer.Error = "发送者暂不可用，将自动重试"
	}
	c.persistLocked()
	c.mu.Unlock()
	c.emitState()
	c.scheduleAutoReceiveRetry(retryAt)
}

// maybeAutoReceiveFiles reconciles the complete authoritative Snapshot. The
// scan is serialized, validates all remote dimensions before allocation, caps
// automatic disk use, and reserves at most two active workers.
func (c *desktopCollaboration) maybeAutoReceiveFiles() {
	c.autoReceiveMu.Lock()
	defer c.autoReceiveMu.Unlock()

	c.mu.RLock()
	workspaceRoot := c.ownerWorkspaceRoot
	memberID, room, roomInstance := c.state.MemberID, c.state.Room, c.roomInstance
	conn := c.conn
	snapshot := c.state.Snapshot
	c.mu.RUnlock()
	if workspaceRoot == "" && room != "" && conn != nil {
		notice := collaborationAutoReceiveNotice + "当前 Session 没有 workspace，请手动接收文件"
		changed := false
		c.mu.Lock()
		if c.conn == conn && (c.state.LastError == "" || strings.HasPrefix(c.state.LastError, collaborationAutoReceiveNotice)) && (c.state.LastError != notice || c.state.Retryable) {
			c.state.LastError, c.state.Retryable = notice, false
			c.persistLocked()
			changed = true
		}
		c.mu.Unlock()
		if changed {
			c.emitState()
		}
		return
	}
	if room == "" || roomInstance == "" || conn == nil || conn.filePeer == nil {
		return
	}

	allOffers := make(map[string]collab.FileOffer)
	eligible := make([]collab.FileOffer, 0)
	eligibleByID := make(map[string]collab.FileOffer)
	seen := make(map[string]struct{})
	invalidOffer, quotaExceeded := false, false
	reserved := make(map[string]struct{})
	quotaReservations := make(map[string]struct{})
	var quotaBytes int64
	c.mu.RLock()
	countTransfer := func(fileID string, transfer *CollaborationFileTransfer) {
		if transfer == nil || !transfer.Automatic {
			return
		}
		quotaKey := collaborationTransferArchiveKey(transfer.RoomInstance, transfer.FileID) + "\x00" + strings.ToLower(transfer.SHA256)
		if _, counted := quotaReservations[quotaKey]; !counted {
			quotaReservations[quotaKey] = struct{}{}
			if transfer.Total > 0 && transfer.Total < collaborationAutoReceiveLimit {
				quotaBytes += transfer.Total
			}
		}
		if transfer.Room == room && transfer.RoomInstance == roomInstance {
			reserved[fileID] = struct{}{}
		}
	}
	for fileID, transfer := range c.transfers {
		countTransfer(fileID, transfer)
	}
	for _, transfer := range c.transferArchive {
		if transfer != nil {
			countTransfer(transfer.FileID, transfer)
		}
	}
	c.mu.RUnlock()
	quotaFiles := len(quotaReservations)
	for _, item := range snapshot.Timeline {
		if item.File == nil {
			continue
		}
		offer := *item.File
		if item.ID != offer.ID {
			invalidOffer = true
			continue
		}
		if _, duplicate := seen[offer.ID]; duplicate {
			invalidOffer = true
			delete(allOffers, offer.ID)
			delete(eligibleByID, offer.ID)
			continue
		}
		seen[offer.ID] = struct{}{}
		allOffers[offer.ID] = offer
		if offer.OwnerID == memberID || offer.RevokedAt != nil || offer.Size >= collaborationAutoReceiveLimit {
			continue
		}
		if err := validateCollaborationFileOffer(offer, collaborationAutoReceiveLimit-1); err != nil {
			invalidOffer = true
			continue
		}
		if _, alreadyReserved := reserved[offer.ID]; !alreadyReserved {
			if quotaFiles >= collaborationAutoReceiveMaxFiles || quotaBytes+offer.Size > collaborationAutoReceiveMaxBytes {
				quotaExceeded = true
				continue
			}
			reserved[offer.ID] = struct{}{}
			quotaFiles++
			quotaBytes += offer.Size
		}
		eligible = append(eligible, offer)
		eligibleByID[offer.ID] = offer
	}

	changed := false
	c.mu.Lock()
	if c.autoScanClosed || c.conn != conn || c.state.Room != room {
		c.mu.Unlock()
		return
	}
	if c.autoReceiveSem == nil {
		c.autoReceiveSem = make(chan struct{}, 2)
	}
	for fileID, transfer := range c.transfers {
		if transfer == nil || !transfer.Automatic || transfer.Room != room || transfer.RoomInstance != roomInstance {
			continue
		}
		offer, allowed := eligibleByID[fileID]
		if allowed && transferMatchesOffer(transfer, room, roomInstance, offer) {
			continue
		}
		before := cloneCollaborationTransfer(*transfer)
		if c.transferCancel[fileID] != nil {
			c.cancelFileTransferLocked(fileID)
		}
		delete(c.autoRetryAfter, fileID)
		delete(c.verifiedFiles, fileID)
		current, found := allOffers[fileID]
		switch {
		case found && current.RevokedAt != nil:
			transfer.Status, transfer.Error, transfer.Retryable, transfer.AutoBlocked = "revoked", "文件分享已撤销", false, true
		case found && !allowed:
			transfer.Status, transfer.Error, transfer.Retryable, transfer.AutoBlocked = "failed", "文件声明无效或已超出自动接收配额", true, true
		default:
			transfer.Status, transfer.Error, transfer.Retryable, transfer.AutoBlocked = "failed", "文件分享已移除或发生变化", false, true
		}
		if !equalCollaborationFileTransfer(before, *transfer) {
			changed = true
		}
	}
	active := 0
	for fileID := range c.transferCancel {
		if transfer := c.transfers[fileID]; transfer != nil && transfer.Automatic && transfer.Room == room && transfer.RoomInstance == roomInstance {
			active++
		}
	}
	startBudget := max(cap(c.autoReceiveSem)-active, 0)
	if changed {
		c.persistLocked()
	}
	c.mu.Unlock()

	starts := make([]string, 0, startBudget)
	var nextRetry time.Time
	for _, offer := range eligible {
		safeName, prepErr := sanitizeRoomAttachmentName(offer.Name)
		prepBlocked := prepErr != nil
		var destination, workspacePath string
		if prepErr == nil {
			destination, workspacePath, prepErr = roomAttachmentDestination(workspaceRoot, roomInstance, offer.ID, safeName, offer.SHA256)
			prepBlocked = errors.Is(prepErr, errCollaborationAttachmentPolicy)
		}

		c.mu.RLock()
		oldView := cloneCollaborationTransferPtr(c.transfers[offer.ID])
		cachedVerification, hasCachedVerification := c.verifiedFiles[offer.ID]
		c.mu.RUnlock()
		sameCompleted := oldView != nil && oldView.Automatic && oldView.Status == "completed" && transferMatchesOffer(oldView, room, roomInstance, offer) && sameDesktopPath(oldView.Destination, destination) && oldView.WorkspacePath == workspacePath
		exists, matches := false, false
		var verified *collaborationVerifiedFile
		if prepErr == nil {
			root, _, rootErr := openRoomWorkspaceRoot(workspaceRoot)
			if rootErr != nil {
				prepErr = rootErr
			} else if sameCompleted {
				var cached *collaborationVerifiedFile
				if hasCachedVerification {
					cached = &cachedVerification
				}
				exists, matches, verified, prepErr = regularFileMatchesRootCached(root, filepath.FromSlash(workspacePath), offer.Size, offer.SHA256, cached)
				root.Close()
			} else {
				exists, matches, prepErr = regularFileMatchesRoot(root, filepath.FromSlash(workspacePath), offer.Size, offer.SHA256)
				root.Close()
			}
		}

		shouldStart := false
		c.mu.Lock()
		current, currentFound := c.fileOfferLocked(offer.ID)
		_, stillAllowed := eligibleByID[offer.ID]
		if c.autoScanClosed || c.conn != conn || c.state.Room != room || !currentFound || !stillAllowed || current.RevokedAt != nil || !fileOfferIdentityEqual(current, offer) {
			c.mu.Unlock()
			continue
		}
		old := c.transfers[offer.ID]
		if oldView == nil && old != nil || oldView != nil && (old == nil || !equalCollaborationFileTransfer(*oldView, *old)) {
			c.mu.Unlock()
			continue
		}
		same := old != nil && old.Automatic && transferMatchesOffer(old, room, roomInstance, offer)
		before := cloneCollaborationTransferPtr(old)
		if !same {
			delete(c.verifiedFiles, offer.ID)
			if c.transferCancel[offer.ID] != nil {
				c.cancelFileTransferLocked(offer.ID)
			}
			old = &CollaborationFileTransfer{
				ID: stableCollaborationID("transfer", roomInstance+"\x00"+offer.ID+"\x00"+memberID), FileID: offer.ID,
				Room: room, RoomInstance: roomInstance, OwnerID: offer.OwnerID, SHA256: offer.SHA256, ManifestHash: offer.ManifestHash,
				ChunkSize: offer.ChunkSize, ChunkCount: offer.ChunkCount, OfferRevision: offer.Revision,
				Direction: "receive", Name: offer.Name, Total: offer.Size, Destination: destination,
				WorkspacePath: workspacePath, Completed: make([]bool, offer.ChunkCount), Automatic: true,
			}
			c.transfers[offer.ID] = old
		} else {
			old.Name, old.Destination, old.WorkspacePath = offer.Name, destination, workspacePath
		}
		if sameCompleted {
			if verified != nil {
				if c.verifiedFiles == nil {
					c.verifiedFiles = map[string]collaborationVerifiedFile{}
				}
				c.verifiedFiles[offer.ID] = *verified
			} else if prepErr != nil || !exists || !matches {
				delete(c.verifiedFiles, offer.ID)
			}
		}
		retryAt := c.autoRetryAfter[offer.ID]
		switch {
		case old.PausedByUser:
			old.Status, old.Error, old.Retryable = "paused", "", true
		case prepErr != nil && prepBlocked:
			if c.transferCancel[offer.ID] != nil {
				c.cancelFileTransferLocked(offer.ID)
			}
			delete(c.autoRetryAfter, offer.ID)
			old.Status, old.Error, old.Retryable, old.AutoBlocked = "failed", prepErr.Error(), true, true
		case prepErr != nil:
			if c.transferCancel[offer.ID] != nil {
				c.cancelFileTransferLocked(offer.ID)
			}
			if retryAt.IsZero() || !time.Now().Before(retryAt) {
				retryWait := collaborationAutoReceiveRetryWait
				if c.autoRetryDelay != nil {
					retryWait = max(c.autoRetryDelay(1), time.Millisecond)
				}
				retryAt = time.Now().Add(retryWait)
				if c.autoRetryAfter == nil {
					c.autoRetryAfter = map[string]time.Time{}
				}
				c.autoRetryAfter[offer.ID] = retryAt
			}
			old.Status, old.Error, old.Retryable, old.AutoBlocked = "waiting_sender", "自动接收目标暂不可用: "+prepErr.Error(), true, false
			if nextRetry.IsZero() || retryAt.Before(nextRetry) {
				nextRetry = retryAt
			}
		case exists && matches:
			if c.transferCancel[offer.ID] != nil {
				c.cancelFileTransferLocked(offer.ID)
			}
			delete(c.autoRetryAfter, offer.ID)
			old.Status, old.Error, old.Retryable, old.Transferred, old.AutoBlocked, old.AutoAttempts = "completed", "", false, offer.Size, false, 0
			for index := range old.Completed {
				old.Completed[index] = true
			}
		case exists:
			if c.transferCancel[offer.ID] != nil {
				c.cancelFileTransferLocked(offer.ID)
			}
			old.Status, old.Error, old.Retryable, old.AutoBlocked = "failed", "目标文件已存在且内容不同，拒绝覆盖", true, true
		case c.transferCancel[offer.ID] != nil:
			// An active automatic attempt already owns this file.
		case old.AutoBlocked:
			// Explicit resume clears this safety latch.
		case !retryAt.IsZero() && time.Now().Before(retryAt):
			old.Status, old.Retryable = "waiting_sender", true
			if nextRetry.IsZero() || retryAt.Before(nextRetry) {
				nextRetry = retryAt
			}
		case startBudget == 0:
			old.Status, old.Error, old.Retryable = "pending", "等待自动接收队列", false
		default:
			delete(c.autoRetryAfter, offer.ID)
			if old.AutoAttempts >= collaborationAutoReceiveAttempts {
				old.AutoAttempts = 0
			}
			if old.Status == "completed" {
				for index := range old.Completed {
					old.Completed[index] = false
				}
				old.Transferred = 0
			}
			old.Status, old.Error, old.Retryable, old.AutoBlocked = "negotiating", "", false, false
			shouldStart = true
			startBudget--
		}
		if before == nil || !equalCollaborationFileTransfer(*before, *old) {
			c.persistLocked()
			changed = true
		}
		c.mu.Unlock()
		if shouldStart {
			starts = append(starts, offer.ID)
		}
	}

	notice := ""
	if invalidOffer {
		notice = collaborationAutoReceiveNotice + "已拒绝不安全的文件声明"
	} else if quotaExceeded {
		notice = collaborationAutoReceiveNotice + "已达到 1024 个文件或 256 MiB 的安全配额，剩余文件可手动接收"
	}
	c.mu.Lock()
	if c.autoScanClosed || c.conn != conn || c.state.Room != room {
		c.mu.Unlock()
		return
	}
	if notice != "" && (c.state.LastError == "" || strings.HasPrefix(c.state.LastError, collaborationAutoReceiveNotice)) {
		if c.state.LastError != notice || !c.state.Retryable {
			c.state.LastError, c.state.Retryable = notice, true
			c.persistLocked()
			changed = true
		}
	} else if notice == "" && strings.HasPrefix(c.state.LastError, collaborationAutoReceiveNotice) {
		c.state.LastError, c.state.Retryable = "", false
		c.persistLocked()
		changed = true
	}
	c.mu.Unlock()
	if changed {
		c.emitState()
	}
	if !nextRetry.IsZero() {
		c.scheduleAutoReceiveRetry(nextRetry)
	}
	for _, fileID := range starts {
		c.startFileDownload(fileID)
	}
}

// roomAttachmentRefs returns verified workspace-relative @-references only for
// the file IDs selected as Agent context.
func (c *desktopCollaboration) roomAttachmentRefs(referenceIDs []string) map[string]string {
	wanted := make(map[string]struct{}, len(referenceIDs))
	for _, fileID := range referenceIDs {
		if fileID = strings.TrimSpace(fileID); fileID != "" {
			wanted[fileID] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	c.mu.RLock()
	if c.ownerWorkspaceRoot == "" {
		c.mu.RUnlock()
		return nil
	}
	workspaceRoot, room, roomInstance, snapshot := c.ownerWorkspaceRoot, c.state.Room, c.roomInstance, c.state.Snapshot
	transfers := make([]CollaborationFileTransfer, 0, len(wanted))
	for fileID := range wanted {
		if transfer := c.transfers[fileID]; transfer != nil {
			transfers = append(transfers, cloneCollaborationTransfer(*transfer))
		}
	}
	c.mu.RUnlock()
	offers := make(map[string]collab.FileOffer, len(wanted))
	for _, item := range snapshot.Timeline {
		if item.File != nil {
			if _, ok := wanted[item.ID]; ok && item.ID == item.File.ID {
				offers[item.ID] = *item.File
			}
		}
	}
	refs := make(map[string]string, len(transfers))
	root, _, err := openRoomWorkspaceRoot(workspaceRoot)
	if err != nil {
		return refs
	}
	defer root.Close()
	for i := range transfers {
		t := &transfers[i]
		offer, found := offers[t.FileID]
		if !found || offer.RevokedAt != nil || !t.Automatic || t.Direction != "receive" || t.Status != "completed" || !transferMatchesOffer(t, room, roomInstance, offer) {
			continue
		}
		rel, err := validateRoomAttachmentRel(t.WorkspacePath)
		if err != nil {
			continue
		}
		if err := checkRoomAttachmentDirs(root, filepath.Dir(rel)); err != nil {
			continue
		}
		expected := filepath.Join(workspaceRoot, rel)
		if !sameDesktopPath(expected, t.Destination) {
			continue
		}
		_, matches, err := regularFileMatchesRoot(root, rel, offer.Size, offer.SHA256)
		if err != nil || !matches {
			continue
		}
		refs[t.FileID] = "@" + filepath.ToSlash(rel)
	}
	return refs
}

func (c *desktopCollaboration) setShareStatus(fileID, status, message string) {
	c.mu.Lock()
	share, ok := c.shares[fileID]
	if ok {
		share.Status, share.Error = status, message
		c.shares[fileID] = share
		c.persistLocked()
	}
	c.mu.Unlock()
}

func (c *desktopCollaboration) updateFileTransfer(fileID string, update func(*CollaborationFileTransfer)) {
	c.mu.Lock()
	if transfer := c.transfers[fileID]; transfer != nil {
		update(transfer)
		c.persistLocked()
	}
	c.mu.Unlock()
	c.emitState()
}

func (c *desktopCollaboration) cancelFileTransferLocked(fileID string) {
	if c.transferRun == nil {
		c.transferRun = map[string]uint64{}
	}
	if c.transferCancel == nil {
		c.transferCancel = map[string]context.CancelFunc{}
	}
	if cancel := c.transferCancel[fileID]; cancel != nil {
		cancel()
		delete(c.transferCancel, fileID)
	}
	c.transferRun[fileID]++
	delete(c.autoRetryAfter, fileID)
}

func (c *desktopCollaboration) fileTransferRunCurrentLocked(fileID string, run uint64) bool {
	transfer := c.transfers[fileID]
	if transfer == nil || c.transferRun[fileID] != run {
		return false
	}
	offer, found := c.fileOfferLocked(fileID)
	return found && offer.RevokedAt == nil && transferMatchesOffer(transfer, c.state.Room, c.roomInstance, offer)
}

func (c *desktopCollaboration) updateFileTransferRun(fileID string, run uint64, update func(*CollaborationFileTransfer)) bool {
	c.mu.Lock()
	transfer := c.transfers[fileID]
	if transfer == nil || !c.fileTransferRunCurrentLocked(fileID, run) {
		c.mu.Unlock()
		return false
	}
	update(transfer)
	c.persistLocked()
	c.mu.Unlock()
	c.emitState()
	return true
}

func (c *desktopCollaboration) publishFileTransferRun(fileID string, run uint64, target *collaborationDownloadTarget, size int64, expectedSHA256 string) (bool, error) {
	c.mu.Lock()
	transfer := c.transfers[fileID]
	if transfer == nil || !c.fileTransferRunCurrentLocked(fileID, run) {
		c.mu.Unlock()
		return false, nil
	}
	err := target.publish(size, expectedSHA256)
	if err != nil {
		retryable := !errors.Is(err, errCollaborationDestinationConflict)
		transfer.Status, transfer.Error, transfer.Retryable = "failed", err.Error(), retryable
		if transfer.Automatic && !retryable {
			transfer.AutoBlocked, transfer.Retryable = true, true
		}
	} else {
		transfer.Status, transfer.Error, transfer.Retryable, transfer.Transferred, transfer.AutoBlocked = "completed", "", false, transfer.Total, false
		delete(c.autoRetryAfter, fileID)
	}
	cleanupPart := err == nil || transfer.AutoBlocked
	if cleanupPart {
		delete(c.ownedParts, target.partPath)
	}
	c.persistLocked()
	c.mu.Unlock()
	if err != nil && cleanupPart {
		target.removePart()
	}
	c.emitState()
	return true, err
}

func (c *desktopCollaboration) failFileTransfer(fileID, status, message string, retryable bool) {
	c.updateFileTransfer(fileID, func(value *CollaborationFileTransfer) {
		value.Status, value.Error, value.Retryable = status, message, retryable
	})
}

func (c *desktopCollaboration) failFileTransferRun(fileID string, run uint64, status, message string, retryable bool) {
	c.updateFileTransferRun(fileID, run, func(value *CollaborationFileTransfer) {
		value.Status, value.Error, value.Retryable = status, message, retryable
		if value.Automatic && !retryable {
			value.AutoBlocked = true
			value.Retryable = true
		}
	})
}

func fileTransferFailure(err error) (string, bool) {
	if strings.Contains(strings.ToLower(err.Error()), "source_changed") {
		return "source_changed", false
	}
	return "waiting_sender", true
}

func (c *desktopCollaboration) resumeWaitingFileTransfers() {
	c.mu.RLock()
	ids := make([]string, 0)
	for id, transfer := range c.transfers {
		if transfer.Status == "waiting_sender" && !transfer.Automatic && !transfer.PausedByUser && transfer.RoomInstance == c.roomInstance {
			ids = append(ids, id)
		}
	}
	c.mu.RUnlock()
	for _, id := range ids {
		_, _ = c.resumeFile(id)
	}
}

func (c *desktopCollaboration) fileTransfersLocked() []CollaborationFileTransfer {
	result := make([]CollaborationFileTransfer, 0, len(c.transfers)+len(c.shares))
	for _, transfer := range c.transfers {
		if transfer.Room != c.state.Room || transfer.RoomInstance != c.roomInstance {
			continue
		}
		result = append(result, cloneCollaborationTransfer(*transfer))
	}
	for id := range c.shares {
		if c.shares[id].Room != c.state.Room || c.shares[id].OwnerID != c.state.MemberID {
			continue
		}
		result = append(result, c.fileTransferForShareLocked(id))
	}
	return result
}

func (c *desktopCollaboration) persistedFileTransfersLocked() []CollaborationFileTransfer {
	result := make([]CollaborationFileTransfer, 0, len(c.transfers)+len(c.transferArchive))
	for _, transfer := range c.transferArchive {
		if transfer != nil {
			result = append(result, cloneCollaborationTransfer(*transfer))
		}
	}
	for _, transfer := range c.transfers {
		if transfer != nil {
			result = append(result, cloneCollaborationTransfer(*transfer))
		}
	}
	return result
}

func (c *desktopCollaboration) fileTransferForShare(fileID string) CollaborationFileTransfer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fileTransferForShareLocked(fileID)
}

func (c *desktopCollaboration) fileTransferForShareLocked(fileID string) CollaborationFileTransfer {
	share := c.shares[fileID]
	status, message := share.Status, share.Error
	if share.ShareAuthority == "" || share.ShareAuthority != c.shareAuthority {
		status, message = "unavailable", "该文件来自之前的 Room 连接，请重新分享"
	}
	return CollaborationFileTransfer{ID: "share:" + fileID, FileID: fileID, Room: share.Room, RoomInstance: c.roomInstance, OwnerID: share.OwnerID, SHA256: share.SHA256, ManifestHash: share.ManifestHash, ChunkSize: share.ChunkSize, ChunkCount: len(share.ChunkHashes), OfferRevision: share.OfferRevision, Direction: "share", Name: share.Name, Status: status, Transferred: share.Size, Total: share.Size, Error: message, Retryable: status == "unavailable" || status == "failed"}
}

func cloneCollaborationTransfers(values []CollaborationFileTransfer) []CollaborationFileTransfer {
	result := make([]CollaborationFileTransfer, len(values))
	for i := range values {
		result[i] = cloneCollaborationTransfer(values[i])
	}
	return result
}

func cloneCollaborationTransfer(value CollaborationFileTransfer) CollaborationFileTransfer {
	value.Completed = append([]bool(nil), value.Completed...)
	return value
}

func equalCollaborationFileTransfer(a, b CollaborationFileTransfer) bool {
	completedA, completedB := a.Completed, b.Completed
	if a.ID != b.ID || a.FileID != b.FileID || a.Room != b.Room || a.RoomInstance != b.RoomInstance || a.OwnerID != b.OwnerID || a.SHA256 != b.SHA256 ||
		a.ManifestHash != b.ManifestHash || a.ChunkSize != b.ChunkSize || a.ChunkCount != b.ChunkCount || a.OfferRevision != b.OfferRevision ||
		a.Direction != b.Direction || a.Name != b.Name || a.Status != b.Status ||
		a.Transferred != b.Transferred || a.Total != b.Total || a.Destination != b.Destination ||
		a.Error != b.Error || a.Retryable != b.Retryable || a.PartPath != b.PartPath ||
		a.Automatic != b.Automatic || a.PausedByUser != b.PausedByUser || a.AutoBlocked != b.AutoBlocked || a.AutoAttempts != b.AutoAttempts ||
		a.WorkspacePath != b.WorkspacePath || len(completedA) != len(completedB) {
		return false
	}
	for index := range completedA {
		if completedA[index] != completedB[index] {
			return false
		}
	}
	return true
}

func cloneCollaborationTransferPtr(value *CollaborationFileTransfer) *CollaborationFileTransfer {
	if value == nil {
		return nil
	}
	clone := cloneCollaborationTransfer(*value)
	return &clone
}

func completedFileBytes(completed []bool, total, chunkSize int64) int64 {
	var value int64
	for index, done := range completed {
		if !done {
			continue
		}
		value += min(chunkSize, total-int64(index)*chunkSize)
	}
	return value
}

func collaborationFileOffer(snapshot collab.Snapshot, fileID string) (collab.FileOffer, bool) {
	for _, item := range snapshot.Timeline {
		if item.ID == fileID && item.File != nil && item.File.ID == fileID {
			return *item.File, true
		}
	}
	return collab.FileOffer{}, false
}

func collaborationFileOfferIndex(snapshot collab.Snapshot) map[string]collab.FileOffer {
	result := make(map[string]collab.FileOffer)
	duplicates := make(map[string]struct{})
	for _, item := range snapshot.Timeline {
		if item.File == nil || item.ID == "" || item.File.ID != item.ID {
			continue
		}
		if _, exists := result[item.ID]; exists {
			delete(result, item.ID)
			duplicates[item.ID] = struct{}{}
			continue
		}
		if _, duplicate := duplicates[item.ID]; !duplicate {
			result[item.ID] = *item.File
		}
	}
	return result
}

func (c *desktopCollaboration) rebuildFileOffersLocked(snapshot collab.Snapshot) {
	c.fileOffers = collaborationFileOfferIndex(snapshot)
	c.fileOffersReady = true
}

func (c *desktopCollaboration) fileOfferLocked(fileID string) (collab.FileOffer, bool) {
	if c.fileOffersReady {
		offer, ok := c.fileOffers[fileID]
		return offer, ok
	}
	return collaborationFileOffer(c.state.Snapshot, fileID)
}

func validateCollaborationFileOffer(offer collab.FileOffer, maxSize int64) error {
	validID := func(value string) bool {
		return strings.TrimSpace(value) != "" && len(value) <= collab.MaxIDBytes
	}
	name := strings.TrimSpace(offer.Name)
	if !validID(offer.ID) || !validID(offer.OwnerID) || name == "" || len(name) > 1024 || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) {
		return fmt.Errorf("identity or name is invalid")
	}
	if offer.Size < 0 || offer.Size > maxSize || len(offer.MIME) > 256 || !validCollaborationSHA256(offer.SHA256) || !validCollaborationSHA256(offer.ManifestHash) {
		return fmt.Errorf("size, MIME, or hash is invalid")
	}
	if offer.ChunkSize < collab.MinFileChunkSize || offer.ChunkSize > collab.MaxFileChunkSize {
		return fmt.Errorf("chunk size is invalid")
	}
	expectedChunks := 0
	if offer.Size > 0 {
		expectedChunks = int((offer.Size + offer.ChunkSize - 1) / offer.ChunkSize)
	}
	if offer.ChunkCount != expectedChunks {
		return fmt.Errorf("chunk count is invalid")
	}
	return nil
}

func validCollaborationSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func collaborationManifestLimit(offer collab.FileOffer) int64 {
	const maxManifest = int64(32 << 20)
	limit := int64(4096) + int64(offer.ChunkCount)*80
	if limit < 4096 {
		return 4096
	}
	return min(limit, maxManifest)
}

func fileOfferIdentityEqual(a, b collab.FileOffer) bool {
	return a.ID == b.ID && a.OwnerID == b.OwnerID && a.Size == b.Size &&
		strings.EqualFold(a.SHA256, b.SHA256) && strings.EqualFold(a.ManifestHash, b.ManifestHash) &&
		a.ChunkSize == b.ChunkSize && a.ChunkCount == b.ChunkCount && a.Revision == b.Revision
}

func fileTicketMatchesOffer(ticket collab.FileTransferTicket, offer collab.FileOffer) bool {
	return ticket.File.RevokedAt == nil && fileOfferIdentityEqual(ticket.File, offer)
}

func transferMatchesOffer(transfer *CollaborationFileTransfer, room, roomInstance string, offer collab.FileOffer) bool {
	return roomInstance != "" && transfer != nil && transfer.FileID == offer.ID && transfer.Room == room && transfer.RoomInstance == roomInstance && transfer.OwnerID == offer.OwnerID &&
		transfer.Total == offer.Size && strings.EqualFold(transfer.SHA256, offer.SHA256) &&
		strings.EqualFold(transfer.ManifestHash, offer.ManifestHash) && transfer.ChunkSize == offer.ChunkSize &&
		transfer.ChunkCount == offer.ChunkCount && transfer.OfferRevision == offer.Revision
}

func collaborationTransferArchiveKey(roomInstance, fileID string) string {
	return strings.TrimSpace(roomInstance) + "\x00" + strings.TrimSpace(fileID)
}

// switchFileTransfersLocked keeps only the newly active Room instance in the
// fileID-keyed hot maps. Other Room instances remain persisted in the archive,
// so identical remote file IDs cannot overwrite pause/completion state.
func (c *desktopCollaboration) switchFileTransfersLocked(roomInstance string) {
	if c.transfers == nil {
		c.transfers = map[string]*CollaborationFileTransfer{}
	}
	if c.transferArchive == nil {
		c.transferArchive = map[string]*CollaborationFileTransfer{}
	}
	for fileID := range c.transferCancel {
		transfer := c.transfers[fileID]
		c.cancelFileTransferLocked(fileID)
		if transfer == nil || transfer.Status == "completed" {
			continue
		}
		if transfer.PausedByUser {
			transfer.Status, transfer.Error, transfer.Retryable = "paused", "", true
		} else {
			transfer.Status, transfer.Error, transfer.Retryable = "waiting_sender", "Room 路由已切换，将自动恢复", true
		}
	}
	for fileID, transfer := range c.transfers {
		if transfer != nil {
			c.transferArchive[collaborationTransferArchiveKey(transfer.RoomInstance, transfer.FileID)] = transfer
		}
		delete(c.transfers, fileID)
	}
	for key, transfer := range c.transferArchive {
		if transfer != nil && transfer.RoomInstance == roomInstance {
			c.transfers[transfer.FileID] = transfer
			delete(c.transferArchive, key)
		}
	}
	c.autoRetryAfter = map[string]time.Time{}
	c.verifiedFiles = map[string]collaborationVerifiedFile{}
}

func (c *desktopCollaboration) closeFileTransfers() {
	c.mu.Lock()
	c.autoScanClosed = true
	if c.autoRetryTimer != nil {
		c.autoRetryTimer.Stop()
		c.autoRetryTimer = nil
		c.autoRetryAt = time.Time{}
	}
	for id := range c.transferCancel {
		c.cancelFileTransferLocked(id)
	}
	owned := c.ownedParts
	c.ownedParts = map[string]os.FileInfo{}
	origin := c.fileOrigin
	c.fileOrigin = nil
	c.mu.Unlock()
	for path, identity := range owned {
		if linked, err := os.Lstat(path); err == nil && linked.Mode().IsRegular() && os.SameFile(identity, linked) {
			_ = os.Remove(path)
		}
	}
	if origin != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = origin.server.Shutdown(ctx)
		cancel()
	}
}
