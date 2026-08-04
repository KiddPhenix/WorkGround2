package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/collab"
)

const collaborationFileChunkSize = int64(4 * 1024 * 1024)

type collaborationFileManifest struct {
	FileID      string   `json:"fileId"`
	Size        int64    `json:"size"`
	ChunkSize   int64    `json:"chunkSize"`
	ChunkHashes []string `json:"chunkHashes"`
}

type collaborationSharedFile struct {
	FileID       string   `json:"fileId"`
	Room         string   `json:"room"`
	OwnerID      string   `json:"ownerId"`
	Path         string   `json:"path"`
	Name         string   `json:"name"`
	Size         int64    `json:"size"`
	MIME         string   `json:"mime,omitempty"`
	SHA256       string   `json:"sha256"`
	ManifestHash string   `json:"manifestHash"`
	ChunkSize    int64    `json:"chunkSize"`
	ChunkHashes  []string `json:"chunkHashes"`
	ModTimeUnix  int64    `json:"modTimeUnix"`
	Status       string   `json:"status"`
	Error        string   `json:"error,omitempty"`
}

type collaborationFileOrigin struct {
	server   *http.Server
	listener net.Listener
	secret   string
	port     int
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
		room, ownerID := c.state.Room, c.state.MemberID
		c.mu.RUnlock()
		if room == "" || ownerID == "" {
			return result, fmt.Errorf("collaboration Room is unavailable")
		}
		share.Room, share.OwnerID = room, ownerID
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
		ChunkHashes: chunkHashes, ModTimeUnix: info.ModTime().UnixNano(), Status: "preparing",
	}, nil
}

func (c *desktopCollaboration) ensureFileOrigin(conn *collaborationConnection) error {
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
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		c.mu.Unlock()
		return fmt.Errorf("listen file origin: %w", err)
	}
	origin := &collaborationFileOrigin{listener: listener, secret: base64.RawURLEncoding.EncodeToString(secretBytes), port: listener.Addr().(*net.TCPAddr).Port}
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
	c.mu.RUnlock()
	if !ok || origin == nil || share.Room != room || share.Status == "revoked" {
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
	_, ok := c.shares[fileID]
	c.mu.RUnlock()
	if conn == nil || conn.filePeer == nil || origin == nil || !ok {
		return fmt.Errorf("file origin is unavailable")
	}
	path := "/collab/v1/rooms/" + url.PathEscape(conn.room) + "/files/" + url.PathEscape(fileID) + "/origin"
	var result map[string]any
	return conn.filePeer.doJSON(ctx, http.MethodPost, path, collab.RegisterFileOriginInput{Port: origin.port, Secret: origin.secret, Hosts: collaborationLocalHosts("0.0.0.0")}, &result, true)
}

func (c *desktopCollaboration) restoreFileOrigins(conn *collaborationConnection) {
	c.mu.RLock()
	current := c.conn == conn
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
		if share.Room == conn.room && share.OwnerID == conn.memberID && share.Status != "revoked" {
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
	c.resumeWaitingFileTransfers()
	c.emitState()
}

func (c *desktopCollaboration) hasPendingFileOrigins(conn *collaborationConnection) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.conn != conn || len(c.outbox) != 0 {
		return false
	}
	for _, share := range c.shares {
		if share.Room == conn.room && share.OwnerID == conn.memberID && (share.Status == "pending" || share.Status == "unavailable" || share.Status == "failed") {
			return true
		}
	}
	return false
}

func (c *desktopCollaboration) receiveFile(ctx context.Context, fileID, destination string) (CollaborationFileTransfer, error) {
	fileID = strings.TrimSpace(fileID)
	c.mu.RLock()
	file, ok := collaborationFileOffer(c.state.Snapshot, fileID)
	c.mu.RUnlock()
	if !ok || file.RevokedAt != nil {
		return CollaborationFileTransfer{}, fmt.Errorf("file offer is unavailable")
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
	if old := c.transfers[fileID]; old != nil && old.Status == "completed" {
		view := cloneCollaborationTransfer(*old)
		c.mu.Unlock()
		return view, nil
	}
	transfer := &CollaborationFileTransfer{ID: stableCollaborationID("transfer", c.state.Room+"\x00"+fileID+"\x00"+c.state.MemberID), FileID: fileID, Direction: "receive", Name: file.Name, Status: "negotiating", Total: file.Size, Destination: destination, PartPath: destination + ".wg2part", Completed: make([]bool, file.ChunkCount)}
	if old := c.transfers[fileID]; old != nil && old.Destination == destination && old.Total == file.Size && len(old.Completed) == file.ChunkCount {
		transfer = old
		transfer.Status, transfer.Error, transfer.Retryable = "negotiating", "", false
	}
	c.transfers[fileID] = transfer
	c.persistLocked()
	view := cloneCollaborationTransfer(*transfer)
	c.mu.Unlock()
	c.emitState()
	c.startFileDownload(fileID)
	return view, nil
}

func (c *desktopCollaboration) startFileDownload(fileID string) {
	c.mu.Lock()
	if cancel := c.transferCancel[fileID]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(c.app.bootContext())
	c.transferCancel[fileID] = cancel
	c.mu.Unlock()
	go c.downloadFile(ctx, fileID)
}

func (c *desktopCollaboration) downloadFile(ctx context.Context, fileID string) {
	c.mu.RLock()
	transfer := c.transfers[fileID]
	conn := c.conn
	file, found := collaborationFileOffer(c.state.Snapshot, fileID)
	c.mu.RUnlock()
	if transfer == nil || conn == nil || conn.filePeer == nil || !found {
		c.failFileTransfer(fileID, "waiting_sender", "分享者或房间连接当前不可用", true)
		return
	}
	ticket, manifest, err := conn.filePeer.fetchFileManifest(ctx, fileID)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		status, retryable := fileTransferFailure(err)
		c.failFileTransfer(fileID, status, err.Error(), retryable)
		return
	}
	manifestData, _ := json.Marshal(manifest)
	manifestSum := sha256.Sum256(manifestData)
	if manifest.FileID != file.ID || manifest.Size != file.Size || manifest.ChunkSize != file.ChunkSize || len(manifest.ChunkHashes) != file.ChunkCount || hex.EncodeToString(manifestSum[:]) != file.ManifestHash {
		c.failFileTransfer(fileID, "failed", "文件清单校验失败", true)
		return
	}
	f, err := os.OpenFile(transfer.PartPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		c.failFileTransfer(fileID, "failed", err.Error(), true)
		return
	}
	defer f.Close()
	if err := f.Truncate(file.Size); err != nil {
		c.failFileTransfer(fileID, "failed", err.Error(), true)
		return
	}
	c.updateFileTransfer(fileID, func(value *CollaborationFileTransfer) {
		value.Status, value.Error, value.Retryable = "downloading", "", false
	})
	for index, expected := range manifest.ChunkHashes {
		if ctx.Err() != nil {
			return
		}
		c.mu.RLock()
		done := index < len(c.transfers[fileID].Completed) && c.transfers[fileID].Completed[index]
		c.mu.RUnlock()
		if done {
			continue
		}
		if time.Until(ticket.ExpiresAt) < 30*time.Second {
			ticket, err = conn.filePeer.fileTicket(ctx, fileID)
			if err != nil {
				c.failFileTransfer(fileID, "waiting_sender", err.Error(), true)
				return
			}
		}
		data, err := conn.filePeer.fetchFileChunk(ctx, ticket, index)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			status, retryable := fileTransferFailure(err)
			c.failFileTransfer(fileID, status, err.Error(), retryable)
			return
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			c.failFileTransfer(fileID, "failed", "文件分块校验失败", true)
			return
		}
		if _, err := f.WriteAt(data, int64(index)*file.ChunkSize); err != nil {
			c.failFileTransfer(fileID, "failed", err.Error(), true)
			return
		}
		c.updateFileTransfer(fileID, func(value *CollaborationFileTransfer) {
			if index < len(value.Completed) {
				value.Completed[index] = true
			}
			value.Transferred = completedFileBytes(value.Completed, value.Total, file.ChunkSize)
		})
	}
	if err := f.Sync(); err != nil {
		c.failFileTransfer(fileID, "failed", err.Error(), true)
		return
	}
	if ctx.Err() != nil {
		return
	}
	c.updateFileTransfer(fileID, func(value *CollaborationFileTransfer) { value.Status = "verifying" })
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		c.failFileTransfer(fileID, "failed", err.Error(), true)
		return
	}
	whole := sha256.New()
	if _, err := io.Copy(whole, f); err != nil || hex.EncodeToString(whole.Sum(nil)) != file.SHA256 {
		c.updateFileTransfer(fileID, func(value *CollaborationFileTransfer) {
			for index := range value.Completed {
				value.Completed[index] = false
			}
			value.Transferred = 0
		})
		c.failFileTransfer(fileID, "failed", "完整文件校验失败", true)
		return
	}
	if ctx.Err() != nil {
		return
	}
	if err := f.Close(); err != nil {
		c.failFileTransfer(fileID, "failed", err.Error(), true)
		return
	}
	if _, err := os.Stat(transfer.Destination); err == nil {
		c.failFileTransfer(fileID, "failed", "目标文件已存在，请选择新的保存位置", false)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		c.failFileTransfer(fileID, "failed", err.Error(), true)
		return
	}
	if err := os.Rename(transfer.PartPath, transfer.Destination); err != nil {
		c.failFileTransfer(fileID, "failed", err.Error(), true)
		return
	}
	c.updateFileTransfer(fileID, func(value *CollaborationFileTransfer) {
		value.Status, value.Error, value.Retryable, value.Transferred = "completed", "", false, value.Total
	})
}

func (p *httpCollaborationPeer) fileTicket(ctx context.Context, fileID string) (collab.FileTransferTicket, error) {
	var ticket collab.FileTransferTicket
	path := "/collab/v1/rooms/" + url.PathEscape(p.room) + "/files/" + url.PathEscape(fileID) + "/ticket"
	err := p.doJSON(ctx, http.MethodGet, path, nil, &ticket, true)
	return ticket, err
}

func (p *httpCollaborationPeer) fetchFileManifest(ctx context.Context, fileID string) (collab.FileTransferTicket, collaborationFileManifest, error) {
	ticket, err := p.fileTicket(ctx, fileID)
	if err != nil {
		return ticket, collaborationFileManifest{}, err
	}
	data, err := p.fetchFileData(ctx, ticket, "/manifest", 32<<20)
	if err != nil {
		return ticket, collaborationFileManifest{}, err
	}
	var manifest collaborationFileManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ticket, collaborationFileManifest{}, fmt.Errorf("decode file manifest: %w", err)
	}
	return ticket, manifest, nil
}

func (p *httpCollaborationPeer) fetchFileChunk(ctx context.Context, ticket collab.FileTransferTicket, index int) ([]byte, error) {
	return p.fetchFileData(ctx, ticket, "/chunks/"+strconv.Itoa(index), ticket.File.ChunkSize+1)
}

func (p *httpCollaborationPeer) fetchFileData(ctx context.Context, ticket collab.FileTransferTicket, suffix string, limit int64) ([]byte, error) {
	client := &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("file transfer redirects are disabled") }}
	candidates := append([]string(nil), ticket.DirectURLs...)
	candidates = append(candidates, p.baseURL+ticket.ProxyPath)
	var lastErr error
	for index, base := range candidates {
		endpoint := base + suffix
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			lastErr = err
			continue
		}
		if index < len(ticket.DirectURLs) {
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
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, limit))
		_ = resp.Body.Close()
		if readErr == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && int64(len(data)) < limit {
			return data, nil
		}
		if readErr != nil {
			lastErr = readErr
		} else {
			lastErr = fmt.Errorf("file source returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("file owner is unavailable")
	}
	return nil, lastErr
}

func (c *desktopCollaboration) pauseFile(fileID string) (CollaborationFileTransfer, error) {
	c.mu.Lock()
	transfer := c.transfers[fileID]
	if transfer == nil {
		c.mu.Unlock()
		return CollaborationFileTransfer{}, fmt.Errorf("file transfer does not exist")
	}
	if cancel := c.transferCancel[fileID]; cancel != nil {
		cancel()
		delete(c.transferCancel, fileID)
	}
	if transfer.Status != "completed" {
		transfer.Status, transfer.Error, transfer.Retryable = "paused", "", true
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
	transfer.Status, transfer.Error, transfer.Retryable = "negotiating", "", false
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

func (c *desktopCollaboration) failFileTransfer(fileID, status, message string, retryable bool) {
	c.updateFileTransfer(fileID, func(value *CollaborationFileTransfer) {
		value.Status, value.Error, value.Retryable = status, message, retryable
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
		if transfer.Status == "waiting_sender" {
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
		result = append(result, cloneCollaborationTransfer(*transfer))
	}
	for id := range c.shares {
		result = append(result, c.fileTransferForShareLocked(id))
	}
	return result
}

func (c *desktopCollaboration) persistedFileTransfersLocked() []CollaborationFileTransfer {
	result := make([]CollaborationFileTransfer, 0, len(c.transfers))
	for _, transfer := range c.transfers {
		result = append(result, cloneCollaborationTransfer(*transfer))
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
	return CollaborationFileTransfer{ID: "share:" + fileID, FileID: fileID, Direction: "share", Name: share.Name, Status: share.Status, Transferred: share.Size, Total: share.Size, Error: share.Error, Retryable: share.Status == "unavailable" || share.Status == "failed"}
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
		if item.ID == fileID && item.File != nil {
			return *item.File, true
		}
	}
	return collab.FileOffer{}, false
}

func (c *desktopCollaboration) closeFileTransfers() {
	c.mu.Lock()
	for id, cancel := range c.transferCancel {
		cancel()
		delete(c.transferCancel, id)
	}
	origin := c.fileOrigin
	c.fileOrigin = nil
	c.mu.Unlock()
	if origin != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = origin.server.Shutdown(ctx)
		cancel()
	}
}
