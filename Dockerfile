# build:
# docker build -t mcp-vroot:latest -f Dockerfile .

# run
# docker run -it --rm -p 8765:8765 -u 1000:1000 -v "$PWD/config.json:/config.json" -v "$PWD/workspace:/workspace" mcp-vroot:latest
# docker run -it --rm --network=host -u 1000:1000 -v "$PWD/config.json:/config.json" -v "$PWD/workspace:/workspace" mcp-vroot:latest

FROM golang:1.27-alpine AS builder
WORKDIR /app

COPY go.mod go.sum .
RUN go mod download

COPY . .

RUN go mod tidy

# Static build required so that we can safely copy the binary over.
# `-tags timetzdata` embeds zone info from the "time/tzdata" package.
#RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -extldflags "-static"' -tags timetzdata
RUN --mount=type=cache,mode=0755,target=/root/.cache/go-build \
    --mount=type=cache,mode=0755,target=/go \
    CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -tags timetzdata

FROM scratch
WORKDIR /workspace

# the tls certificates:
# NB: this pulls directly from the upstream image, which already has ca-certificates:
COPY --from=alpine:latest /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /app/config.json /config.json

# the program:
COPY --from=builder /app/mcp-vroot /

ENTRYPOINT ["/mcp-vroot"]
CMD ["-c", "/config.json"]


