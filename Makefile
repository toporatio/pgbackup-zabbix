BINARY := pgbackup-zabbix
PKG    := ./...

.PHONY: build test vet lint linux clean

build:
	go build -o $(BINARY) ./cmd/$(BINARY)

linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BINARY) ./cmd/$(BINARY)

test:
	go test $(PKG)

test-integration:
	go test -tags=integration $(PKG)

vet:
	go vet $(PKG)

clean:
	rm -f $(BINARY)
