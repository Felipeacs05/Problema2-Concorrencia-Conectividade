# Dockerfile para o Servidor de Jogo Distribuído
FROM golang:1.25-alpine AS builder

# Instala dependências de build
RUN apk update && apk add --no-cache git

WORKDIR /app

# Copia arquivos de dependências
COPY go.mod go.sum ./
RUN go mod download

# Copia código fonte
COPY . .

# Compila o servidor
RUN go build -o /servidor ./servidor

# Imagem final
FROM alpine:3.22

# Atualiza índices de repositórios e instala dependências
RUN apk update && apk --no-cache add ca-certificates wget netcat-openbsd

WORKDIR /root/

# Copia o binário compilado
COPY --from=builder /servidor .

# Expõe a porta da API REST
EXPOSE 8080

# Define o comando de inicialização
ENTRYPOINT ["./servidor"]


