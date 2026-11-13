# Contributing

## Build

### From source

```sh
npm install --prefix ui
npm run build --prefix ui
go build -o smsc -ldflags "-s -w -X github.com/ellanetworks/smsc/version.GitCommit=$(git rev-parse HEAD)" ./cmd/smsc
```

### Container Image

```sh
rockcraft pack
```

### Snap

```sh
snapcraft pack
```

### Documentation

```sh
uvx --with-requirements requirements-docs.txt mkdocs build --strict
```

## Run

```sh
./smsc --config smsc.yaml
```

## Test

```sh
sudo modprobe sctp
go test ./...
npm test --prefix ui
```

The UI end-to-end tests build the UI and the SMSC, and run it in the `ella-smsc:latest` image with Docker Compose. They need Docker and that image, and Playwright's Chromium:

```sh
npx --prefix ui playwright install chromium
npm run test:e2e --prefix ui
```
