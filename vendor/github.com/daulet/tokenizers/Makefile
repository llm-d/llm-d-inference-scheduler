build:
	@cargo build --release
	@cp target/release/libtokenizers.a .
	@go build .

build-example:
	@docker build -f ./example/Dockerfile . -t tokenizers-example

release-darwin-%: test
	cargo build --release --target $*-apple-darwin
	mkdir -p artifacts/darwin-$*
	cp target/$*-apple-darwin/release/libtokenizers.a artifacts/darwin-$*/libtokenizers.a
	cd artifacts/darwin-$* && \
		tar -czf libtokenizers.darwin-$*.tar.gz libtokenizers.a
	mkdir -p artifacts/all
	cp artifacts/darwin-$*/libtokenizers.darwin-$*.tar.gz artifacts/all/libtokenizers.darwin-$*.tar.gz

release-linux-%: test
	docker buildx build --platform linux/$* --build-arg="DOCKER_TARGETPLATFORM=linux/$*" -f release/Dockerfile . -t tokenizers.linux-$*
	mkdir -p artifacts/linux-$*
	docker run -v $(PWD)/artifacts/linux-$*:/mnt --entrypoint ls tokenizers.linux-$* /workspace/tokenizers/lib/linux
	docker run -v $(PWD)/artifacts/linux-$*:/mnt --entrypoint cp tokenizers.linux-$* /workspace/tokenizers/lib/linux/$*/libtokenizers.a /mnt/libtokenizers.a
	cd artifacts/linux-$* && \
		tar -czf libtokenizers.linux-$*.tar.gz libtokenizers.a
	mkdir -p artifacts/all
	cp artifacts/linux-$*/libtokenizers.linux-$*.tar.gz artifacts/all/libtokenizers.linux-$*.tar.gz

release: release-darwin-aarch64 release-darwin-x86_64 release-linux-arm64 release-linux-x86_64
	cp artifacts/all/libtokenizers.darwin-aarch64.tar.gz artifacts/all/libtokenizers.darwin-arm64.tar.gz
	cp artifacts/all/libtokenizers.linux-arm64.tar.gz artifacts/all/libtokenizers.linux-aarch64.tar.gz
	cp artifacts/all/libtokenizers.linux-x86_64.tar.gz artifacts/all/libtokenizers.linux-amd64.tar.gz

test: build
	@go test -ldflags="-extldflags '-L./'" -v ./... -count=1

clean:
	rm -rf libtokenizers.a target

bazel-sync:
	CARGO_BAZEL_REPIN=1 bazel sync --only=crate_index
