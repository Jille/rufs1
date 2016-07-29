rufs: *.go Makefile
	go build

rufs_windows_386.exe: rufs
	gox -osarch=windows/386

rufs_windows_amd64.exe: rufs
	gox -osarch=windows/amd64

fmt:
	for i in *.go; do gofmt -w -d $$i; done
	make rufs
