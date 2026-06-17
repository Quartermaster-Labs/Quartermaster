package config

import (
	"fmt"
	"sort"
	"strings"
)

// ListenerConfig declares which groups a single listen address exposes. A
// listener's catalog (/v1/models) and request routing are restricted to the
// models that belong to its groups.
type ListenerConfig struct {
	Groups []string `yaml:"groups"`
}

// validateListeners checks the listeners block after groups have been
// normalized. It requires the group router (listeners select by group, which
// the matrix router does not have), non-empty addresses, at least one group per
// listener, and that every referenced group exists.
func (c *Config) validateListeners() error {
	if len(c.Listeners) == 0 {
		return nil
	}

	if c.Matrix != nil {
		return fmt.Errorf("listeners require the group router; they are not supported with 'matrix'")
	}

	for addr, lc := range c.Listeners {
		if strings.TrimSpace(addr) == "" {
			return fmt.Errorf("listeners: empty listen address")
		}
		if len(lc.Groups) == 0 {
			return fmt.Errorf("listeners: address %q exposes no groups", addr)
		}
		seen := make(map[string]bool, len(lc.Groups))
		for _, gid := range lc.Groups {
			if seen[gid] {
				return fmt.Errorf("listeners: address %q lists group %q more than once", addr, gid)
			}
			seen[gid] = true
			if _, ok := c.Groups[gid]; !ok {
				return fmt.Errorf("listeners: address %q references unknown group %q", addr, gid)
			}
		}
	}

	return nil
}

// ListenerModelSets returns, per listen address, the set of real model IDs
// reachable from that address (the union of its groups' members). It returns an
// empty map when no listeners are configured. Addresses absent from the result
// are unrestricted by convention (see Server.ServeListener).
func (c *Config) ListenerModelSets() map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(c.Listeners))
	for addr, lc := range c.Listeners {
		models := make(map[string]bool)
		for _, gid := range lc.Groups {
			for _, mid := range c.Groups[gid].Members {
				models[mid] = true
			}
		}
		out[addr] = models
	}
	return out
}

// ListenerAddrs returns the configured listen addresses in deterministic order.
func (c *Config) ListenerAddrs() []string {
	addrs := make([]string, 0, len(c.Listeners))
	for addr := range c.Listeners {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	return addrs
}
