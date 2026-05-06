package cache

import (
	"path/filepath"
	"regexp"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

func (c *Cache) Search(pattern string, useRegex bool) ([]string, error) {
	c.logger.Debug("search", zap.String("pattern", pattern))

	c.mu.RLock(metrics.LockRead)
	defer c.mu.RUnlock()

	start := time.Now()
	defer func() {
		op := metrics.OpSearchWildcard
		if useRegex {
			op = metrics.OpSearchRegex
		}
		metrics.ObserveOperation(time.Since(start), op)
	}()

	if !useRegex && len(pattern) > 1 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return c.searchPrefix(prefix), nil
	}

	return c.searchGeneric(pattern, useRegex)
}

func (c *Cache) searchPrefix(prefix string) []string {
	result := make([]string, 0)

	c.prefixTree.WalkPrefix(prefix, func(s string, v interface{}) bool {
		if item, exists := c.items[s]; exists {
			if !item.expiration.IsZero() && time.Now().After(item.expiration) {
				return false
			}
			result = append(result, s)
		}
		return false
	})

	return result
}

func (c *Cache) searchGeneric(pattern string, useRegex bool) ([]string, error) {
	var (
		re  *regexp.Regexp
		err error
	)

	if useRegex {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
	} else {
		if _, err := filepath.Match(pattern, "dummy"); err != nil {
			return nil, err
		}
	}

	matches := make([]string, 0)

	c.prefixTree.Walk(func(s string, v interface{}) bool {
		// Check if key has an expiration and is already expired
		if item, exists := c.items[s]; exists {
			if !item.expiration.IsZero() && time.Now().After(item.expiration) {
				return false
			}

			// Pattern matching
			var match bool
			if useRegex {
				match = re.MatchString(s)
			} else {
				match, _ = filepath.Match(pattern, s)
			}

			if match {
				matches = append(matches, s)
			}
		}
		return false
	})

	return matches, nil
}
