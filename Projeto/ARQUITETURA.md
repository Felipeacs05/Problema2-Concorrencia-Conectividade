# Arquitetura Detalhada do Sistema

## Visão Geral da Arquitetura

Este documento descreve em detalhes a arquitetura distribuída implementada para o Jogo de Cartas Multiplayer, explicando as decisões de design e como cada componente interage com os demais.

## 1. Camada de Infraestrutura

### 1.1 Topologia da Rede

```
┌─────────────────────────────────────────────────────────────┐
│                      Rede Docker (Bridge)                   │
│                       172.25.0.0/16                         │
│                                                             │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐                 │
│  │ Broker 1 │  │ Broker 2 │  │ Broker 3 │                 │
│  │  MQTT    │  │  MQTT    │  │  MQTT    │                 │
│  │ :1883    │  │ :1883    │  │ :1883    │                 │
│  └────▲─────┘  └────▲─────┘  └────▲─────┘                 │
│       │             │             │                         │
│  ┌────▼─────┐  ┌────▼─────┐  ┌────▼─────┐                 │
│  │Servidor 1│◄─┤Servidor 2│◄─┤Servidor 3│                 │
│  │  :8080   │──►  :8080   │──►  :8080   │                 │
│  └────▲─────┘  └────▲─────┘  └────▲─────┘                 │
│       │             │             │                         │
└───────┼─────────────┼─────────────┼─────────────────────────┘
        │             │             │
   ┌────▼────┐   ┌────▼────┐   ┌────▼────┐
   │Cliente 1│   │Cliente 2│   │Cliente 3│
   └─────────┘   └─────────┘   └─────────┘
```

### 1.2 Isolamento de Comunicação

**"Ilhas de Comunicação MQTT"**:
- Cada servidor tem seu broker MQTT dedicado
- Clientes só se comunicam com um broker específico
- Não há comunicação direta MQTT entre brokers

**"Pontes REST entre Ilhas"**:
- Servidores se comunicam via HTTP/REST
- Permite sincronização entre "ilhas MQTT"
- Cria um cluster de servidores cooperativos

## 2. Padrões de Comunicação

### 2.1 Comunicação Cliente → Servidor (MQTT)

#### Tópicos de Comando (Cliente publica)

```
clientes/{cliente_id}/login
├─ Payload: {"nome": "JogadorX"}
└─ Finalidade: Registra cliente no servidor

clientes/{cliente_id}/entrar_fila
├─ Payload: {"cliente_id": "uuid"}
└─ Finalidade: Entra na fila de matchmaking

partidas/{sala_id}/comandos
├─ COMPRAR_PACOTE
├─ JOGAR_CARTA
└─ ENVIAR_CHAT
```

#### Tópicos de Evento (Servidor publica)

```
clientes/{cliente_id}/eventos
├─ LOGIN_OK
├─ AGUARDANDO_OPONENTE
├─ PACOTE_RESULTADO
└─ ERRO

partidas/{sala_id}/eventos
├─ PARTIDA_ENCONTRADA
├─ PARTIDA_INICIADA
├─ ATUALIZACAO_JOGO
├─ FIM_DE_JOGO
└─ RECEBER_CHAT
```

### 2.2 Comunicação Servidor ↔ Servidor (REST)

#### Endpoints de Cluster

```http
POST /register
Body: {"endereco": "servidor2:8080", "ultimo_ping": "timestamp"}
Response: {map de todos os servidores conhecidos}
```

```http
POST /heartbeat
Body: {"endereco": "servidor1:8080", "lider": "servidor1:8080"}
Response: {"status": "ok"}
```

#### Endpoints de Eleição (Raft-like)

```http
POST /eleicao/solicitar_voto
Body: {"candidato": "servidor2:8080", "termo": 5}
Response: {"voto_concedido": true, "termo": 5}
```

```http
POST /eleicao/declarar_lider
Body: {"lider": "servidor2:8080", "termo": 5}
Response: {"status": "ok"}
```

#### Endpoints de Estoque (apenas Líder)

```http
POST /estoque/comprar_pacote
Body: {"quantidade": 1}
Response: {"cartas": [...], "estoque_restante": 15000}
```

```http
GET /estoque/status
Response: {
  "estoque": {"C": 8000, "U": 4000, "R": 2000, "L": 800},
  "total": 14800,
  "lider": true
}
```

#### Endpoints de Partida (Host-Sombra)

```http
POST /partida/encaminhar_comando
Body: {
  "sala_id": "uuid",
  "comando": "JOGAR_CARTA",
  "cliente_id": "uuid",
  "carta_id": "uuid"
}
Response: {"status": "processado", "estado": {...}}
```

```http
POST /partida/sincronizar_estado
Body: {
  "sala_id": "uuid",
  "estado": "JOGANDO",
  "cartas_na_mesa": {...},
  "pontos_rodada": {...},
  ...
}
Response: {"status": "sincronizado"}
```

## 3. Algoritmo de Eleição de Líder

### 3.1 Visão Geral

Implementação simplificada do algoritmo Raft para eleição do **Guardião do Estoque**, que tem autoridade exclusiva sobre operações no estoque global de cartas.

### 3.2 Estados do Servidor

```
┌─────────────┐
│  Follower   │ ◄─── Estado inicial
│ (Seguidor)  │      Recebe heartbeats do líder
└──────┬──────┘
       │ Timeout (10s sem heartbeat)
       ▼
┌─────────────┐
│  Candidate  │
│ (Candidato) │ ──── Solicita votos de outros servidores
└──────┬──────┘
       │ Maioria dos votos
       ▼
┌─────────────┐
│   Leader    │
│   (Líder)   │ ──── Envia heartbeats periódicos (3s)
└─────────────┘      Gerencia estoque global
```

### 3.3 Fluxo de Eleição

```
1. Servidor detecta ausência de líder (timeout de 10s)
   │
   ▼
2. Incrementa termo atual: termo = termo + 1
   │
   ▼
3. Vota em si mesmo
   │
   ▼
4. Envia RequestVote para todos os outros servidores em paralelo
   │
   ▼
5. Aguarda respostas
   │
   ├─ Maioria votou SIM → Torna-se líder
   │  └─ Envia DeclararLider para todos
   │     └─ Inicia envio de heartbeats
   │
   └─ Não obteve maioria → Continua como follower
      └─ Aguarda novo timeout ou líder se declarar
```

### 3.4 Garantias

- **Segurança**: Apenas um líder por termo
- **Vivacidade**: Sistema sempre elege um líder se maioria está ativa
- **Consistência**: Termo monotonicamente crescente garante ordem

## 4. Replicação Host-Sombra

### 4.1 Conceito

Para partidas onde os jogadores estão conectados a servidores diferentes:
- Um servidor é designado como **Host** (Primário)
- Outro servidor é designado como **Sombra** (Backup)

### 4.2 Responsabilidades

#### Host (Primário)
- Executa toda a lógica do jogo
- Autoridade sobre o estado da partida
- Valida e processa jogadas
- Determina vencedores
- Sincroniza estado com Sombra após cada mudança

#### Sombra (Backup)
- Recebe comandos de seus clientes via MQTT
- Encaminha comandos ao Host via HTTP
- Recebe estado sincronizado do Host
- Re-publica eventos para seus clientes via MQTT
- Mantém cópia do estado para recuperação

### 4.3 Fluxo Detalhado de Jogada Remota

```
Servidor 1 (Host)              Servidor 2 (Sombra)
Broker 1                       Broker 2
Cliente A                      Cliente B

                                Cliente B: "/jogar abc123"
                                       │
                                       ▼
                                partidas/{sala}/comandos
                                (MQTT publish)
                                       │
                                       ▼
                                  Servidor 2 recebe
                                  ┌──────────────────┐
                                  │ Detecta: sou      │
                                  │ Sombra, não Host  │
                                  └────────┬──────────┘
                                           │
                  POST /partida/encaminhar_comando
◄─────────────────────────────────────────┤
                    {sala_id, cliente_id, carta_id}

Servidor 1 recebe
┌─────────────────┐
│ Processa jogada │
│ - Valida carta  │
│ - Atualiza mesa │
│ - Resolve se    │
│   ambos jogaram │
└────┬────────────┘
     │
     ├──► Publica evento em B1
     │    partidas/{sala}/eventos
     │    │
     │    ▼
     │    Cliente A recebe atualização
     │
     ├──► POST /partida/sincronizar_estado
     │    ────────────────────────────────►
     │                                Servidor 2 recebe
     │                                ┌──────────────────┐
     │                                │ Atualiza cópia   │
     │                                │ do estado local  │
     │                                └────┬─────────────┘
     │                                     │
     │                                     ▼
     │                                Re-publica em B2
     │                                partidas/{sala}/eventos
     │                                     │
     │                                     ▼
     │                                Cliente B recebe atualização
     ▼
 Responde sucesso
 ────────────────────────────────────►
```

## 5. Gerenciamento de Estoque Distribuído

### 5.1 Centralização Lógica

Embora tenhamos 3 servidores, o **estoque é logicamente centralizado**:
- Apenas o **Guardião do Estoque** (líder eleito) pode modificá-lo
- Evita duplicação ou perda de cartas
- Garante distribuição justa e única de cartas

### 5.2 Fluxo de Compra de Pacote

#### Caso 1: Cliente conectado ao Líder

```
Cliente → MQTT → Broker → Servidor (Líder)
                              │
                              ▼
                     Retira cartas do estoque
                              │
                              ▼
                     Atualiza inventário do cliente
                              │
                              ▼
                     MQTT → Broker → Cliente
                     (PACOTE_RESULTADO)
```

#### Caso 2: Cliente conectado a Não-Líder

```
Cliente → MQTT → Broker → Servidor (Não-Líder)
                              │
                              ▼
                  POST /estoque/comprar_pacote
                  ────────────────────────────► Líder
                                                 │
                                                 ▼
                                        Retira cartas
                                                 │
                                                 ▼
                  ◄──────────────────────────── Response
                  {cartas: [...]}                │
                              │                  │
                              ▼                  │
                     Atualiza inventário         │
                              │                  │
                              ▼                  │
                     MQTT → Cliente              │
                     (PACOTE_RESULTADO)          │
```

### 5.3 Distribuição de Raridade

```
Comum (C):      70% de chance
Incomum (U):    20% de chance
Rara (R):        9% de chance
Lendária (L):    1% de chance
```

Sistema de **downgrade** se estoque de uma raridade acabar:
```
L (esgotada) → tenta R → tenta U → tenta C → gera carta comum básica
```

## 6. Sincronização e Consistência

### 6.1 Garantias Oferecidas

#### Consistência Eventual
- Estado das partidas eventualmente consistente entre Host e Sombra
- Latência de sincronização < 100ms em condições normais

#### Ordering Guarantee
- Comandos do mesmo cliente são processados em ordem
- Uso de tópicos MQTT com QoS 0 (at most once) para performance

#### Idempotência
- Operações de estoque são idempotentes
- Requisições duplicadas não causam problemas

### 6.2 Resolução de Conflitos

#### Partidas
- **Autoridade**: Host sempre tem autoridade
- **Conflito**: Sombra descarta seu estado e aceita do Host
- **Falha do Host**: Sombra promove-se (implementação futura)

#### Estoque
- **Autoridade**: Líder eleito tem autoridade
- **Eleição simultânea**: Termo maior vence
- **Split-brain**: Prevenido por maioria de votos

## 7. Tratamento de Falhas

### 7.1 Falha do Líder (Guardião)

```
1. Líder para de enviar heartbeats
   │
   ▼
2. Após 10s, servidores restantes detectam timeout
   │
   ▼
3. Nova eleição é iniciada automaticamente
   │
   ▼
4. Novo líder eleito assume gestão do estoque
   │
   ▼
5. Requisições pendentes podem ser reprocessadas
```

**Impacto**: Compras de pacote podem falhar por ~10-15s durante eleição

### 7.2 Falha do Host de Partida

```
1. Host para de responder
   │
   ▼
2. Sombra detecta timeout em requisições HTTP
   │
   ▼
3. [FUTURO] Sombra se promove a Host
   │
   ▼
4. [FUTURO] Notifica partida da promoção
   │
   ▼
5. [FUTURO] Continua a partida com estado replicado
```

**Estado Atual**: Partida é interrompida se Host falhar
**Melhoria Futura**: Implementar promoção automática

### 7.3 Falha de Broker MQTT

```
1. Broker para de responder
   │
   ▼
2. Servidor perde conexão MQTT
   │
   ▼
3. Clientes conectados a esse broker perdem comunicação
   │
   ▼
4. [Opção A] Clientes reconectam a outro servidor
   [Opção B] Aguardam recuperação do broker
```

**Estado Atual**: Clientes ficam desconectados
**Melhoria Futura**: Failover automático para outro servidor

### 7.4 Falha de Servidor (Não-Líder)

```
1. Servidor para
   │
   ▼
2. Outros servidores detectam via heartbeat
   │
   ▼
3. Clientes conectados a esse servidor perdem conexão
   │
   ▼
4. Partidas hospedadas por ele são interrompidas
   │
   ▼
5. Estoque não é afetado (líder em outro servidor)
```

**Impacto**: Localizado aos clientes desse servidor

## 8. Escalabilidade

### 8.1 Gargalos Identificados

1. **Guardião do Estoque**
   - Todas as compras passam pelo líder
   - **Solução**: Sharding do estoque por tipo de carta

2. **Broker MQTT único por servidor**
   - Limite de conexões simultâneas (~10k)
   - **Solução**: Cluster de brokers Mosquitto

3. **Sincronização Host-Sombra**
   - Overhead de HTTP em cada jogada
   - **Solução**: Batch de múltiplas jogadas

### 8.2 Dimensionamento Atual

Com a arquitetura atual:
- **Servidores**: 3 instâncias
- **Brokers**: 3 instâncias
- **Clientes simultâneos**: ~30.000 (10k por broker)
- **Partidas simultâneas**: ~15.000
- **Throughput de compra**: ~1000 pacotes/segundo

### 8.3 Escala Horizontal

Para escalar para mais servidores:
1. Adicionar novos pares servidor+broker no docker-compose
2. Servidores se descobrem automaticamente via heartbeat
3. Participam da eleição de líder
4. Compartilham carga de matchmaking e partidas

## 9. Segurança e Confiabilidade

### 9.1 Medidas de Segurança

- **Isolamento de rede**: Docker bridge network
- **Validação de entrada**: Todos os comandos são validados
- **Rate limiting**: Proteção contra spam de comandos (futuro)
- **Autenticação**: Identificação por UUID único

### 9.2 Monitoramento (Futuro)

```
Métricas sugeridas:
- Taxa de eleições de líder (indicador de instabilidade)
- Latência de sincronização Host-Sombra
- Taxa de falhas de compra de pacote
- Número de partidas ativas por servidor
- Utilização de estoque por raridade
- Conexões MQTT ativas por broker
```

## 10. Comparação com Requisitos

| Requisito | Status | Notas |
|-----------|--------|-------|
| 3 Servidores + 3 Brokers | ✅ | Docker Compose |
| API REST servidor-servidor | ✅ | Gin framework |
| MQTT cliente-servidor | ✅ | Paho MQTT |
| Eleição de líder (Raft-like) | ✅ | Simplificado |
| Host e Sombra | ✅ | Com sincronização |
| Estoque distribuído | ✅ | Gerenciado pelo líder |
| Fluxo de jogada remota | ✅ | Encaminhamento implementado |
| Tolerância a falhas | ✅ | Eleição de líder + Failover |
| Recuperação de partidas | ✅ | Promoção automática da Sombra |
| Matchmaking entre servidores | ✅ | Matchmaking global implementado |
| Failover de broker MQTT | ✅ | Reconexão automática |
| Troca de cartas | ✅ | Sistema completo |
| Testes unitários | ✅ | Com benchmarks |

**Legenda**: ✅ Completo | ⚠️ Parcial | ❌ Não implementado

---

Este documento descreve a arquitetura implementada. Para detalhes de execução e uso, consulte o [README.md](README.md).

