package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"workground2/internal/collab"
	"workground2/internal/relayproto"
)

const relayFileSegmentSize = 48 << 10

type relayFileRequest struct {
	Room    string `json:"room"`
	Session string `json:"session"`
	FileID  string `json:"fileId"`
	Index   int    `json:"index,omitempty"`
	Offset  int64  `json:"offset,omitempty"`
	Size    int    `json:"size,omitempty"`
}

type relayFileManifestResponse struct {
	File     collab.FileOffer          `json:"file"`
	Manifest collaborationFileManifest `json:"manifest"`
}

type relayFileSegmentResponse struct {
	Data []byte `json:"data"`
}

type relayHostFilePeer struct {
	host    *collaborationRelayHost
	room    string
	member  string
	session string
}

type fallbackCollaborationFilePeer struct {
	primary  collaborationFilePeer
	fallback collaborationFilePeer
}

func (p *fallbackCollaborationFilePeer) RegisterFileOrigin(ctx context.Context, fileID string, input collab.RegisterFileOriginInput) error {
	if err := p.primary.RegisterFileOrigin(ctx, fileID, input); err == nil {
		return nil
	}
	return p.fallback.RegisterFileOrigin(ctx, fileID, input)
}

func (p *fallbackCollaborationFilePeer) fileTicket(ctx context.Context, fileID string) (collab.FileTransferTicket, error) {
	value, err := p.primary.fileTicket(ctx, fileID)
	if err == nil {
		return value, nil
	}
	return p.fallback.fileTicket(ctx, fileID)
}

func (p *fallbackCollaborationFilePeer) fetchFileManifest(ctx context.Context, fileID string) (collab.FileTransferTicket, collaborationFileManifest, error) {
	ticket, manifest, err := p.primary.fetchFileManifest(ctx, fileID)
	if err == nil {
		return ticket, manifest, nil
	}
	return p.fallback.fetchFileManifest(ctx, fileID)
}

func (p *fallbackCollaborationFilePeer) fetchFileChunk(ctx context.Context, ticket collab.FileTransferTicket, index int) ([]byte, error) {
	value, err := p.primary.fetchFileChunk(ctx, ticket, index)
	if err == nil {
		return value, nil
	}
	return p.fallback.fetchFileChunk(ctx, ticket, index)
}

func (h *collaborationRelayHost) dispatchRelayFile(ctx context.Context, _ string, request relayRPCRequest) (json.RawMessage, *relayRPCError) {
	var input relayFileRequest
	if err := json.Unmarshal(request.Body, &input); err != nil {
		return nil, toRelayRPCError(err)
	}
	input.Room = h.authorityRoom(input.Room)
	receiver, err := h.roomConn.authority.service.Authenticate(ctx, input.Room, input.Session)
	if err != nil {
		return nil, toRelayRPCError(err)
	}
	file, err := h.roomConn.authority.service.File(ctx, input.Room, input.FileID)
	if err != nil {
		return nil, toRelayRPCError(err)
	}
	if receiver == "" {
		return nil, &relayRPCError{Code: "unauthorized", Message: "file receiver is unavailable"}
	}
	var value any
	if file.OwnerID == h.roomConn.memberID {
		value, err = h.runtime.serveRelayFileSource(request.Method, input)
	} else {
		peerID := h.peerForMember(file.OwnerID)
		if peerID == "" {
			err = &collab.Error{Code: collab.CodeUnavailable, Message: "file owner is offline", Retryable: true}
		} else {
			timeoutCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			err = h.callPeerRPC(timeoutCtx, peerID, "file.source."+request.Method[5:], input, &value)
		}
	}
	if err != nil {
		return nil, toRelayRPCError(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, toRelayRPCError(err)
	}
	return data, nil
}

func (h *collaborationRelayHost) peerForMember(memberID string) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for peerID, session := range h.sessions {
		if session.memberID == memberID {
			return peerID
		}
	}
	return ""
}

func (h *collaborationRelayHost) callPeerRPC(ctx context.Context, peerID, method string, input, output any) error {
	h.mu.RLock()
	session := h.sessions[peerID]
	h.mu.RUnlock()
	if session == nil || session.cipher == nil {
		return &collab.Error{Code: collab.CodeUnavailable, Message: "file owner Relay route is unavailable", Retryable: true}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := json.Marshal(relayRPCRequest{Method: method, Body: body})
	if err != nil {
		return err
	}
	requestID := newCollaborationRequestID("relay-host-rpc")
	header := relayproto.Header{Version: relayproto.Version, Type: "rpc.request", RelayRequestID: requestID, TunnelID: h.tunnelID, PeerID: peerID, Epoch: uint64(time.Now().UnixNano()), Flags: []string{"encrypted"}}
	header.Sequence = h.socket.seq.Add(1)
	encrypted, err := session.cipher.seal(header, request)
	if err != nil {
		return err
	}
	waiter := make(chan relayRPCResult, 1)
	h.mu.Lock()
	h.pending[requestID] = waiter
	h.mu.Unlock()
	if err := h.socket.writeBytes(header, encrypted); err != nil {
		h.mu.Lock()
		delete(h.pending, requestID)
		h.mu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		h.mu.Lock()
		delete(h.pending, requestID)
		h.mu.Unlock()
		return ctx.Err()
	case result := <-waiter:
		if result.err != nil {
			return result.err
		}
		if result.response.Error != nil {
			return &collab.Error{Code: collab.ErrorCode(result.response.Error.Code), Message: result.response.Error.Message, Retryable: result.response.Error.Retryable}
		}
		return json.Unmarshal(result.response.Body, output)
	}
}

func (h *collaborationRelayHost) handlePeerRPCResponse(header relayproto.Header, payload []byte) {
	h.mu.RLock()
	session := h.sessions[header.PeerID]
	h.mu.RUnlock()
	if session == nil || session.cipher == nil {
		return
	}
	plaintext, err := session.cipher.open(header, payload)
	if err != nil {
		return
	}
	var response relayRPCResponse
	if json.Unmarshal(plaintext, &response) != nil {
		return
	}
	h.mu.Lock()
	waiter := h.pending[header.RelayRequestID]
	delete(h.pending, header.RelayRequestID)
	h.mu.Unlock()
	if waiter != nil {
		waiter <- relayRPCResult{response: response}
	}
}

func (p *relayCollaborationPeer) handleHostRPC(header relayproto.Header, payload []byte) {
	plaintext, err := p.cipher.open(header, payload)
	if err != nil {
		return
	}
	var request relayRPCRequest
	if json.Unmarshal(plaintext, &request) != nil {
		return
	}
	var body json.RawMessage
	var rpcErr *relayRPCError
	if p.fileSource == nil {
		rpcErr = &relayRPCError{Code: string(collab.CodeUnavailable), Message: "file source is unavailable", Retryable: true}
	} else {
		var input relayFileRequest
		if err := json.Unmarshal(request.Body, &input); err != nil {
			rpcErr = toRelayRPCError(err)
		} else {
			value, err := p.fileSource.serveRelayFileSource(request.Method, input)
			if err != nil {
				rpcErr = toRelayRPCError(err)
			} else {
				body, _ = json.Marshal(value)
			}
		}
	}
	encoded, _ := json.Marshal(relayRPCResponse{Body: body, Error: rpcErr})
	responseHeader := relayproto.Header{Version: relayproto.Version, Type: "rpc.response", RelayRequestID: header.RelayRequestID, TunnelID: p.tunnelID, PeerID: p.peerID, Epoch: header.Epoch, Flags: []string{"encrypted"}}
	responseHeader.Sequence = p.socket.seq.Add(1)
	encrypted, err := p.cipher.seal(responseHeader, encoded)
	if err == nil {
		_ = p.socket.writeBytes(responseHeader, encrypted)
	}
}

func (c *desktopCollaboration) serveRelayFileSource(method string, input relayFileRequest) (any, error) {
	c.mu.RLock()
	share, ok := c.shares[input.FileID]
	c.mu.RUnlock()
	if !ok || share.Room != input.Room || share.Status == "revoked" {
		return nil, &collab.Error{Code: collab.CodeUnavailable, Message: "file source is unavailable", Retryable: true}
	}
	switch method {
	case "file.manifest", "file.source.manifest":
		return relayFileManifestResponse{File: collab.FileOffer{ID: share.FileID, OwnerID: share.OwnerID, Name: share.Name, Size: share.Size, MIME: share.MIME, SHA256: share.SHA256, ManifestHash: share.ManifestHash, ChunkSize: share.ChunkSize, ChunkCount: len(share.ChunkHashes)}, Manifest: collaborationFileManifest{FileID: share.FileID, Size: share.Size, ChunkSize: share.ChunkSize, ChunkHashes: append([]string(nil), share.ChunkHashes...)}}, nil
	case "file.segment", "file.source.segment":
		if input.Index < 0 || input.Index >= len(share.ChunkHashes) || input.Offset < 0 || input.Size < 1 || input.Size > relayFileSegmentSize {
			return nil, &collab.Error{Code: collab.CodeInvalid, Message: "invalid file segment"}
		}
		chunkOffset := int64(input.Index) * share.ChunkSize
		chunkLength := min(share.ChunkSize, share.Size-chunkOffset)
		if input.Offset+int64(input.Size) > chunkLength {
			return nil, &collab.Error{Code: collab.CodeInvalid, Message: "file segment exceeds chunk"}
		}
		file, err := os.Open(share.Path)
		if err != nil {
			return nil, &collab.Error{Code: collab.CodeUnavailable, Message: err.Error(), Retryable: true}
		}
		defer file.Close()
		data := make([]byte, input.Size)
		if _, err := io.ReadFull(io.NewSectionReader(file, chunkOffset+input.Offset, int64(input.Size)), data); err != nil {
			return nil, &collab.Error{Code: collab.CodeUnavailable, Message: err.Error(), Retryable: true}
		}
		return relayFileSegmentResponse{Data: data}, nil
	default:
		return nil, &collab.Error{Code: collab.CodeInvalid, Message: "unsupported Relay file method"}
	}
}

func (p *relayCollaborationPeer) RegisterFileOrigin(_ context.Context, fileID string, _ collab.RegisterFileOriginInput) error {
	if p.fileSource == nil {
		return &collab.Error{Code: collab.CodeUnavailable, Message: "Relay file source is unavailable", Retryable: true}
	}
	p.fileSource.mu.RLock()
	_, ok := p.fileSource.shares[fileID]
	p.fileSource.mu.RUnlock()
	if !ok {
		return &collab.Error{Code: collab.CodeNotFound, Message: "shared file is unavailable"}
	}
	return nil
}

func (p *relayCollaborationPeer) fetchFileManifest(ctx context.Context, fileID string) (collab.FileTransferTicket, collaborationFileManifest, error) {
	var value relayFileManifestResponse
	err := p.call(ctx, "file.manifest", relayFileRequest{Room: p.room, Session: p.session, FileID: fileID}, &value)
	if err != nil {
		return collab.FileTransferTicket{}, collaborationFileManifest{}, err
	}
	ticket := collab.FileTransferTicket{File: value.File, Ticket: "relay-e2e", ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}
	return ticket, value.Manifest, nil
}

func (p *relayCollaborationPeer) fileTicket(ctx context.Context, fileID string) (collab.FileTransferTicket, error) {
	ticket, _, err := p.fetchFileManifest(ctx, fileID)
	return ticket, err
}

func (p *relayCollaborationPeer) fetchFileChunk(ctx context.Context, ticket collab.FileTransferTicket, index int) ([]byte, error) {
	chunkLength := min(ticket.File.ChunkSize, ticket.File.Size-int64(index)*ticket.File.ChunkSize)
	if index < 0 || chunkLength < 0 {
		return nil, fmt.Errorf("invalid file chunk")
	}
	data := make([]byte, chunkLength)
	for offset := int64(0); offset < chunkLength; {
		size := int(min(int64(relayFileSegmentSize), chunkLength-offset))
		var value relayFileSegmentResponse
		err := p.call(ctx, "file.segment", relayFileRequest{Room: p.room, Session: p.session, FileID: ticket.File.ID, Index: index, Offset: offset, Size: size}, &value)
		if err != nil {
			return nil, err
		}
		if len(value.Data) != size {
			return nil, &collaborationTransportError{message: "Relay file segment has an unexpected size", retryable: true}
		}
		copy(data[offset:], value.Data)
		offset += int64(size)
	}
	return data, nil
}

func (p *relayHostFilePeer) RegisterFileOrigin(_ context.Context, _ string, _ collab.RegisterFileOriginInput) error {
	return nil
}

func (p *relayHostFilePeer) fetchFileManifest(ctx context.Context, fileID string) (collab.FileTransferTicket, collaborationFileManifest, error) {
	request := relayRPCRequest{Method: "file.manifest"}
	request.Body, _ = json.Marshal(relayFileRequest{Room: p.room, Session: p.session, FileID: fileID})
	body, rpcErr := p.host.dispatchRelayFile(ctx, "", request)
	if rpcErr != nil {
		return collab.FileTransferTicket{}, collaborationFileManifest{}, &collab.Error{Code: collab.ErrorCode(rpcErr.Code), Message: rpcErr.Message, Retryable: rpcErr.Retryable}
	}
	var value relayFileManifestResponse
	if err := json.Unmarshal(body, &value); err != nil {
		return collab.FileTransferTicket{}, collaborationFileManifest{}, err
	}
	return collab.FileTransferTicket{File: value.File, Ticket: "relay-e2e", ExpiresAt: time.Now().UTC().Add(5 * time.Minute)}, value.Manifest, nil
}

func (p *relayHostFilePeer) fileTicket(ctx context.Context, fileID string) (collab.FileTransferTicket, error) {
	ticket, _, err := p.fetchFileManifest(ctx, fileID)
	return ticket, err
}

func (p *relayHostFilePeer) fetchFileChunk(ctx context.Context, ticket collab.FileTransferTicket, index int) ([]byte, error) {
	chunkLength := min(ticket.File.ChunkSize, ticket.File.Size-int64(index)*ticket.File.ChunkSize)
	data := make([]byte, chunkLength)
	for offset := int64(0); offset < chunkLength; {
		size := int(min(int64(relayFileSegmentSize), chunkLength-offset))
		request := relayRPCRequest{Method: "file.segment"}
		request.Body, _ = json.Marshal(relayFileRequest{Room: p.room, Session: p.session, FileID: ticket.File.ID, Index: index, Offset: offset, Size: size})
		body, rpcErr := p.host.dispatchRelayFile(ctx, "", request)
		if rpcErr != nil {
			return nil, &collab.Error{Code: collab.ErrorCode(rpcErr.Code), Message: rpcErr.Message, Retryable: rpcErr.Retryable}
		}
		var value relayFileSegmentResponse
		if err := json.Unmarshal(body, &value); err != nil {
			return nil, err
		}
		if len(value.Data) != size {
			return nil, &collaborationTransportError{message: "Relay file segment has an unexpected size", retryable: true}
		}
		copy(data[offset:], value.Data)
		offset += int64(len(value.Data))
	}
	return data, nil
}

var _ collaborationFilePeer = (*relayCollaborationPeer)(nil)
var _ collaborationFilePeer = (*relayHostFilePeer)(nil)
