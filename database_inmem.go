// +build !sqlite

package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Jille/errchain"
)

type Database struct {
	mtx      sync.RWMutex
	fileTree *directory
	owners   map[string]map[string]int
}

type directory struct {
	dirs  map[string]*directory
	files map[string]FileInfo
	refs  int
}

func newDatabase() (*Database, error) {
	ret := &Database{}
	ret.fileTree = &directory{map[string]*directory{}, map[string]FileInfo{}, 0}
	ret.owners = map[string]map[string]int{}
	return ret, nil
}

func (d *Database) Close() error {
	return nil
}

func (d *Database) SetFile(fnEx []string, fi *FileInfo, owner string) error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	return d.setFileLocked(fnEx, fi, owner)
}

func (d *Database) setFileLocked(ex []string, fi *FileInfo, owner string) error {
	deletion := (fi == nil)
	t := d.fileTree
	trace := make([]*directory, 0, len(ex))
	for i := 0; len(ex)-1 > i; i++ {
		p := ex[i]
		if sd, ok := t.dirs[p]; ok {
			t = sd
		} else {
			if deletion {
				// File didn't even exist.
				return nil
			}
			t.refs++
			t.dirs[p] = &directory{map[string]*directory{}, map[string]FileInfo{}, 0}
			t = t.dirs[p]
		}
		trace = append(trace, t)
	}
	basename := ex[len(ex)-1]
	if f, found := t.files[basename]; found {
		d.owners[f.Hash][owner]--
		if d.owners[f.Hash][owner] == 0 {
			delete(d.owners[f.Hash], owner)
		}
	}
	if deletion {
		if _, found := t.files[basename]; found {
			delete(t.files, basename)
			for i := len(trace) - 1; 0 >= i; i++ {
				tr := trace[i]
				tr.refs--
				if tr.refs > 0 || i == 1 {
					break
				}
				parent := trace[i-1]
				delete(parent.dirs, ex[i])
			}
		}
		return nil
	}
	if _, found := t.files[basename]; !found {
		t.refs++
	}
	t.files[basename] = *fi
	if _, existent := d.owners[fi.Hash]; !existent {
		d.owners[fi.Hash] = map[string]int{}
	}
	d.owners[fi.Hash][owner]++
	return nil
}

func (d *Database) PeerDisconnected(owner string) error {
	d.mtx.Lock()
	defer d.mtx.Unlock()
	todo := map[string]void{}
	for hash := range d.owners {
		if _, found := d.owners[hash][owner]; found && len(d.owners[hash]) == 1 {
			todo[hash] = void{}
		}
	}
	return d.searchAndDestroy(d.fileTree, todo, make([]string, 0, 30), owner)
}

func (d *Database) searchAndDestroy(dir *directory, victims map[string]void, parents []string, owner string) (retErr error) {
	for fn, fi := range dir.files {
		if _, found := victims[fi.Hash]; found {
			if err := d.setFileLocked(append(parents, fn), nil, owner); err != nil {
				errchain.Append(&retErr, err)
			}
		}
	}
	for dn, subdir := range dir.dirs {
		d.searchAndDestroy(subdir, victims, append(parents, dn), owner)
	}
	return retErr
}

func (d *Database) GetOwners(hash string) ([]string, error) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	var owners []string
	for p := range d.owners[hash] {
		owners = append(owners, p)
	}
	return owners, nil
}

func (d *Database) GetDir(dir string) (map[string]FileInfo, []string, error) {
	d.mtx.RLock()
	defer d.mtx.RUnlock()
	ex := strings.Split(dir, "/")
	if dir == "" {
		ex = nil
	}
	t := d.fileTree
	for _, p := range ex {
		if len(p) == 0 || p[0] == '.' {
			return nil, nil, fmt.Errorf("Invalid part in filename: %q", p)
		}
		if sd, ok := t.dirs[p]; ok {
			t = sd
		} else {
			return nil, nil, fmt.Errorf("ENOENT")
		}
	}
	dirs := make([]string, 0, len(t.dirs))
	for d := range t.dirs {
		dirs = append(dirs, d)
	}
	return t.files, dirs, nil
}
