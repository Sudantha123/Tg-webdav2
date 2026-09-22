package store

import (
	"container/list"
	"sync"
)

// Cache is a bounded LRU byte cache for downloaded telegram blocks.
type Cache struct {
	mu    sync.Mutex
	max   int64
	size  int64
	items map[string]*list.Element
	lru   *list.List // front = most recently used
}

type cacheItem struct {
	key  string
	data []byte
}

// NewCache creates a cache limited to maxBytes of payload.
func NewCache(maxBytes int64) *Cache {
	if maxBytes < 1<<20 {
		maxBytes = 1 << 20
	}
	return &Cache{
		max:   maxBytes,
		items: make(map[string]*list.Element),
		lru:   list.New(),
	}
}

// Get returns a cached block. The returned slice is shared — callers must
// not modify it.
func (c *Cache) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	c.lru.MoveToFront(el)
	return el.Value.(*cacheItem).data, true
}

// Add stores a block, evicting least recently used items as needed.
func (c *Cache) Add(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if int64(len(data)) > c.max {
		return // never fits
	}
	if el, ok := c.items[key]; ok {
		c.size -= int64(len(el.Value.(*cacheItem).data))
		el.Value.(*cacheItem).data = data
		c.size += int64(len(data))
		c.lru.MoveToFront(el)
		return
	}
	for c.size+int64(len(data)) > c.max {
		back := c.lru.Back()
		if back == nil {
			break
		}
		c.lru.Remove(back)
		old := back.Value.(*cacheItem)
		c.size -= int64(len(old.data))
		delete(c.items, old.key)
	}
	el := c.lru.PushFront(&cacheItem{key: key, data: data})
	c.items[key] = el
}

// Drop removes a single key (e.g. a deleted file).
func (c *Cache) Drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.lru.Remove(el)
		c.size -= int64(len(el.Value.(*cacheItem).data))
		delete(c.items, key)
	}
}

// DropPrefix removes every key with the given prefix (file purged).
func (c *Cache) DropPrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.lru.Front(); el != nil; {
		next := el.Next()
		it := el.Value.(*cacheItem)
		if len(it.key) >= len(prefix) && it.key[:len(prefix)] == prefix {
			c.lru.Remove(el)
			c.size -= int64(len(it.data))
			delete(c.items, it.key)
		}
		el = next
	}
}

// Len returns the current byte usage.
func (c *Cache) Len() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.size
}
