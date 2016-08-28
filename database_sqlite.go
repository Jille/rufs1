// +build sqlite

package main

import (
	"database/sql"
	"flag"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Jille/errchain"
	_ "github.com/mattn/go-sqlite3"
)

var (
	masterDbFile = flag.String("master_db_file", "%rufs_var_storage%/master/db.sqlite", "Path to sqlite3 database of the master")
)

type Database struct {
	db             *sql.DB
	stmtAddFile    *sql.Stmt
	stmtDeleteFile *sql.Stmt
	stmtDisconnect *sql.Stmt
	stmtGetOwners  *sql.Stmt
	stmtGetDir     *sql.Stmt
	stmtGetAllDirs *sql.Stmt
	dbLock         sync.RWMutex
	dirCache       map[string][]string
	dirCacheMtx    sync.Mutex
}

func newDatabase() (*Database, error) {
	ret := &Database{}
	fn := getPath(*masterDbFile)

	_ = os.Remove(fn)

	var err error
	ret.db, err = sql.Open("sqlite3", fn)
	if err != nil {
		return nil, err
	}

	if _, err := ret.db.Exec(`
DROP TABLE IF EXISTS fs;
CREATE TABLE fs (
	directory VARCHAR(255) NOT NULL,
	file VARCHAR(255) NOT NULL,
	size INT NOT NULL,
	mtime INT NOT NULL,
	hash VARCHAR(40) NOT NULL,
	peer VARCHAR(64) NOT NULL,
	PRIMARY KEY(directory, file, peer)
);
CREATE INDEX idx_directory ON fs (directory);
CREATE INDEX idx_hash ON fs (hash);
	`); err != nil {
		return nil, err
	}
	if _, err := ret.db.Exec(`PRAGMA synchronous = OFF`); err != nil {
		return nil, err
	}
	if _, err := ret.db.Exec(`PRAGMA journal_mode = OFF`); err != nil {
		return nil, err
	}
	if _, err := ret.db.Exec(`PRAGMA locking_mode=EXCLUSIVE`); err != nil {
		return nil, err
	}

	ret.stmtAddFile, err = ret.db.Prepare(`INSERT INTO fs (directory, file, size, mtime, hash, peer) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	ret.stmtDeleteFile, err = ret.db.Prepare(`DELETE FROM fs WHERE directory = ? AND file = ? AND peer = ?`)
	if err != nil {
		return nil, err
	}
	ret.stmtDisconnect, err = ret.db.Prepare(`DELETE FROM fs WHERE peer = ?`)
	if err != nil {
		return nil, err
	}
	ret.stmtGetOwners, err = ret.db.Prepare(`SELECT DISTINCT peer FROM fs WHERE hash = ?`)
	if err != nil {
		return nil, err
	}
	ret.stmtGetDir, err = ret.db.Prepare(`SELECT file, size, mtime, hash FROM fs WHERE directory = ? GROUP BY file`)
	if err != nil {
		return nil, err
	}
	ret.stmtGetAllDirs, err = ret.db.Prepare(`SELECT DISTINCT directory FROM fs`)
	if err != nil {
		return nil, err
	}
	ret.dirCache = nil

	return ret, nil
}

func (d *Database) Close() (retErr error) {
	errchain.Append(&retErr, d.stmtAddFile.Close())
	errchain.Append(&retErr, d.stmtDeleteFile.Close())
	errchain.Append(&retErr, d.stmtDisconnect.Close())
	errchain.Append(&retErr, d.db.Close())
	return retErr
}

func (d *Database) SetFile(fnEx []string, fi *FileInfo, owner string) error {
	d.dbLock.Lock()
	defer d.dbLock.Unlock()
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	dn := strings.Join(fnEx[:len(fnEx)-1], "/")
	f := fnEx[len(fnEx)-1]
	if _, err := tx.Stmt(d.stmtDeleteFile).Exec(dn, f, owner); err != nil {
		return err
	}
	if fi != nil {
		if _, err := tx.Stmt(d.stmtAddFile).Exec(dn, f, fi.Size, fi.Mtime.Unix(), fi.Hash, owner); err != nil {
			return err
		}
		d.dirCacheMtx.Lock()
		if d.dirCache != nil && d.dirCache[dn] == nil {
			for s := len(fnEx) - 1; s > 0; s-- {
				dn := strings.Join(fnEx[:s], "/")
				if d.dirCache[dn] != nil {
					break
				}
				pdn := strings.Join(fnEx[:s-1], "/")
				f := fnEx[s-1]
				d.dirCache[pdn] = append(d.dirCache[pdn], f)
			}
			d.dirCache[dn] = []string{}
		}
		d.dirCacheMtx.Unlock()
	} else {
		d.invalidateDirCache()
	}
	return tx.Commit()
}

func (d *Database) PeerDisconnected(owner string) error {
	d.dbLock.Lock()
	defer d.dbLock.Unlock()
	_, err := d.stmtDisconnect.Exec(owner)
	d.invalidateDirCache()
	return err
}

func (d *Database) GetOwners(hash string) ([]string, error) {
	d.dbLock.RLock()
	defer d.dbLock.RUnlock()
	rows, err := d.stmtGetOwners.Query(hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ret []string
	for rows.Next() {
		var peer string
		err = rows.Scan(&peer)
		if err != nil {
			return nil, err
		}
		ret = append(ret, peer)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return ret, nil
}

func (d *Database) GetDir(dir string) (map[string]FileInfo, []string, error) {
	d.dbLock.RLock()
	defer d.dbLock.RUnlock()

	d.dirCacheMtx.Lock()
	if d.dirCache == nil {
		d.updateDirCache()
	}
	dirs := d.dirCache[dir]
	d.dirCacheMtx.Unlock()

	rows, err := d.stmtGetDir.Query(dir)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	files := map[string]FileInfo{}
	for rows.Next() {
		var file, hash string
		var size, mtime int64
		err = rows.Scan(&file, &size, &mtime, &hash)
		if err != nil {
			return nil, nil, err
		}
		files[file] = FileInfo{
			Size:  size,
			Mtime: time.Unix(mtime, 0),
			Hash:  hash,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	return files, dirs, nil
}

func (d *Database) invalidateDirCache() {
	d.dirCacheMtx.Lock()
	defer d.dirCacheMtx.Unlock()
	d.dirCache = nil
}

func (d *Database) updateDirCache() error {
	rows, err := d.stmtGetAllDirs.Query()
	if err != nil {
		return err
	}
	defer rows.Close()
	d.dirCache = map[string][]string{}
	for rows.Next() {
		var dir string
		err = rows.Scan(&dir)
		if err != nil {
			return err
		}
		ex := strings.Split(dir, "/")
	outer:
		for s := len(ex) - 1; s >= 0; s-- {
			dn := strings.Join(ex[:s], "/")
			f := ex[s]
			for _, d := range d.dirCache[dn] {
				if d == f {
					break outer
				}
			}
			d.dirCache[dn] = append(d.dirCache[dn], f)
		}
	}
	return rows.Err()
}
