FROM golang:1.19-alpine AS builder

WORKDIR /build
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o mcp-bridge .

FROM scratch

WORKDIR /
COPY --from=builder /build/mcp-bridge /mcp-bridge

EXPOSE 8080
ENTRYPOINT ["/mcp-bridge"]
