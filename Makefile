.PHONY: install

install:
	go -C executable build -o "$$(go env GOPATH)/bin/verde" .
