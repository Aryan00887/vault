package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	s, err := NewStore(root, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func requestObject(s *Store, method, key string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/objects/"+key, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer test-token")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.api(w, r)
	return w
}

func TestStreamingWriteReadAndConditionalUpdate(t *testing.T) {
	s := testStore(t)
	data := bytes.Repeat([]byte("vault-chunk-check"), (chunkSize*2)/17+31)
	put := requestObject(s, http.MethodPut, "logs/archive", data, nil)
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status %d: %s", put.Code, put.Body.String())
	}
	var m Manifest
	if err := json.Unmarshal(put.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Replicas) != 3 || len(m.Chunks) < 2 {
		t.Fatalf("expected three durable replicas and chunking; got replicas=%v chunks=%d", m.Replicas, len(m.Chunks))
	}
	got := requestObject(s, http.MethodGet, "logs/archive", nil, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("GET status %d: %s", got.Code, got.Body.String())
	}
	if !bytes.Equal(got.Body.Bytes(), data) {
		t.Fatalf("GET body differs from PUT body")
	}
	stale := requestObject(s, http.MethodPut, "logs/archive", []byte("stale"), map[string]string{"If-Match": "\"99\""})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale conditional write returned %d", stale.Code)
	}
	update := requestObject(s, http.MethodPut, "logs/archive", []byte("next"), map[string]string{"If-Match": "\"1\""})
	if update.Code != http.StatusOK {
		t.Fatalf("conditional update status %d: %s", update.Code, update.Body.String())
	}
	if got = requestObject(s, http.MethodGet, "logs/archive", nil, nil); got.Body.String() != "next" {
		t.Fatalf("read after update = %q", got.Body.String())
	}
}

func TestWriteRequiresDurabilityThreshold(t *testing.T) {
	s := testStore(t)
	s.nodes[1].State = "draining"
	s.nodes[2].State = "draining"
	w := requestObject(s, http.MethodPut, "key", []byte("value"), nil)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("write without quorum returned %d", w.Code)
	}
	if len(s.disk.Objects) != 0 {
		t.Fatal("failed write became visible")
	}
}

func TestSingleNodeFailureStillMeetsTwoCopyWritePolicy(t *testing.T) {
	s := testStore(t)
	s.nodes[0].State = "draining"
	w := requestObject(s, http.MethodPut, "available", []byte("survives one node"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("write with one failed node returned %d: %s", w.Code, w.Body.String())
	}
	if len(s.disk.Objects["available"].Replicas) != 2 {
		t.Fatalf("expected two durable copies, got %v", s.disk.Objects["available"].Replicas)
	}
}

func TestConcurrentConditionalWritesHaveOneWinner(t *testing.T) {
	s := testStore(t)
	if w := requestObject(s, http.MethodPut, "race", []byte("first"), nil); w.Code != http.StatusOK {
		t.Fatalf("initial PUT status %d", w.Code)
	}
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, body := range [][]byte{[]byte("left"), []byte("right")} {
		wg.Add(1)
		go func(body []byte) {
			defer wg.Done()
			w := requestObject(s, http.MethodPut, "race", body, map[string]string{"If-Match": "\"1\""})
			results <- w.Code
		}(body)
	}
	wg.Wait()
	close(results)
	wins, stale := 0, 0
	for code := range results {
		if code == http.StatusOK {
			wins++
		} else if code == http.StatusPreconditionFailed {
			stale++
		}
	}
	if wins != 1 || stale != 1 {
		t.Fatalf("conditional race outcomes: %d winners, %d stale", wins, stale)
	}
}

func TestReplicaCorruptionFallsBackToVerifiedCopy(t *testing.T) {
	s := testStore(t)
	body := []byte("replicated payload")
	w := requestObject(s, http.MethodPut, "key", body, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status %d", w.Code)
	}
	m := s.disk.Objects["key"]
	if err := os.WriteFile(objectDir(s.root, m.Replicas[0], m.Key, m.Generation)+"/00000000.chunk", []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	got := requestObject(s, http.MethodGet, "key", nil, nil)
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), body) {
		t.Fatalf("verified fallback failed: %d %q", got.Code, got.Body.String())
	}
}

func TestRepairPassReplacesCorruptedReplica(t *testing.T) {
	s := testStore(t)
	body := []byte("repair this copy")
	if w := requestObject(s, http.MethodPut, "repair-key", body, nil); w.Code != http.StatusOK {
		t.Fatalf("PUT status %d", w.Code)
	}
	m := s.disk.Objects["repair-key"]
	if err := os.WriteFile(filepath.Join(objectDir(s.root, m.Replicas[0], m.Key, m.Generation), m.Chunks[0].Name), []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	s.repairPass()
	repaired := s.disk.Objects["repair-key"]
	if len(repaired.Replicas) != 3 || !s.verifyReplica(m.Replicas[0], repaired) {
		t.Fatalf("repair did not restore all verified copies: %v", repaired.Replicas)
	}
	if s.repairs == 0 {
		t.Fatal("repair counter did not advance")
	}
}

func TestObjectAPIRequiresBearerToken(t *testing.T) {
	s := testStore(t)
	r := httptest.NewRequest(http.MethodPut, "/api/objects/key", strings.NewReader("data"))
	w := httptest.NewRecorder()
	s.api(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated API returned %d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPut, "/api/objects/key", strings.NewReader("data"))
	r.Header.Set("Authorization", "Bearer test-token")
	w = httptest.NewRecorder()
	s.api(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("authenticated API returned %d", w.Code)
	}
}

func TestManifestReplicationSurvivesRestart(t *testing.T) {
	root := t.TempDir()
	s, err := NewStore(root, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	if w := requestObject(s, http.MethodPut, "persist", []byte("durable"), nil); w.Code != http.StatusOK {
		t.Fatalf("PUT status %d", w.Code)
	}
	reloaded, err := NewStore(root, "test-token")
	if err != nil {
		t.Fatal(err)
	}
	got := requestObject(reloaded, http.MethodGet, "persist", nil, nil)
	if got.Code != http.StatusOK {
		t.Fatalf("read after restart %d: %s", got.Code, got.Body.String())
	}
	b, err := io.ReadAll(got.Result().Body)
	if err != nil || string(b) != "durable" {
		t.Fatalf("read after restart body %q err %v", b, err)
	}
}
