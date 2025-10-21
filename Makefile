.PHONY: all build go-test go-test-race test clean

all: build

build:
	go build -o bin/opscfs .

go-test:
	go test ./...

go-test-race:
	go test -race ./...

test: build
	KEY_HEX=$$(openssl rand -hex 32); \
    echo "KEY is $$KEY_HEX"; \
    openssl rand -out sample1.bin 1048576; \
    openssl rand -out sample2.bin 1048576; \
	./bin/opscfs -root data -key "$$KEY_HEX" add "sample1.bin" "./sample1.bin"; \
	./bin/opscfs -root data -key "$$KEY_HEX" add "sample2.bin" "./sample2.bin"; \
	./bin/opscfs -root data -key "$$KEY_HEX" list; \
	./bin/opscfs -root data -key "$$KEY_HEX" verify-all; \
	./bin/opscfs -root data -key "$$KEY_HEX" get "sample1.bin" "./sample1.bin.out"; \
	./bin/opscfs -root data -key "$$KEY_HEX" get "sample2.bin" "./sample2.bin.out"; \
	./bin/opscfs -root data -key "$$KEY_HEX" delete "sample1.bin"; \
	./bin/opscfs -root data -key "$$KEY_HEX" list; \
	./bin/opscfs -root data -key "$$KEY_HEX" clean; \
	./bin/opscfs -root data -key "$$KEY_HEX" list; \
	./bin/opscfs -root data -key "$$KEY_HEX" delete "sample2.bin"; \
	./bin/opscfs -root data -key "$$KEY_HEX" clean; \
    sha256sum "./sample1.bin" "./sample1.bin.out"; \
    sha256sum "./sample2.bin" "./sample2.bin.out"; \
    rm -f ./sample1.bin ./sample1.bin.out ./sample2.bin ./sample2.bin.out

clean:
	-rm -f bin/opscfs
