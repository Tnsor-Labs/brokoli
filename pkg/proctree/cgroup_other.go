//go:build !linux

package proctree

import (
	"errors"
	"os/exec"
)

// MemoryCgroup is Linux-only; elsewhere every constructor reports that.
type MemoryCgroup struct{}

var errNoCgroups = errors.New("memory cgroups are Linux-only")

func MemoryCgroupParent(string) (string, error)             { return "", errNoCgroups }
func NewMemoryCgroup(string, uint64) (*MemoryCgroup, error) { return nil, errNoCgroups }
func (c *MemoryCgroup) Place(*exec.Cmd)                     {}
func (c *MemoryCgroup) OOMKilled() bool                     { return false }
func (c *MemoryCgroup) Close() error                        { return nil }
