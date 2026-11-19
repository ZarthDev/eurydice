# syntax=docker/dockerfile:1
# Binário estático numa imagem distroless sem shell, rodando como nonroot.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/eurydice-sync .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/eurydice-sync /eurydice-sync
USER nonroot:nonroot
ENTRYPOINT ["/eurydice-sync"]
