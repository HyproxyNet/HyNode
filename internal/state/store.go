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

type Store struct {
	mu       sync.RWMutex
	users    map[int64]*userState
	usage    map[string]map[int64]*usageState
	bySecret map[string]int64
	ttl      time.Duration
	started  time.Time
	total    atomic.Int64
	active   atomic.Int64
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
	return &Store{
		users:    make(map[int64]*userState),
		usage:    make(map[string]map[int64]*usageState),
		bySecret: make(map[string]int64),
		ttl:      5 * time.Minute,
		started:  time.Now(),
	}
}

func (s *Store) ReplaceUsers(users []panel.User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range users {
		s.bySecret[user.UUID] = user.ID
		current := s.users[user.ID]
		if current == nil {
			current = &userState{ips: make(map[string]time.Time)}
			s.users[user.ID] = current
		}
		current.policy = user
		current.limiter = limiterFor(user.SpeedLimit)
	}
}

func (s *Store) UserBySecret(secret string) (int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.bySecret[secret]
	return id, ok
}

func (s *Store) Open(nodeID string, userID int64, source string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.users[userID]
	if current == nil {
		return false
	}
	now := time.Now()
	s.pruneLocked(current, now)
	if source != "" {
		if _, exists := current.ips[source]; !exists &&
			current.policy.DeviceLimit > 0 &&
			len(current.ips) >= current.policy.DeviceLimit {
			return false
		}
		current.ips[source] = now
	}
	usage := s.nodeUsageLocked(nodeID, userID)
	usage.active++
	s.active.Add(1)
	s.total.Add(1)
	return true
}

func (s *Store) Close(nodeID string, userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if usage := s.nodeUsageLocked(nodeID, userID); usage.active > 0 {
		usage.active--
		s.active.Add(-1)
	}
}

func (s *Store) Add(nodeID string, userID, upload, download int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users[userID] != nil {
		usage := s.nodeUsageLocked(nodeID, userID)
		usage.upload += upload
		usage.download += download
	}
}

func (s *Store) Wait(userID int64, n int) {
	s.mu.RLock()
	current := s.users[userID]
	var limiter *rate.Limiter
	if current != nil {
		limiter = current.limiter
	}
	s.mu.RUnlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	result := Snapshot{
		Traffic: make(map[string][2]int64),
		Alive:   make(map[string][]string),
		Online:  make(map[string]int),
	}
	for id, current := range s.users {
		s.pruneLocked(current, now)
		key := strconv.FormatInt(id, 10)
		usage := s.nodeUsageLocked(nodeID, id)
		if usage.upload > 0 || usage.download > 0 {
			result.Traffic[key] = [2]int64{usage.upload, usage.download}
		}
		if len(current.ips) > 0 {
			ips := make([]string, 0, len(current.ips))
			for ip := range current.ips {
				ips = append(ips, ip)
			}
			result.Alive[key] = ips
		}
		if usage.active > 0 {
			result.Online[key] = usage.active
		}
	}
	return result
}

func (s *Store) Commit(nodeID string, snapshot Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, value := range snapshot.Traffic {
		id, _ := strconv.ParseInt(key, 10, 64)
		if s.users[id] == nil {
			continue
		}
		usage := s.nodeUsageLocked(nodeID, id)
		usage.upload -= min(usage.upload, value[0])
		usage.download -= min(usage.download, value[1])
	}
}

func (s *Store) Metrics() map[string]any {
	s.mu.RLock()
	totalUsers := len(s.users)
	s.mu.RUnlock()
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

type TrackedConn struct {
	net.Conn
	store  *Store
	nodeID string
	userID int64
	once   sync.Once
}

func WrapConn(conn net.Conn, store *Store, nodeID string, userID int64) net.Conn {
	return &TrackedConn{Conn: conn, store: store, nodeID: nodeID, userID: userID}
}

func (c *TrackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.store.Wait(c.userID, n)
		c.store.Add(c.nodeID, c.userID, int64(n), 0)
	}
	return n, err
}

func (c *TrackedConn) Write(p []byte) (int, error) {
	c.store.Wait(c.userID, len(p))
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.store.Add(c.nodeID, c.userID, 0, int64(n))
	}
	return n, err
}

func (c *TrackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.store.Close(c.nodeID, c.userID) })
	return err
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
