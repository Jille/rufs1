rufs: *.go */*.go Makefile
	go build

rufs_master: *.go */*.go Makefile
	go build -tags bolt -o rufs-master-bolt

rufs_windows_386.exe: rufs
	gox -osarch=windows/386

rufs_windows_amd64.exe: rufs
	gox -osarch=windows/amd64

fmt:
	for i in *.go */*.go; do gofmt -w -d $$i; done
	make rufs
