# Makefile for OPSCFS development tasks

.PHONY: all genkeys build test test-race test-store clean clean-testdata

all: build

# build CLI binary
build:
	@mkdir -p bin
	go build -o bin/opscfs .

# run all tests
test:
	go test ./...

# run tests with race detector
test-race:
	go test -race ./...

# run store test using provided TEST_FILE and TEST_NAME (defaults available)
test-store: build prepare-data
	@if [ -z "${TEST_FILE}" ]; then echo "Please set TEST_FILE, e.g.: TEST_FILE=abc make test-store"; exit 2; fi
	@ROOT_DIR=data
	@KEY_HEX=0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20
	@NAME=${TEST_NAME:-testfile}
	@OUT=${OUT_FILE:-out_testfile}
	@echo "Using TEST_FILE=$$TEST_FILE -> storing as $$NAME in $$ROOT_DIR"
	@./bin/opscfs -root "$$ROOT_DIR" -key "$$KEY_HEX" add "$$NAME" "$$TEST_FILE"
	@./bin/opscfs -root "$$ROOT_DIR" -key "$$KEY_HEX" list
	@./bin/opscfs -root "$$ROOT_DIR" -key "$$KEY_HEX" verify "$$NAME"
	@./bin/opscfs -root "$$ROOT_DIR" -key "$$KEY_HEX" get "$$NAME" "$$OUT"
	@./bin/opscfs -root "$$ROOT_DIR" -key "$$KEY_HEX" delete "$$NAME"
	@./bin/opscfs -root "$$ROOT_DIR" -key "$$KEY_HEX" clean
	@echo "test-store completed; output file: $$OUT"

clean:
	@echo "Cleaning build artifacts..."
	-rm -f bin/opscfs
