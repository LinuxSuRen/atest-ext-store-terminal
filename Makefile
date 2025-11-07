build: build-ui
	go build -o bin/atest-ext-store-terminal .
build-ui:
	cd ui && npm i && npm run build-only
test:
	mkdir -p ui/dist
	touch ui/dist/atest-ext-store-terminal.css
	touch ui/dist/atest-ext-store-terminal.umd.js
	go test ./... -cover -v -coverprofile=coverage.out
	go tool cover -func=coverage.out
build-image:
	docker build .
run-e2e: build
