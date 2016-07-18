rufs: *.go Makefile
	go build

fmt:
	for i in *.go; do gofmt -w -d $$i; done
	make rufs
