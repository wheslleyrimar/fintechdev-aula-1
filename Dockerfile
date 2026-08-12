# Monólito modular: UMA imagem, UM deploy (Aula 1 — "régua de evolução").
# O bacen-sim usa o mesmo Dockerfile só porque simula um sistema EXTERNO (BACEN),
# que por definição não faz parte do nosso deploy.
FROM golang:1.25-alpine AS build

WORKDIR /src

# Cache de dependências separado do código.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG CMD=techpix
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${CMD}

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata curl && adduser -D -u 10001 app
USER app
COPY --from=build /out/app /usr/local/bin/app
ENTRYPOINT ["/usr/local/bin/app"]
