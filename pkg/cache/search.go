package cache

import (
	"path/filepath"
	"regexp"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

func (c *Cache) Search(pattern string, useRegex bool) ([]string, error) {
	c.logger.Info("search", zap.String("pattern", pattern))

	c.mu.RLock("read")
	defer c.mu.RUnlock()

	start := time.Now()
	defer func() {
		searchType := "wildcard"
		if useRegex {
			searchType = "regex"
		}

		metrics.ObserveOperation(time.Since(start), searchType)
	}()

	if !useRegex && len(pattern) > 1 && pattern[len(pattern)-1] == '*' {
		prefix := pattern[:len(pattern)-1]
		return c.searchPrefix(prefix), nil
	}

	return c.searchGeneric(pattern, useRegex)
}

func (c *Cache) searchPrefix(prefix string) []string {
	c.logger.Info("search", zap.String("prefix", prefix))

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
	c.logger.Info("search", zap.String("pattern", pattern), zap.Bool("useRegex", useRegex))

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

	seen := make(map[string]struct{})
	matches := make([]string, 0)

	c.prefixTree.Walk(func(s string, v interface{}) bool {
		// Filter out duplicates
		if _, exists := seen[s]; exists {
			return false
		}
		seen[s] = struct{}{}

		// Check if key has an expiration and is already expired
		if item, exists := c.items[s]; exists {
			if !item.expiration.IsZero() && time.Now().After(item.expiration) {
				return false
			}
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
		return false
	})

	return matches, nil
}

func (c *Cache) searchWorker(pattern string, useRegex bool, re *regexp.Regexp, results chan string) {
	c.logger.Info("search", zap.String("pattern", pattern))

	seen := make(map[string]struct{})

	c.prefixTree.Walk(func(s string, v interface{}) bool {
		if _, exists := seen[s]; exists {
			return false
		}
		seen[s] = struct{}{}

		// Skip keys with expiration set and already expired
		if item, exists := c.items[s]; exists {
			if !item.expiration.IsZero() && time.Now().After(item.expiration) {
				return false
			}
			var match bool
			if useRegex {
				match = re.MatchString(s)
			} else {
				match, _ = filepath.Match(pattern, s)
			}
			if match {
				results <- s
			}
		}
		return false
	})
}
