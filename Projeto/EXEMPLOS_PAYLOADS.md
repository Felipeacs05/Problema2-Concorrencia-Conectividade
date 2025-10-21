# 🧪 Exemplos de Payloads - API REST Cross-Server

Este documento contém exemplos práticos de payloads JSON para testar os endpoints REST do sistema.

---

## 🔐 Gerando Token JWT (Simulado)

Para testes, você pode usar um token JWT simples. Em produção, obtenha o token do servidor:

```bash
# Gerar token no servidor
docker exec servidor1 /root/servidor -generate-jwt

# Ou use este token de teste (expira em 24h)
TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzZXJ2ZXJfaWQiOiJzZXJ2aWRvcjEiLCJleHAiOjE3MzU2ODk2MDAsImlhdCI6MTczNTYwMzIwMH0.test_signature"
```

---

## 1️⃣ POST `/game/start` - Criar Nova Partida

### Descrição
Cria uma nova partida cross-server e envia o estado inicial ao servidor Shadow.

### Exemplo de Requisição

```bash
curl -X POST http://servidor1:8080/game/start \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
    "hostServer": "servidor1:8080",
    "players": [
      {
        "id": "b3f0f70a-f525-4260-8dfc-1f03b99c9af8",
        "nome": "Marcelo",
        "server": "servidor1:8080"
      },
      {
        "id": "e3b4d184-5c77-461e-9f72-ea95ce00def6",
        "nome": "Felipe",
        "server": "servidor2:8080"
      }
    ],
    "token": "'"$TOKEN"'"
  }'
```

### Payload JSON

```json
{
  "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
  "hostServer": "servidor1:8080",
  "players": [
    {
      "id": "b3f0f70a-f525-4260-8dfc-1f03b99c9af8",
      "nome": "Marcelo",
      "server": "servidor1:8080"
    },
    {
      "id": "e3b4d184-5c77-461e-9f72-ea95ce00def6",
      "nome": "Felipe",
      "server": "servidor2:8080"
    }
  ],
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
}
```

### Resposta Esperada (200 OK)

```json
{
  "status": "created",
  "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
  "host": "servidor1:8080",
  "shadow": "servidor2:8080"
}
```

### Resposta de Erro (401 Unauthorized)

```json
{
  "error": "Token de autorização ausente"
}
```

---

## 2️⃣ POST `/game/event` - Enviar Evento de Jogo

### Descrição
Envia um evento de jogo (jogada) do servidor Shadow para o Host.

### Exemplo de Requisição

```bash
curl -X POST http://servidor1:8080/game/event \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
    "eventSeq": 3,
    "eventType": "CARD_PLAYED",
    "playerId": "e3b4d184-5c77-461e-9f72-ea95ce00def6",
    "data": {
      "carta_id": "Abc12"
    },
    "token": "'"$TOKEN"'",
    "signature": "dGVzdF9zaWduYXR1cmU="
  }'
```

### Payload JSON

```json
{
  "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
  "eventSeq": 3,
  "eventType": "CARD_PLAYED",
  "playerId": "e3b4d184-5c77-461e-9f72-ea95ce00def6",
  "data": {
    "carta_id": "Abc12"
  },
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "signature": "dGVzdF9zaWduYXR1cmU="
}
```

### Resposta Esperada (200 OK)

```json
{
  "status": "processed",
  "eventSeq": 3,
  "state": {
    "sala_id": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
    "estado": "JOGANDO",
    "cartas_na_mesa": {
      "Felipe": {
        "id": "Abc12",
        "nome": "Dragão",
        "naipe": "♠",
        "valor": 85,
        "raridade": "R"
      }
    },
    "pontos_rodada": {},
    "pontos_partida": {},
    "numero_rodada": 1,
    "prontos": {},
    "eventSeq": 3
  }
}
```

### Resposta de Erro (409 Conflict)

```json
{
  "error": "Evento desatualizado ou duplicado"
}
```

### Resposta de Erro (403 Forbidden)

```json
{
  "error": "Este servidor não é o Host da partida"
}
```

---

## 3️⃣ POST `/game/replicate` - Replicar Estado

### Descrição
Replica o estado da partida do Host para o Shadow.

### Exemplo de Requisição

```bash
curl -X POST http://servidor2:8080/game/replicate \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
    "eventSeq": 5,
    "state": {
      "sala_id": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
      "estado": "JOGANDO",
      "cartas_na_mesa": {
        "Marcelo": {
          "id": "Xyz89",
          "nome": "Fênix",
          "naipe": "♥",
          "valor": 92,
          "raridade": "L"
        },
        "Felipe": {
          "id": "Abc12",
          "nome": "Dragão",
          "naipe": "♠",
          "valor": 85,
          "raridade": "R"
        }
      },
      "pontos_rodada": {
        "Marcelo": 1
      },
      "pontos_partida": {},
      "numero_rodada": 1,
      "prontos": {},
      "eventSeq": 5,
      "eventLog": [
        {
          "eventSeq": 0,
          "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
          "timestamp": "2025-10-19T12:26:19Z",
          "eventType": "MATCH_START",
          "playerId": "SYSTEM",
          "data": {
            "host": "servidor1:8080",
            "shadow": "servidor2:8080"
          },
          "signature": "abc123..."
        }
      ]
    },
    "token": "'"$TOKEN"'",
    "signature": "c3RhdGVfc2lnbmF0dXJl"
  }'
```

### Payload JSON (Completo)

```json
{
  "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
  "eventSeq": 5,
  "state": {
    "sala_id": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
    "estado": "JOGANDO",
    "cartas_na_mesa": {
      "Marcelo": {
        "id": "Xyz89",
        "nome": "Fênix",
        "naipe": "♥",
        "valor": 92,
        "raridade": "L"
      },
      "Felipe": {
        "id": "Abc12",
        "nome": "Dragão",
        "naipe": "♠",
        "valor": 85,
        "raridade": "R"
      }
    },
    "pontos_rodada": {
      "Marcelo": 1
    },
    "pontos_partida": {},
    "numero_rodada": 1,
    "prontos": {},
    "eventSeq": 5,
    "eventLog": [
      {
        "eventSeq": 0,
        "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
        "timestamp": "2025-10-19T12:26:19Z",
        "eventType": "MATCH_START",
        "playerId": "SYSTEM",
        "data": {
          "host": "servidor1:8080",
          "shadow": "servidor2:8080"
        },
        "signature": "abc123..."
      },
      {
        "eventSeq": 3,
        "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
        "timestamp": "2025-10-19T12:27:45Z",
        "eventType": "CARD_PLAYED",
        "playerId": "e3b4d184-5c77-461e-9f72-ea95ce00def6",
        "data": {
          "carta_id": "Abc12",
          "carta_nome": "Dragão",
          "carta_valor": 85
        },
        "signature": "def456..."
      },
      {
        "eventSeq": 5,
        "matchId": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
        "timestamp": "2025-10-19T12:28:12Z",
        "eventType": "CARD_PLAYED",
        "playerId": "b3f0f70a-f525-4260-8dfc-1f03b99c9af8",
        "data": {
          "carta_id": "Xyz89",
          "carta_nome": "Fênix",
          "carta_valor": 92
        },
        "signature": "ghi789..."
      }
    ]
  },
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "signature": "c3RhdGVfc2lnbmF0dXJl"
}
```

### Resposta Esperada (200 OK)

```json
{
  "status": "replicated",
  "eventSeq": 5
}
```

### Resposta - Replicação Desatualizada (200 OK)

```json
{
  "status": "ignored",
  "reason": "outdated"
}
```

---

## 🔄 Outros Endpoints (Sem Autenticação)

### GET `/servers` - Listar Servidores

```bash
curl http://servidor1:8080/servers
```

**Resposta:**
```json
{
  "servidor1:8080": {
    "endereco": "servidor1:8080",
    "ultimo_ping": "2025-10-19T14:39:40Z",
    "ativo": true
  },
  "servidor2:8080": {
    "endereco": "servidor2:8080",
    "ultimo_ping": "2025-10-19T14:40:37Z",
    "ativo": true
  },
  "servidor3:8080": {
    "endereco": "servidor3:8080",
    "ultimo_ping": "2025-10-19T14:40:37Z",
    "ativo": true
  }
}
```

---

### POST `/matchmaking/solicitar_oponente` - Buscar Oponente

```bash
curl -X POST http://servidor1:8080/matchmaking/solicitar_oponente \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "solicitante_id": "e3b4d184-5c77-461e-9f72-ea95ce00def6",
    "solicitante_nome": "Felipe",
    "servidor_origem": "servidor2:8080"
  }'
```

**Resposta (Oponente Encontrado):**
```json
{
  "partida_encontrada": true,
  "sala_id": "76b638b2-8d6d-45a9-bcca-5f01b6a74cc6",
  "oponente_nome": "Marcelo",
  "servidor_host": "servidor1:8080"
}
```

**Resposta (Sem Oponente):**
```json
{
  "partida_encontrada": false
}
```

---

## 🧪 Script de Teste Completo (Bash)

```bash
#!/bin/bash

# Configuração
HOST="servidor1:8080"
SHADOW="servidor2:8080"
TOKEN="eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.test_token"

echo "🧪 Testando API REST Cross-Server"
echo "=================================="

# Teste 1: Verificar servidores
echo ""
echo "1️⃣ Testando GET /servers..."
curl -s http://$HOST/servers | jq .
sleep 1

# Teste 2: Criar partida
echo ""
echo "2️⃣ Testando POST /game/start..."
curl -X POST http://$HOST/game/start \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "matchId": "test-match-123",
    "hostServer": "'"$HOST"'",
    "players": [
      {"id": "player1", "nome": "Jogador1", "server": "'"$HOST"'"},
      {"id": "player2", "nome": "Jogador2", "server": "'"$SHADOW"'"}
    ]
  }' | jq .
sleep 1

# Teste 3: Enviar evento
echo ""
echo "3️⃣ Testando POST /game/event..."
curl -X POST http://$HOST/game/event \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "matchId": "test-match-123",
    "eventSeq": 1,
    "eventType": "CARD_PLAYED",
    "playerId": "player1",
    "data": {"carta_id": "card_1"}
  }' | jq .
sleep 1

# Teste 4: Replicar estado
echo ""
echo "4️⃣ Testando POST /game/replicate..."
curl -X POST http://$SHADOW/game/replicate \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $TOKEN" \
  -d '{
    "matchId": "test-match-123",
    "eventSeq": 1,
    "state": {
      "sala_id": "test-match-123",
      "estado": "JOGANDO",
      "eventSeq": 1
    }
  }' | jq .

echo ""
echo "✅ Testes concluídos!"
```

---

## 📊 Testando com Postman

### Collection Postman

```json
{
  "info": {
    "name": "Jogo Distribuído - API Cross-Server",
    "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"
  },
  "item": [
    {
      "name": "Game Start",
      "request": {
        "method": "POST",
        "header": [
          {
            "key": "Content-Type",
            "value": "application/json"
          },
          {
            "key": "Authorization",
            "value": "Bearer {{jwt_token}}"
          }
        ],
        "body": {
          "mode": "raw",
          "raw": "{\n  \"matchId\": \"{{$guid}}\",\n  \"hostServer\": \"servidor1:8080\",\n  \"players\": [\n    {\"id\": \"player1\", \"nome\": \"Jogador1\", \"server\": \"servidor1:8080\"},\n    {\"id\": \"player2\", \"nome\": \"Jogador2\", \"server\": \"servidor2:8080\"}\n  ]\n}"
        },
        "url": {
          "raw": "http://servidor1:8080/game/start",
          "protocol": "http",
          "host": ["servidor1"],
          "port": "8080",
          "path": ["game", "start"]
        }
      }
    }
  ],
  "variable": [
    {
      "key": "jwt_token",
      "value": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9..."
    }
  ]
}
```

---

## 🔍 Depuração

### Verificar Logs em Tempo Real

```bash
# Ver logs de todos os servidores
docker compose logs -f

# Ver logs de um servidor específico
docker compose logs -f servidor1

# Filtrar por tipo de log
docker compose logs | grep "GAME-"
docker compose logs | grep "HOST"
docker compose logs | grep "SHADOW"
```

### Verificar Estado Interno

```bash
# Verificar conexões ativas
docker exec servidor1 netstat -an | grep 8080

# Verificar processos
docker exec servidor1 ps aux

# Verificar variáveis de ambiente
docker exec servidor1 env | grep SERVER
```

---

## ⚠️ Troubleshooting

### Erro: 401 Unauthorized

**Problema:** Token JWT inválido ou expirado

**Solução:**
```bash
# Gere um novo token
TOKEN=$(docker exec servidor1 /root/servidor -generate-jwt)
```

### Erro: 409 Conflict (Evento desatualizado)

**Problema:** EventSeq não é sequencial

**Solução:** Certifique-se de incrementar eventSeq corretamente:
```json
{
  "eventSeq": 1,  // Primeiro evento
  "eventSeq": 2,  // Próximo evento
  "eventSeq": 3   // E assim por diante...
}
```

### Erro: 403 Forbidden (Não é Host)

**Problema:** Tentando processar evento em servidor que não é Host

**Solução:** Envie eventos apenas para o servidor Host da partida.

---

## 🎯 Conclusão

Use estes exemplos como referência para testar e integrar com a API REST do sistema de jogo distribuído. Todos os endpoints estão protegidos por JWT e validam assinaturas HMAC para garantir segurança e integridade! 🔐✨

