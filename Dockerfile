FROM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/moltenhub-dispatch ./cmd/moltenhub-dispatch

RUN mkdir -p /out/workspace/config

FROM gcr.io/distroless/base-debian12:nonroot

WORKDIR /app

ENV LISTEN_ADDR=:8080
ENV APP_DATA_DIR=/workspace/config

COPY --from=build /out/moltenhub-dispatch /app/moltenhub-dispatch
COPY --from=build --chown=nonroot:nonroot /out/workspace/config /workspace/config

EXPOSE 8080

VOLUME ["/workspace/config"]

ENTRYPOINT ["/app/moltenhub-dispatch"]
