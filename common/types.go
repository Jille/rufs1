package common

import "time"

type FileInfo struct {
	Size  int64
	Hash  string
	Mtime time.Time
}
