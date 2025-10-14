# Dockerfile para o Cliente de Jogo Distribuído
FROM golang:1.25-alpine AS builder

# Instala dependências de build
RUN apk add --no-cache git

WORKDIR /app

# Copia arquivos de dependências
COPY go.mod go.sum ./
RUN go mod download

# Copia código fonte
COPY . .

# Compila o cliente
RUN go build -o /cliente ./cliente

# Imagem final
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copia o binário compilado
COPY --from=builder /cliente .

# Define o comando de inicialização
ENTRYPOINT ["./cliente"]


