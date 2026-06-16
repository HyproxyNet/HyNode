package state

import (
	"context"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"hynode/internal/panel"

	"golang.org/x/time/rate"
)

const (
	// shardCount is the number of mutex shards. Must be a power of 2.
	// Higher values reduce contention at the cost of more memory.
	shardCount = 64
	// shardMask is shardCount - 1, used for fast modulo via bitwise AND.
	shardMask = shardCount - 1
)

// shard holds a subset of user state under its own lock.
type shard struct {
	mu    sync.RWMutex
	users map[int64]*userState
}

type Store struct {
	// Sharded user state for reduced lock contention.
	shards [shardCount]shard

	// bySecret maps UUID -> user ID. Protected by its own lock.
	secretMu   sync.RWMutex
	bySecret   map[string]int64

	// usage tracks per-node, per-user traffic. Protected by usageMu.
	usageMu sync.RWMutex
	usage   map[string]map[int64]*usageState

	// trackMu protects tracked set for the shared background flusher.
	trackMu    sync.Mutex
	tracked    map[*TrackedConn]struct{}
	flusherRun atomic.Bool // true while the background flusher goroutine is active

	ttl     time.Duration
	started time.Time
	total   atomic.Int64
	active  atomic.Int64
}

type userState struct {
	policy  panel.User
	ips     map[string]time.Time
	limiter *rate.Limiter
}

type usageState struct {
	upload   int64
	download int64
	active   int
}

type Snapshot struct {
	Traffic map[string][2]int64
	Alive   map[string][]string
	Online  map[string]int
}

func New() *Store {
	s := &Store{
		bySecret: make(map[string]int64),
		usage:    make(map[string]map[int64]*usageState),
		tracked:  make(map[*TrackedConn]struct{}),
		ttl:      5 * time.Minute,
		started:  time.Now(),
	}
	for i := range s.shards {
		s.shards[i].users = make(map[int64]*userState)
	}
	return s
}

// getShard returns the shard for a given user ID.
func (s *Store) getShard(userID int64) *shard {
	// Use absolute value modulo shardCount for even distribution.
	idx := userID & shardMask
	if idx < 0 {
		idx = -idx
	}
	return &s.shards[idx]
}

func (s *Store) ReplaceUsers(users []panel.User) {
	// Build new secret map outside any lock.
	newSecret := make(map[string]int64, len(users))
	for _, u := range users {
		newSecret[u.UUID] = u.ID
	}

	// Swap secret map.
	s.secretMu.Lock()
	s.bySecret = newSecret
	s.secretMu.Unlock()

	// Update per-user state in shards.
	for _, user := range users {
		sh := s.getShard(user.ID)
		sh.mu.Lock()
		current := sh.users[user.ID]
		if current == nil {
			current = &userState{ips: make(map[string]time.Time)}
			sh.users[user.ID] = current
		}
		current.policy = user
		current.limiter = limiterFor(user.SpeedLimit)
		sh.mu.Unlock()
	}
}

func (s *Store) UserBySecret(secret string) (int64, bool) {
	s.secretMu.RLock()
	id, ok := s.bySecret[secret]
	s.secretMu.RUnlock()
	return id, ok
}

func (s *Store) Open(nodeID string, userID int64, source string) bool {
	sh := s.getShard(userID)
	sh.mu.Lock()
	current := sh.users[userID]
	if current == nil {
		sh.mu.Unlock()
		return false
	}
	now := time.Now()
	s.pruneLocked(current, now)
	if source != "" {
		if _, exists := current.ips[source]; !exists &&
			current.policy.DeviceLimit > 0 &&
			len(current.ips) >= current.policy.DeviceLimit {
			sh.mu.Unlock()
			return false
		}
		current.ips[source] = now
	}
	sh.mu.Unlock()

	// Usage tracking under separate lock.
	s.usageMu.Lock()
	usage := s.nodeUsageLocked(nodeID, userID)
	usage.active++
	s.usageMu.Unlock()

	s.active.Add(1)
	s.total.Add(1)
	return true
}

func (s *Store) Close(nodeID string, userID int64) {
	s.usageMu.Lock()
	if usage := s.nodeUsageLocked(nodeID, userID); usage.active > 0 {
		usage.active--
		s.active.Add(-1)
	}
	s.usageMu.Unlock()
}

func (s *Store) Add(nodeID string, userID, upload, download int64) {
	// Only check existence in shard (fast path).
	sh := s.getShard(userID)
	sh.mu.RLock()
	exists := sh.users[userID] != nil
	sh.mu.RUnlock()
	if !exists {
		return
	}

	s.usageMu.Lock()
	usage := s.nodeUsageLocked(nodeID, userID)
	usage.upload += upload
	usage.download += download
	s.usageMu.Unlock()
}

func (s *Store) Wait(userID int64, n int) {
	sh := s.getShard(userID)
	sh.mu.RLock()
	current := sh.users[userID]
	var limiter *rate.Limiter
	if current != nil {
		limiter = current.limiter
	}
	sh.mu.RUnlock()

	if limiter == nil || n <= 0 {
		return
	}
	for n > 0 {
		part := n
		if part > 64*1024 {
			part = 64 * 1024
		}
		_ = limiter.WaitN(context.Background(), part)
		n -= part
	}
}

func (s *Store) Snapshot(nodeID string) Snapshot {
	now := time.Now()
	result := Snapshot{
		Traffic: make(map[string][2]int64),
		Alive:   make(map[string][]string),
		Online:  make(map[string]int),
	}

	// Collect alive IPs from all shards.
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		for id, current := range sh.users {
			// Prune expired IPs (copy-on-write: only lock shard for read).
			alive := make([]string, 0, len(current.ips))
			for ip, last := range current.ips {
				if now.Sub(last) <= s.ttl {
					alive = append(alive, ip)
				}
			}
			if len(alive) > 0 {
				key := strconv.FormatInt(id, 10)
				result.Alive[key] = alive
			}
		}
		sh.mu.RUnlock()
	}

	// Collect traffic and online counts.
	s.usageMu.RLock()
	nodeUsage := s.usage[nodeID]
	for id, usage := range nodeUsage {
		key := strconv.FormatInt(id, 10)
		if usage.upload > 0 || usage.download > 0 {
			result.Traffic[key] = [2]int64{usage.upload, usage.download}
		}
		if usage.active > 0 {
			result.Online[key] = usage.active
		}
	}
	s.usageMu.RUnlock()

	return result
}

func (s *Store) Commit(nodeID string, snapshot Snapshot) {
	s.usageMu.Lock()
	defer s.usageMu.Unlock()
	for key, value := range snapshot.Traffic {
		id, _ := strconv.ParseInt(key, 10, 64)
		node := s.usage[nodeID]
		if node == nil {
			continue
		}
		usage := node[id]
		if usage == nil {
			continue
		}
		usage.upload -= min(usage.upload, value[0])
		usage.download -= min(usage.download, value[1])
	}
}

func (s *Store) Metrics() map[string]any {
	// Count total users across all shards (no global lock needed).
	totalUsers := 0
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.RLock()
		totalUsers += len(sh.users)
		sh.mu.RUnlock()
	}
	return map[string]any{
		"uptime":             int64(time.Since(s.started).Seconds()),
		"active_connections": s.active.Load(),
		"total_connections":  s.total.Load(),
		"total_users":        totalUsers,
	}
}

func (s *Store) pruneLocked(current *userState, now time.Time) {
	for ip, last := range current.ips {
		if now.Sub(last) > s.ttl {
			delete(current.ips, ip)
		}
	}
}

func (s *Store) nodeUsageLocked(nodeID string, userID int64) *usageState {
	node := s.usage[nodeID]
	if node == nil {
		node = make(map[int64]*usageState)
		s.usage[nodeID] = node
	}
	current := node[userID]
	if current == nil {
		current = &usageState{}
		node[userID] = current
	}
	return current
}

func limiterFor(mbps int) *rate.Limiter {
	if mbps <= 0 {
		return nil
	}
	bytesPerSecond := rate.Limit(int64(mbps) * 1000 * 1000 / 8)
	return rate.NewLimiter(bytesPerSecond, max(64*1024, int(bytesPerSecond)))
}

// ─── Shared background flusher ─────────────────────────────────────────

// flushLoop runs as a single background goroutine that periodically flushes
// batched traffic counters from ALL tracked connections. This replaces the
// previous per-connection goroutine model, dramatically reducing goroutine
// count under high-concurrency scenarios (e.g. 10000 connections = 1 goroutine
// instead of 10000).
func (s *Store) flushLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		s.trackMu.Lock()
		for tc := range s.tracked {
			tc.flush()
		}
		s.trackMu.Unlock()
	}
}

// registerConn adds a TrackedConn to the shared flusher and ensures the
// background flusher goroutine is running.
func (s *Store) registerConn(tc *TrackedConn) {
	s.trackMu.Lock()
	s.tracked[tc] = struct{}{}
	needStart := s.flusherRun.CompareAndSwap(false, true)
	s.trackMu.Unlock()
	if needStart {
		go s.flushLoop()
	}
}

// unregisterConn removes a TrackedConn from the shared flusher.
func (s *Store) unregisterConn(tc *TrackedConn) {
	s.trackMu.Lock()
	delete(s.tracked, tc)
	if len(s.tracked) == 0 {
		s.flusherRun.Store(false)
	}
	s.trackMu.Unlock()
}

// ─── TrackedConn ────────────────────────────────────────────────────────

// TrackedConn wraps a net.Conn to track traffic and enforce rate limits.
// Uses batched traffic accounting: traffic is accumulated in local atomic
// counters and flushed periodically by the store's shared background flusher.
// This eliminates one goroutine per connection, significantly reducing overhead
// under high-concurrency multi-user scenarios.
type TrackedConn struct {
	net.Conn
	store  *Store
	nodeID string
	userID int64

	// Batched traffic counters (goroutine-local, no lock needed).
	pendingUpload   atomic.Int64
	pendingDownload atomic.Int64

	// closed ensures final flush happens exactly once.
	closed atomic.Bool
}

func WrapConn(conn net.Conn, store *Store, nodeID string, userID int64) net.Conn {
	tc := &TrackedConn{Conn: conn, store: store, nodeID: nodeID, userID: userID}
	store.registerConn(tc)
	return tc
}

func (c *TrackedConn) flush() {
	if c.closed.Load() {
		return
	}
	up := c.pendingUpload.Swap(0)
	down := c.pendingDownload.Swap(0)
	if up > 0 || down > 0 {
		c.store.Add(c.nodeID, c.userID, up, down)
	}
}

func (c *TrackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.store.Wait(c.userID, n)
		c.pendingUpload.Add(int64(n))
	}
	return n, err
}

func (c *TrackedConn) Write(p []byte) (int, error) {
	c.store.Wait(c.userID, len(p))
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.pendingDownload.Add(int64(n))
	}
	return n, err
}

func (c *TrackedConn) Close() error {
	if c.closed.CompareAndSwap(false, true) {
		c.flush() // Final flush before closing.
		c.store.unregisterConn(c)
		c.store.Close(c.nodeID, c.userID)
	}
	return c.Conn.Close()
}

func IPFromAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err == nil {
		return host
	}
	return addr.String()
}
