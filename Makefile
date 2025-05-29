.PHONY: fmt
fmt:
	gofumpt -l -w .

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test -v -count=1 -cover ./...

.PHONY: proto
proto:
	rm -f pkg/pb/*.go
	rm -f pkg/cmd/swagger/*.swagger.json
	protoc --proto_path=pkg/proto --go_out=pkg/pb --go_opt=paths=source_relative \
	--go-grpc_out=pkg/pb --go-grpc_opt=paths=source_relative \
	--grpc-gateway_out=pkg/pb --grpc-gateway_opt=paths=source_relative \
	--openapiv2_out=pkg/cmd/swagger --openapiv2_opt=allow_merge=true,merge_file_name=simple_cache \
	pkg/proto/*.proto

.PHONY: docker-build
docker-build:
	docker build -t ishenle/simple-cache:v0.1  .

.PHONY: docker-push
docker-push:
	docker push ishenle/simple-cache:v0.1

.PHONY: docker
docker: docker-build docker-push
