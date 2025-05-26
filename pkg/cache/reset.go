package cache

func (c *Cache) Reset() int {
	c.logger.Info("resetting cache")

	c.mu.Lock("write")
	defer c.mu.Unlock()
	count := len(c.items)
	c.items = make(map[string]*Item)
	return count
}
