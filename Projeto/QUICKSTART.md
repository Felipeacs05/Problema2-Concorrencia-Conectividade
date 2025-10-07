# Guia Rápido de Início

## Pré-requisitos

Certifique-se de ter instalado:
- Docker Desktop (Windows/Mac) ou Docker Engine (Linux)
- Docker Compose

## Passo 1: Iniciar o Sistema

```bash
# No diretório do projeto
docker-compose up --build
```

Aguarde até ver as mensagens:
```
servidor1    | [INFO] Servidor pronto e operacional
servidor2    | [INFO] Servidor pronto e operacional  
servidor3    | [INFO] Servidor pronto e operacional
```

## Passo 2: Executar Clientes

### Opção A: Cliente em Container

```bash
# Terminal 1 - Cliente 1
docker-compose run --rm cliente

# Terminal 2 - Cliente 2 (em outro terminal)
docker-compose run --rm cliente
```

### Opção B: Cliente Local (mais responsivo)

```bash
# Terminal 1 - Cliente 1
cd cliente
go run main.go

# Terminal 2 - Cliente 2
cd cliente
go run main.go
```

**IMPORTANTE**: Se usar cliente local, certifique-se de:
- Editar `main.go` para usar `localhost` nos brokers:
  ```go
  case "1":
      brokerAddr = "tcp://localhost:1883"  // ao invés de broker1
  case "2":
      brokerAddr = "tcp://localhost:1884"  // ao invés de broker2
  case "3":
      brokerAddr = "tcp://localhost:1885"  // ao invés de broker3
  ```

## Passo 3: Jogar

1. Escolha um servidor (1, 2 ou 3)
2. Digite seu nome
3. Aguarde encontrar um oponente
4. Use `/comprar` para comprar cartas
5. Use `/cartas` para ver suas cartas
6. Use `/jogar <ID>` para jogar uma carta
7. Divirta-se!

## Comandos Úteis

### Ver Logs

```bash
# Todos os servidores
docker-compose logs -f servidor1 servidor2 servidor3

# Servidor específico
docker-compose logs -f servidor1

# Brokers MQTT
docker-compose logs -f broker1 broker2 broker3
```

### Verificar Status

```bash
# Status do cluster
curl http://localhost:8080/servers

# Status do estoque (servidor 1)
curl http://localhost:8080/estoque/status

# Status do estoque (servidor 2)
curl http://localhost:8081/estoque/status

# Status do estoque (servidor 3)
curl http://localhost:8082/estoque/status
```

### Parar o Sistema

```bash
# Parar todos os containers
docker-compose down

# Parar e remover volumes
docker-compose down -v
```

## Teste Rápido de Falha

### Teste 1: Falha do Líder

```bash
# 1. Verifique quem é o líder
curl http://localhost:8080/estoque/status | jq '.lider'
curl http://localhost:8081/estoque/status | jq '.lider'
curl http://localhost:8082/estoque/status | jq '.lider'

# 2. Pare o servidor líder (exemplo: servidor1)
docker-compose stop servidor1

# 3. Aguarde 10-15 segundos e verifique novo líder
curl http://localhost:8081/estoque/status | jq '.lider'

# 4. Reinicie o servidor parado
docker-compose start servidor1
```

### Teste 2: Falha de Broker

```bash
# 1. Pare um broker
docker-compose stop broker2

# 2. Clientes conectados ao broker2 perdem conexão
# 3. Outros clientes continuam funcionando

# 4. Reinicie o broker
docker-compose start broker2
```

## Solução de Problemas

### Cliente não conecta

```bash
# Verifique se os brokers estão rodando
docker-compose ps broker1 broker2 broker3

# Verifique logs do broker
docker-compose logs broker1
```

### Servidor não inicia

```bash
# Verifique portas em uso
netstat -ano | findstr "8080"  # Windows
lsof -i :8080                  # Linux/Mac

# Verifique logs
docker-compose logs servidor1
```

### Erro de build

```bash
# Limpe tudo e reconstrua
docker-compose down -v
docker-compose build --no-cache
docker-compose up
```

### Cliente local não conecta

Certifique-se de usar `localhost` ao invés de `broker1/2/3` quando executar o cliente fora do Docker.

## Arquitetura Resumida

```
┌─────────┐  ┌─────────┐  ┌─────────┐
│Broker 1 │  │Broker 2 │  │Broker 3 │  (MQTT)
└────▲────┘  └────▲────┘  └────▲────┘
     │            │            │
┌────▼────┐  ┌────▼────┐  ┌────▼────┐
│Server 1 │◄─┤Server 2 │◄─┤Server 3 │  (REST API)
│ :8080   │──► :8080   │──► :8080   │
└────▲────┘  └────▲────┘  └────▲────┘
     │            │            │
┌────▼────┐  ┌────▼────┐  ┌────▼────┐
│Client 1 │  │Client 2 │  │Client 3 │  (MQTT)
└─────────┘  └─────────┘  └─────────┘
```

- **Comunicação vertical (MQTT)**: Cliente ↔ Servidor
- **Comunicação horizontal (HTTP)**: Servidor ↔ Servidor
- **Cada servidor tem seu broker dedicado**
- **Servidores se comunicam via REST para sincronização**

## Próximos Passos

- Leia [README.md](README.md) para documentação completa
- Leia [ARQUITETURA.md](ARQUITETURA.md) para detalhes técnicos
- Explore a API REST com Postman ou curl
- Implemente melhorias sugeridas

---

**Dica**: Use Ctrl+C para parar os clientes. Para parar o sistema completo, use `docker-compose down` em outro terminal.

