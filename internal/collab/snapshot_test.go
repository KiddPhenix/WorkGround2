package collab

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestSnapshotManifestFreezesSequenceAndChunks(t *testing.T) {
	service, _, joined := newTestService(t, "")
	for i := 0; i < 3; i++ {
		text := strings.Repeat(string(rune('a'+i)), 220*1024)
		if _, err := service.Submit(context.Background(), env("large-"+string(rune('a'+i)), "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: text}})); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := service.SnapshotManifest(context.Background(), "room", joined.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Chunks) < 2 || manifest.BaseSequence == 0 || manifest.TotalBytes <= DefaultSnapshotChunkBytes {
		t.Fatalf("manifest is not chunked: %+v", manifest)
	}
	if _, err := service.Submit(context.Background(), env("late-message", "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "late"}})); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	for index, meta := range manifest.Chunks {
		chunk, err := service.SnapshotChunk(context.Background(), "room", joined.ConnectionSession, manifest.SnapshotID, index)
		if err != nil {
			t.Fatal(err)
		}
		if chunk.SHA256 != meta.SHA256 || hashSnapshotBytes(chunk.Data) != meta.SHA256 || len(chunk.Data) != meta.Size {
			t.Fatalf("chunk %d failed integrity: chunk=%+v meta=%+v", index, chunk, meta)
		}
		_, _ = encoded.Write(chunk.Data)
	}
	if int64(encoded.Len()) != manifest.TotalBytes || hashSnapshotBytes(encoded.Bytes()) != manifest.RootSHA256 {
		t.Fatal("assembled snapshot failed root integrity")
	}
	var frozen Snapshot
	if err := json.Unmarshal(encoded.Bytes(), &frozen); err != nil {
		t.Fatal(err)
	}
	if frozen.LatestSequence != manifest.BaseSequence || frozen.Room.LatestSequence != manifest.BaseSequence {
		t.Fatalf("frozen watermark = %d/%d, want %d", frozen.LatestSequence, frozen.Room.LatestSequence, manifest.BaseSequence)
	}
	for _, item := range frozen.Timeline {
		if item.Chat != nil && item.Chat.Text == "late" {
			t.Fatal("post-manifest mutation leaked into frozen chunks")
		}
	}
	newer, err := service.SnapshotManifest(context.Background(), "room", joined.ConnectionSession)
	if err != nil || newer.BaseSequence <= manifest.BaseSequence || newer.SnapshotID == manifest.SnapshotID {
		t.Fatalf("new manifest did not advance: old=%+v new=%+v err=%v", manifest, newer, err)
	}
	if _, err := service.SnapshotChunk(context.Background(), "room", "wrong-session", manifest.SnapshotID, 0); err == nil {
		t.Fatal("snapshot chunk accepted an unauthorized session")
	}
}

func TestSnapshotChunkHTTPRoundTrip(t *testing.T) {
	service, _, joined := newTestService(t, "")
	server := httptest.NewServer(NewHandler(service))
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/collab/v1/rooms/room/snapshot/manifest", nil)
	request.Header.Set("Authorization", "Bearer "+joined.ConnectionSession)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var manifest SnapshotManifest
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&manifest) != nil {
		t.Fatalf("manifest response status=%d", response.StatusCode)
	}
	chunkURL := server.URL + "/collab/v1/rooms/room/snapshot/chunks/0?snapshotId=" + url.QueryEscape(manifest.SnapshotID)
	request, _ = http.NewRequest(http.MethodGet, chunkURL, nil)
	request.Header.Set("Authorization", "Bearer "+joined.ConnectionSession)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var data bytes.Buffer
	_, _ = data.ReadFrom(response.Body)
	if response.StatusCode != http.StatusOK || response.Header.Get("X-Collab-Chunk-SHA256") != manifest.Chunks[0].SHA256 || hashSnapshotBytes(data.Bytes()) != manifest.Chunks[0].SHA256 {
		t.Fatalf("chunk response status=%d headers=%v", response.StatusCode, response.Header)
	}
}

func TestIndependentOfflineOutboxesConvergeOnHostReplay(t *testing.T) {
	service, _, a := newTestService(t, "")
	b, err := service.Join(context.Background(), JoinInput{RequestID: "join-b", Room: "room", Member: memberDesc("b", "agent-b")})
	if err != nil {
		t.Fatal(err)
	}
	queues := [][]CommandEnvelope{
		{
			env("offline-a1", "a", a.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "A1"}}),
			env("offline-a2", "a", a.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "A2"}}),
		},
		{
			env("offline-b1", "b", b.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "B1"}}),
			env("offline-b2", "b", b.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "B2"}}),
		},
	}
	start := make(chan struct{})
	errCh := make(chan error, len(queues))
	var wg sync.WaitGroup
	for _, queue := range queues {
		queue := queue
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for _, command := range queue {
				if _, err := service.Submit(context.Background(), command); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	snapshot, err := service.Snapshot(context.Background(), "room", a.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]uint64{}
	for _, item := range snapshot.Timeline {
		if item.Chat != nil {
			positions[item.Chat.Text] = item.Sequence
		}
	}
	if len(positions) != 4 || positions["A1"] >= positions["A2"] || positions["B1"] >= positions["B2"] {
		t.Fatalf("offline queues lost FIFO convergence: positions=%v", positions)
	}
	duplicate, err := service.Submit(context.Background(), queues[0][0])
	if err != nil || !duplicate.Duplicate || duplicate.LatestSequence != positions["A1"] {
		t.Fatalf("offline replay was not idempotent: receipt=%+v err=%v", duplicate, err)
	}
}
