.PHONY: build test vet fmt tidy run-server run-opsctl race docker-eval clean

BUILD_FLAGS := -trimpath
GOFLAGS := -mod=readonly

build:
	go build $(BUILD_FLAGS) ./...

test:
	go test -timeout=300s -count=1 ./...

race:
	go test -race -timeout=420s -count=1 ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

tidy:
	go mod tidy

run-server:
	go run ./cmd/server -config config.example.yaml

run-opsctl:
	go run ./cmd/opsctl

docker-eval:
	./build_eval_docker.sh sitesync-eval linux/amd64

clean:
	rm -rf ./data
