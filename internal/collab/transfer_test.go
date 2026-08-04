package collab

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestFileTransferTicketAndProxy(t *testing.T) {
	service, _, owner := newTestService(t, "")
	const hash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	_, err := service.Submit(context.Background(), CommandEnvelope{RequestID: "offer", Room: "room", MemberID: owner.Member.ID, Session: owner.ConnectionSession, Command: Command{Type: CommandOfferFile, FileOffer: &OfferFileInput{
		FileID: "file", Name: "data.bin", Size: 4, SHA256: hash, ManifestHash: hash, ChunkSize: MinFileChunkSize, ChunkCount: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "01234567890123456789012345678901"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := VerifyFileTicket(secret, r.URL.Query().Get("ticket"), "room", "file", owner.Member.ID, time.Now()); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/manifest") {
			_, _ = io.WriteString(w, `{"chunkHashes":["`+hash+`"]}`)
			return
		}
		_, _ = w.Write([]byte("data"))
	}))
	defer origin.Close()
	originURL, _ := url.Parse(origin.URL)
	port, _ := strconv.Atoi(originURL.Port())
	host := httptest.NewServer(NewHandler(service))
	defer host.Close()
	registerBody, _ := json.Marshal(RegisterFileOriginInput{Port: port, Secret: secret, Hosts: []string{"127.0.0.1"}})
	req, _ := http.NewRequest(http.MethodPost, host.URL+"/collab/v1/rooms/room/files/file/origin", bytes.NewReader(registerBody))
	req.Header.Set("Authorization", "Bearer "+owner.ConnectionSession)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("register status = %v, %v", responseStatus(resp), err)
	}
	_ = resp.Body.Close()

	get := func(path string) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, host.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+owner.ConnectionSession)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	ticketResp := get("/collab/v1/rooms/room/files/file/ticket")
	defer ticketResp.Body.Close()
	var ticket FileTransferTicket
	if ticketResp.StatusCode != http.StatusOK || json.NewDecoder(ticketResp.Body).Decode(&ticket) != nil || ticket.Ticket == "" || len(ticket.DirectURLs) == 0 {
		t.Fatalf("ticket = %+v, status %d", ticket, ticketResp.StatusCode)
	}
	chunkResp := get("/collab/v1/rooms/room/files/file/chunks/0")
	body, _ := io.ReadAll(chunkResp.Body)
	_ = chunkResp.Body.Close()
	if chunkResp.StatusCode != http.StatusOK || string(body) != "data" {
		t.Fatalf("chunk status = %d, body = %q", chunkResp.StatusCode, body)
	}
}

func TestVerifyFileTicketRejectsTamperingAndExpiry(t *testing.T) {
	claims := fileTicketClaims{Room: "room", FileID: "file", OwnerID: "owner", ReceiverID: "receiver", Expires: time.Now().Add(time.Minute).Unix()}
	ticket, err := signFileTicket("01234567890123456789012345678901", claims)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyFileTicket("01234567890123456789012345678901", ticket, "room", "file", "owner", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFileTicket("wrong-secret-wrong-secret-wrong-secret", ticket, "room", "file", "owner", time.Now()); err == nil {
		t.Fatal("tampered secret was accepted")
	}
	if err := VerifyFileTicket("01234567890123456789012345678901", ticket, "room", "file", "owner", time.Now().Add(2*time.Minute)); err == nil {
		t.Fatal("expired ticket was accepted")
	}
}

func responseStatus(resp *http.Response) any {
	if resp == nil {
		return nil
	}
	return resp.StatusCode
}
