// +build bolt,!sqlite

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/boltdb/bolt"
)

var (
	masterDbFile = flag.String("master_db_file", "%rufs_var_storage%/master/db.bolt", "Path to Bolt database of the master")
)

type Database struct {
	db *bolt.DB
}

func newDatabase() (*Database, error) {
	ret := &Database{}
	fn := getPath(*masterDbFile)

	_ = os.Remove(fn)

	var err error
	ret.db, err = bolt.Open(fn, 0600, nil)
	if err != nil {
		return nil, err
	}
	ret.db.NoSync = true

	err = ret.db.Update(func(tx *bolt.Tx) error {
		// files/bucket/bucket/fn = serialized FileInfo
		_, err := tx.CreateBucket([]byte("files"))
		if err != nil {
			return fmt.Errorf("create bucket: %s", err)
		}
		// byHash/hash = \0 separated list of owners
		_, err = tx.CreateBucket([]byte("byHash"))
		if err != nil {
			return fmt.Errorf("create bucket: %s", err)
		}
		// byOwner/owner/filename = ""
		_, err = tx.CreateBucket([]byte("byOwner"))
		if err != nil {
			return fmt.Errorf("create bucket: %s", err)
		}
		return nil
	})

	return ret, nil
}

func (d *Database) Close() error {
	d.db.Sync()
	return d.db.Close()
}

func (Database) encodeFileInfo(fi *FileInfo) []byte {
	ret, err := json.Marshal(fi)
	if err != nil {
		panic(err)
	}
	return ret
}

func (Database) decodeFileInfo(buf []byte) (fi FileInfo) {
	err := json.Unmarshal(buf, &fi)
	if err != nil {
		panic(err)
	}
	return fi
}

func (d *Database) SetFile(fnEx []string, fi *FileInfo, owner string) error {
	// fi can be null if this is a delete.
	ownerB := []byte(owner)
	return d.db.Update(func(tx *bolt.Tx) error {
		var err error
		// Update byOwner bucket
		b := tx.Bucket([]byte("byOwner"))
		ob := b.Bucket(ownerB)
		if ob == nil && fi != nil {
			// Create bucket if it doesn't exist (unless we're deleting).
			ob, err = b.CreateBucket(ownerB)
			if err != nil {
				return err
			}
		}
		if fi != nil {
			if err := ob.Put([]byte(strings.Join(fnEx, "/")), []byte{}); err != nil {
				return err
			}
		} else if ob != nil {
			if err := ob.Delete([]byte(strings.Join(fnEx, "/"))); err != nil {
				return err
			}
		}

		// Update files (sub)buckets
		b = tx.Bucket([]byte("files"))
		var parents []*bolt.Bucket
		dirs := fnEx[:len(fnEx)-1]
		fn := []byte(fnEx[len(fnEx)-1])
		// Deep-dive to find the bucket for every directory in the path.
		for _, dn := range dirs {
			// parents keeps track so we can delete them all if this was the last file in these buckets.
			parents = append(parents, b)
			sb := b.Bucket([]byte(dn))
			if sb == nil {
				sb, err = b.CreateBucket([]byte(dn))
				if err != nil {
					return err
				}
			}
			b = sb
		}
		var hashB []byte
		if fi == nil {
			// Get the old hash so we can remove if from the byHash bucket below.
			oldFi := d.decodeFileInfo(b.Get(fn))
			hashB = []byte(oldFi.Hash)

			if err := b.Delete(fn); err != nil {
				return err
			}

			// Delete all buckets upwards that are now empty.
			for k, _ := b.Cursor().First(); k == nil && len(parents) > 0; k, _ = b.Cursor().First() {
				pb := parents[len(parents)-1]
				bname := dirs[len(parents)-1]
				if err := pb.DeleteBucket([]byte(bname)); err != nil {
					return err
				}
				b = pb
				parents = parents[:len(parents)-1]
			}
		} else {
			if err := b.Put(fn, d.encodeFileInfo(fi)); err != nil {
				return err
			}
			hashB = []byte(fi.Hash)
		}

		// Update byHash bucket
		b = tx.Bucket([]byte("byHash"))
		owners := b.Get(hashB)
		if fi == nil {
			// Remove owner from list of owners.
			if len(owners) > 0 {
				var newOwners [][]byte
				for _, o := range bytes.Split(owners, []byte{0}) {
					if !bytes.Equal(o, ownerB) {
						newOwners = append(newOwners, o)
					}
				}
				owners = bytes.Join(newOwners, []byte{0})
			}
		} else {
			if len(owners) == 0 {
				owners = []byte(owner)
			} else {
				owners = append(owners, 0)
				owners = append(owners, []byte(owner)...)
			}
		}
		if len(owners) == 0 {
			if err := b.Delete(hashB); err != nil {
				return err
			}
		} else {
			if err := b.Put(hashB, owners); err != nil {
				return err
			}
		}

		return nil
	})
}

func (d *Database) PeerDisconnected(owner string) error {
	var files []string
	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("byOwner")).Bucket([]byte(owner))
		if b == nil {
			return nil
		}
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			// We need to put this in a list and process them later because we can't start multiple read-write transactions during this read-only transaction.
			files = append(files, string(k))
		}
		return nil
	})
	if err != nil {
		return err
	}
	var lastErr error
	for _, fn := range files {
		if err := d.SetFile(strings.Split(fn, "/"), nil, owner); err != nil {
			lastErr = err
			log.Printf("Failed to delete file on PeerDisconnected: %v", err)
		}
	}
	return lastErr
}

func (d *Database) GetOwners(hash string) ([]string, error) {
	var owners []string
	err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("byHash"))
		v := b.Get([]byte(hash))
		if len(v) == 0 {
			return fmt.Errorf("hash %q not found", hash)
		}
		for _, o := range bytes.Split(v, []byte{'/'}) {
			owners = append(owners, string(o))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return owners, nil
}

func (d *Database) GetDir(dir string) (map[string]FileInfo, []string, error) {
	files := map[string]FileInfo{}
	var dirs []string

	fnEx := bytes.Split([]byte(dir), []byte{'/'})
	if dir == "" {
		fnEx = nil
	}
	if err := d.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("files"))
		for _, dn := range fnEx {
			b = b.Bucket(dn)
			if b == nil {
				return fmt.Errorf("ENOENT")
			}
		}

		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if v == nil {
				dirs = append(dirs, string(k))
			} else {
				files[string(k)] = d.decodeFileInfo(v)
			}
		}

		return nil
	}); err != nil {
		return nil, nil, err
	}

	return files, dirs, nil
}
