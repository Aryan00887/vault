package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const chunkSize = 4 << 20

type Chunk struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Key         string    `json:"key"`
	Generation  uint64    `json:"generation"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	ContentType string    `json:"contentType"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Chunks      []Chunk   `json:"chunks"`
	Replicas    []string  `json:"replicas"`
}
type Node struct {
	ID        string `json:"id"`
	Domain    string `json:"domain"`
	State     string `json:"state"`
	UsedBytes int64  `json:"usedBytes"`
	Objects   int    `json:"objects"`
}
type Policy struct {
	Replicas  int `json:"replicas"`
	WriteAcks int `json:"writeAcks"`
}
type DiskState struct {
	Objects map[string]Manifest `json:"objects"`
	Policy  Policy              `json:"policy"`
}
type Store struct {
	mu                sync.RWMutex
	root, token       string
	nodes             []Node
	disk              DiskState
	requests, repairs uint64
	repairBacklog     int
	repairErr         string
	started           time.Time
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func hash(b []byte) string  { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func safeKey(k string) bool { return k != "" && len(k) <= 1024 && !strings.ContainsRune(k, 0) }
func NewStore(root, token string) (*Store, error) {
	s := &Store{root: root, token: token, started: time.Now(), nodes: []Node{{ID: "node-01", Domain: "rack-a", State: "healthy"}, {ID: "node-02", Domain: "rack-b", State: "healthy"}, {ID: "node-03", Domain: "rack-c", State: "healthy"}}}
	s.disk = DiskState{Objects: map[string]Manifest{}, Policy: Policy{Replicas: 3, WriteAcks: 2}}
	if b, err := os.ReadFile(filepath.Join(root, "metadata.json")); err == nil {
		if err = json.Unmarshal(b, &s.disk); err != nil {
			return nil, fmt.Errorf("read metadata: %w", err)
		}
		if s.disk.Objects == nil {
			s.disk.Objects = map[string]Manifest{}
		}
		if s.disk.Policy.Replicas < 1 {
			s.disk.Policy = Policy{3, 2}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, n := range s.nodes {
		if err := os.MkdirAll(filepath.Join(root, n.ID), 0755); err != nil {
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) persistLocked() error {
	b, e := json.MarshalIndent(s.disk, "", "  ")
	if e != nil {
		return e
	}
	tmp := filepath.Join(s.root, "metadata.tmp")
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, filepath.Join(s.root, "metadata.json"))
}
func (s *Store) admin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, "operator authentication required", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func objectDir(root, node, key string, generation uint64) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(root, node, hex.EncodeToString(sum[:]), strconv.FormatUint(generation, 10))
}
func removeObject(root, node, key string, generation uint64) {
	_ = os.RemoveAll(objectDir(root, node, key, generation))
}
func (s *Store) nodesLocked() []Node {
	out := append([]Node(nil), s.nodes...)
	for i := range out {
		out[i].UsedBytes = 0
		out[i].Objects = 0
		for _, m := range s.disk.Objects {
			for _, rep := range m.Replicas {
				if rep == out[i].ID {
					out[i].Objects++
					out[i].UsedBytes += m.Size
				}
			}
		}
	}
	return out
}
func (s *Store) chooseLocked(want int) []string {
	domains := map[string]bool{}
	out := []string{}
	used := map[string]bool{}
	for _, n := range s.nodes {
		if len(out) >= want {
			break
		}
		if n.State == "healthy" && !domains[n.Domain] {
			out = append(out, n.ID)
			domains[n.Domain] = true
			used[n.ID] = true
		}
	}
	for _, n := range s.nodes {
		if len(out) >= want {
			break
		}
		if n.State == "healthy" && !used[n.ID] {
			out = append(out, n.ID)
			used[n.ID] = true
		}
	}
	return out
}
func (s *Store) writeReplica(node, key string, generation uint64, chunks []Chunk, body io.Reader) error {
	dir := objectDir(s.root, node, key, generation)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, c := range chunks {
		dst, e := os.Create(filepath.Join(dir, c.Name))
		if e != nil {
			return e
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(dst, h), io.LimitReader(body, c.Size))
		syncErr := dst.Sync()
		closeErr := dst.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != c.Size || hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
			return errors.New("source chunk checksum mismatch")
		}
	}
	return nil
}

type multiFileReader struct {
	io.Reader
	files []*os.File
}

func (m *multiFileReader) Close() error {
	var first error
	for _, f := range m.files {
		if e := f.Close(); e != nil && first == nil {
			first = e
		}
	}
	return first
}
func (s *Store) readReplica(m Manifest) (io.ReadCloser, string, error) {
	for _, id := range m.Replicas {
		s.mu.RLock()
		healthy := false
		for _, n := range s.nodes {
			if n.ID == id && n.State == "healthy" {
				healthy = true
			}
		}
		s.mu.RUnlock()
		if !healthy {
			continue
		}
		whole := sha256.New()
		valid := true
		for _, c := range m.Chunks {
			f, e := os.Open(filepath.Join(objectDir(s.root, id, m.Key, m.Generation), c.Name))
			if e != nil {
				valid = false
				break
			}
			ch := sha256.New()
			n, e := io.Copy(io.MultiWriter(whole, ch), io.LimitReader(f, c.Size))
			ce := f.Close()
			if e != nil || ce != nil || n != c.Size || hex.EncodeToString(ch.Sum(nil)) != c.SHA256 {
				valid = false
				break
			}
		}
		if !valid || hex.EncodeToString(whole.Sum(nil)) != m.SHA256 {
			continue
		}
		files := make([]*os.File, 0, len(m.Chunks))
		readers := make([]io.Reader, 0, len(m.Chunks))
		for _, c := range m.Chunks {
			f, e := os.Open(filepath.Join(objectDir(s.root, id, m.Key, m.Generation), c.Name))
			if e != nil {
				valid = false
				break
			}
			files = append(files, f)
			readers = append(readers, io.LimitReader(f, c.Size))
		}
		if !valid {
			for _, f := range files {
				_ = f.Close()
			}
			continue
		}
		return &multiFileReader{Reader: io.MultiReader(readers...), files: files}, id, nil
	}
	return nil, "", errors.New("no verified replica is available")
}

func (s *Store) put(w http.ResponseWriter, r *http.Request, key string) {
	if !safeKey(key) {
		http.Error(w, "invalid object key", 400)
		return
	}
	stage, e := os.CreateTemp(s.root, "incoming-*.tmp")
	if e != nil {
		http.Error(w, "could not stage upload", 500)
		return
	}
	stageName := stage.Name()
	defer os.Remove(stageName)
	h := sha256.New()
	var chunks []Chunk
	var size int64
	buf := make([]byte, chunkSize)
	for {
		n, re := io.ReadFull(r.Body, buf)
		if n > 0 {
			part := buf[:n]
			if _, e = stage.Write(part); e != nil {
				_ = stage.Close()
				http.Error(w, "could not stage upload", 500)
				return
			}
			_, _ = h.Write(part)
			size += int64(n)
			chunks = append(chunks, Chunk{Name: fmt.Sprintf("%08d.chunk", len(chunks)), Size: int64(n), SHA256: hash(part)})
		}
		if re == io.EOF || re == io.ErrUnexpectedEOF {
			break
		}
		if re != nil {
			_ = stage.Close()
			http.Error(w, re.Error(), 400)
			return
		}
	}
	if e = stage.Sync(); e != nil {
		_ = stage.Close()
		http.Error(w, "could not sync staged upload", 500)
		return
	}
	if e = stage.Close(); e != nil {
		http.Error(w, "could not close staged upload", 500)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	old, exists := s.disk.Objects[key]
	if v := r.Header.Get("If-Match"); v != "" && (!exists || strings.Trim(v, "\"") != strconv.FormatUint(old.Generation, 10)) {
		http.Error(w, "generation precondition failed", 412)
		return
	}
	if r.Header.Get("If-None-Match") == "*" && exists {
		http.Error(w, "object already exists", 412)
		return
	}
	policy := s.disk.Policy
	if policy.Replicas < 1 || policy.WriteAcks < 1 || policy.WriteAcks > policy.Replicas {
		http.Error(w, "invalid replication policy", 500)
		return
	}
	targets := s.chooseLocked(policy.Replicas)
	if len(targets) < policy.WriteAcks {
		http.Error(w, "insufficient healthy nodes for write quorum", 503)
		return
	}
	m := Manifest{Key: key, Generation: old.Generation + 1, Size: size, SHA256: hex.EncodeToString(h.Sum(nil)), ContentType: r.Header.Get("Content-Type"), UpdatedAt: time.Now().UTC(), Chunks: chunks}
	successful := []string{}
	for _, id := range targets {
		src, openErr := os.Open(stageName)
		if openErr != nil {
			continue
		}
		copyErr := s.writeReplica(id, key, m.Generation, chunks, src)
		_ = src.Close()
		if copyErr == nil {
			successful = append(successful, id)
		} else {
			removeObject(s.root, id, key, m.Generation)
		}
	}
	if len(successful) < policy.WriteAcks {
		for _, id := range successful {
			removeObject(s.root, id, key, m.Generation)
		}
		http.Error(w, "durable acknowledgement threshold not met", 503)
		return
	}
	m.Replicas = successful
	s.disk.Objects[key] = m
	if err := s.persistLocked(); err != nil {
		if exists {
			s.disk.Objects[key] = old
		} else {
			delete(s.disk.Objects, key)
		}
		for _, id := range successful {
			removeObject(s.root, id, key, m.Generation)
		}
		http.Error(w, "metadata commit failed", 500)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(m.Generation, 10)))
	jsonOut(w, m)
}
func (s *Store) object(w http.ResponseWriter, r *http.Request, key string) {
	if !safeKey(key) {
		http.Error(w, "invalid object key", 400)
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.put(w, r, key)
		return
	case http.MethodDelete:
		s.mu.Lock()
		defer s.mu.Unlock()
		m, ok := s.disk.Objects[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if v := r.Header.Get("If-Match"); v != "" && strings.Trim(v, "\"") != strconv.FormatUint(m.Generation, 10) {
			http.Error(w, "generation precondition failed", 412)
			return
		}
		delete(s.disk.Objects, key)
		if e := s.persistLocked(); e != nil {
			s.disk.Objects[key] = m
			http.Error(w, "metadata commit failed", 500)
			return
		}
		for _, id := range m.Replicas {
			removeObject(s.root, id, key, m.Generation)
		}
		s.requests++
		w.WriteHeader(204)
		return
	case http.MethodGet, http.MethodHead:
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		http.Error(w, "method not allowed", 405)
		return
	}
	s.mu.RLock()
	m, ok := s.disk.Objects[key]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(m.Generation, 10)))
	w.Header().Set("X-Content-SHA256", m.SHA256)
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(m.Size, 10))
	if r.Method == http.MethodHead {
		return
	}
	rc, _, err := s.readReplica(m)
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	defer rc.Close()
	_, _ = io.Copy(w, rc)
}
func (s *Store) api(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api/")
	if p == "health" && r.Method == http.MethodGet {
		jsonOut(w, map[string]any{"status": "ok", "uptimeSeconds": int64(time.Since(s.started).Seconds())})
		return
	}
	if !strings.HasPrefix(p, "objects/") {
		http.NotFound(w, r)
		return
	}
	key := strings.TrimPrefix(p, "objects/")
	s.admin(func(w http.ResponseWriter, r *http.Request) { s.object(w, r, key) })(w, r)
}
func (s *Store) dashboard(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	nodes := s.nodesLocked()
	healthy := 0
	var used int64
	for _, n := range nodes {
		if n.State == "healthy" {
			healthy++
		}
		used += n.UsedBytes
	}
	state := "HEALTHY"
	if healthy < len(s.nodes) {
		state = "DEGRADED"
	}
	if healthy == 0 {
		state = "OFFLINE"
	}
	jsonOut(w, map[string]any{"cluster": map[string]any{"state": state, "nodesHealthy": healthy, "nodesTotal": len(nodes), "objects": len(s.disk.Objects), "usedBytes": used, "uptimeSeconds": int64(time.Since(s.started).Seconds())}, "nodes": nodes, "policy": s.disk.Policy, "repairBacklog": s.repairBacklog, "repairCompleted": s.repairs, "repairError": s.repairErr, "requests": s.requests})
}
func (s *Store) policy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.mu.RLock()
		defer s.mu.RUnlock()
		jsonOut(w, s.disk.Policy)
		return
	}
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", 405)
		return
	}
	var p Policy
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.Replicas < 1 || p.Replicas > len(s.nodes) || p.WriteAcks < 1 || p.WriteAcks > p.Replicas {
		http.Error(w, "replicas and writeAcks must satisfy 1 <= writeAcks <= replicas <= 3", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.disk.Policy
	s.disk.Policy = p
	if e := s.persistLocked(); e != nil {
		s.disk.Policy = prev
		http.Error(w, "could not persist policy", 500)
		return
	}
	jsonOut(w, p)
}
func (s *Store) nodeAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/nodes/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i := range s.nodes {
		if s.nodes[i].ID == parts[0] {
			switch parts[1] {
			case "drain":
				s.nodes[i].State = "draining"
			case "restore":
				s.nodes[i].State = "healthy"
			default:
				http.NotFound(w, r)
				return
			}
			found = true
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	jsonOut(w, s.nodesLocked())
}
func (s *Store) repairLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.repairPass()
	}
}

func (s *Store) repairPass() {
	s.mu.Lock()
	s.repairBacklog = 0
	for key, m := range s.disk.Objects {
		valid := make([]string, 0, len(m.Replicas))
		for _, id := range m.Replicas {
			if s.nodeHealthyLocked(id) && s.verifyReplica(id, m) {
				valid = append(valid, id)
			}
		}
		m.Replicas = valid
		if len(valid) > s.disk.Policy.Replicas {
			m.Replicas = valid[:s.disk.Policy.Replicas]
		}
		for _, id := range valid[safeMin(len(valid), s.disk.Policy.Replicas):] {
			removeObject(s.root, id, key, m.Generation)
		}
		if len(m.Replicas) < s.disk.Policy.Replicas {
			source := ""
			if len(m.Replicas) > 0 {
				source = m.Replicas[0]
			}
			for _, target := range s.chooseLocked(s.disk.Policy.Replicas) {
				if contains(m.Replicas, target) {
					continue
				}
				if source == "" {
					break
				}
				if err := s.copyReplica(source, target, key, m); err != nil {
					s.repairErr = err.Error()
					continue
				}
				m.Replicas = append(m.Replicas, target)
				s.repairs++
				s.repairErr = ""
				if len(m.Replicas) >= s.disk.Policy.Replicas {
					break
				}
			}
		}
		if len(m.Replicas) < s.disk.Policy.Replicas {
			s.repairBacklog++
		}
		s.disk.Objects[key] = m
	}
	_ = s.persistLocked()
	s.mu.Unlock()
}

func safeMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
func (s *Store) nodeHealthyLocked(id string) bool {
	for _, n := range s.nodes {
		if n.ID == id {
			return n.State == "healthy"
		}
	}
	return false
}
func (s *Store) verifyReplica(node string, m Manifest) bool {
	whole := sha256.New()
	for _, c := range m.Chunks {
		f, e := os.Open(filepath.Join(objectDir(s.root, node, m.Key, m.Generation), c.Name))
		if e != nil {
			return false
		}
		ch := sha256.New()
		n, e := io.Copy(io.MultiWriter(whole, ch), io.LimitReader(f, c.Size))
		ce := f.Close()
		if e != nil || ce != nil || n != c.Size || hex.EncodeToString(ch.Sum(nil)) != c.SHA256 {
			return false
		}
	}
	return hex.EncodeToString(whole.Sum(nil)) == m.SHA256
}
func (s *Store) copyReplica(source, target, key string, m Manifest) error {
	dir := objectDir(s.root, target, key, m.Generation)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for _, c := range m.Chunks {
		src, e := os.Open(filepath.Join(objectDir(s.root, source, key, m.Generation), c.Name))
		if e != nil {
			return e
		}
		dst, e := os.Create(filepath.Join(dir, c.Name))
		if e != nil {
			_ = src.Close()
			return e
		}
		h := sha256.New()
		n, e := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, c.Size))
		_ = src.Close()
		if e == nil {
			e = dst.Sync()
		}
		_ = dst.Close()
		if e != nil {
			return e
		}
		if n != c.Size || hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
			return errors.New("repair checksum verification failed")
		}
	}
	return nil
}
func main() {
	root := env("VAULT_DATA_DIR", "./vault-data")
	if err := os.MkdirAll(root, 0755); err != nil {
		log.Fatal(err)
	}
	s, err := NewStore(root, env("VAULT_ADMIN_TOKEN", "dev-vault-token"))
	if err != nil {
		log.Fatal(err)
	}
	go s.repairLoop()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", s.api)
	mux.HandleFunc("/api/ops/overview", s.admin(s.dashboard))
	mux.HandleFunc("/api/ops/policy", s.admin(s.policy))
	mux.HandleFunc("/api/nodes/", s.admin(s.nodeAction))
	mux.Handle("/", http.FileServer(http.Dir("./dist")))
	addr := env("VAULT_ADDR", ":8080")
	log.Printf("Vault listening on %s (data: %s)", addr, root)
	log.Fatal(http.ListenAndServe(addr, mux))
}
