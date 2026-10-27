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
