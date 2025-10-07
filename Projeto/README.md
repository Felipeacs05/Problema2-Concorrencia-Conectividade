# Jogo de Cartas Multiplayer Distribuído

## Visão Geral

Sistema de jogo de cartas multiplayer com arquitetura distribuída e tolerante a falhas, desenvolvido em Go. O sistema utiliza múltiplos servidores de jogo que colaboram para hospedar partidas e gerenciar recursos compartilhados.

## Arquitetura

### Componentes Principais

#### 1. Infraestrutura (3 Servidores + 3 Brokers MQTT)
- **3 Servidores de Jogo** (S1, S2, S3): Cada um rodando em contêiner Docker
- **3 Brokers MQTT** (B1, B2, B3): Um broker Mosquitto dedicado por servidor
- **Orquestração**: Docker Compose gerencia todos os 6 contêineres

#### 2. Comunicação

##### Servidor ↔ Servidor (A Ponte)
- **Protocolo**: API REST (HTTP)
- **Finalidade**: Comunicação entre servidores para:
  - Eleição de líder (Guardião do Estoque)
  - Operações de estoque distribuído
  - Sincronização de estado de partidas
  - Encaminhamento de comandos entre Host e Sombra

##### Cliente ↔ Servidor (As Ilhas)
- **Protocolo**: MQTT (Publisher-Subscriber)
- **Tópicos**:
  - `clientes/{id_cliente}/login` - Login do cliente
  - `clientes/{id_cliente}/entrar_fila` - Entrada na fila de matchmaking
  - `clientes/{id_cliente}/eventos` - Eventos para o cliente
  - `partidas/{id_sala}/comandos` - Comandos dos jogadores
  - `partidas/{id_sala}/eventos` - Eventos da partida

#### 3. Lógica de Estado Distribuído

##### Guardião do Estoque (Consenso Raft-like)
- **Eleição de Líder**: Algoritmo baseado em Raft com votação entre servidores
- **Termo Atual**: Cada servidor mantém um contador de termo para eleições
- **Heartbeats**: Líder envia heartbeats periódicos (3s) para manter autoridade
- **Timeout de Eleição**: Se não há líder por 10s, inicia nova eleição
- **Autoridade**: Apenas o líder pode realizar operações no estoque global
- **Requisições**: Servidores não-líderes fazem requisições HTTP ao líder para operações de estoque

##### Host e Sombra da Partida (Replicação Primário-Backup)
Para partidas entre jogadores em servidores diferentes:
- **Host (Primário)**: 
  - Responsável por executar toda a lógica do jogo
  - Processa jogadas e determina vencedores
  - Envia estado atualizado para a Sombra após cada jogada
- **Sombra (Backup)**:
  - Recebe comandos dos seus clientes via MQTT
  - Encaminha comandos ao Host via API REST
  - Recebe estado sincronizado do Host
  - Re-publica atualizações para seus clientes via MQTT
  - Pronta para assumir como Host em caso de falha

### Fluxo de uma Jogada Remota

#### Cenário: Cliente B (conectado a S2 - Sombra) joga uma carta

1. **CB → B2 (MQTT)**: Cliente B publica comando em `partidas/{sala_id}/comandos`
2. **B2 → S2**: Broker entrega mensagem ao Servidor 2
3. **S2 → S1 (HTTP)**: Sombra encaminha comando ao Host via API REST
   - Endpoint: `POST /partida/encaminhar_comando`
   - Dados: `{sala_id, comando, cliente_id, carta_id}`
4. **S1 (Processa)**: Host executa lógica do jogo
   - Valida jogada
   - Atualiza estado da partida
   - Determina vencedor (se ambos jogaram)
5. **S1 → S2 (HTTP)**: Host sincroniza estado com Sombra
   - Endpoint: `POST /partida/sincronizar_estado`
   - Dados: `EstadoPartida{...}`
6. **S1 → B1 (MQTT)**: Host publica atualização em `partidas/{sala_id}/eventos`
7. **B1 → CA**: Cliente A (do S1) recebe atualização
8. **S2 → B2 (MQTT)**: Sombra re-publica atualização em seu broker
9. **B2 → CB**: Cliente B (do S2) recebe atualização

## Estrutura de Diretórios

```
Projeto/
├── servidor/
│   ├── main.go           # Servidor distribuído completo
│   └── Dockerfile        # Container do servidor
├── cliente/
│   ├── main.go           # Cliente interativo
│   └── Dockerfile        # Container do cliente
├── protocolo/
│   └── protocolo.go      # Definições de mensagens e estruturas
├── mosquitto/
│   └── config/
│       └── mosquitto.conf # Configuração dos brokers MQTT
├── docker-compose.yml    # Orquestração completa
├── go.mod               # Dependências Go
└── README.md            # Este arquivo
```

## Pré-requisitos

- **Docker** (versão 20.10+)
- **Docker Compose** (versão 1.29+)
- **Go** 1.25+ (apenas para desenvolvimento local)

## Como Executar

### 1. Iniciar o Sistema Completo

```bash
# No diretório do projeto
docker-compose up --build
```

Isso iniciará:
- 3 brokers MQTT (portas 1883, 1884, 1885)
- 3 servidores de jogo (portas 8080, 8081, 8082)

### 2. Executar Clientes

#### Opção A: Cliente em Container (Interativo)

```bash
# Em um novo terminal
docker-compose run --rm cliente
```

#### Opção B: Cliente Local (Desenvolvimento)

```bash
# Terminal 1 - Cliente 1
cd cliente
go run main.go

# Terminal 2 - Cliente 2
cd cliente
go run main.go
```

### 3. Jogar

1. **Escolha um servidor** (1, 2 ou 3) quando solicitado
2. **Digite seu nome** de jogador
3. O sistema automaticamente coloca você na **fila de matchmaking**
4. Quando encontrar um oponente, use `/comprar` para adquirir cartas
5. Use `/cartas` para ver suas cartas
6. Use `/jogar <ID_da_carta>` para jogar uma carta

## Comandos do Cliente

| Comando | Descrição |
|---------|-----------|
| `/comprar` | Compra um pacote de 5 cartas |
| `/jogar <ID>` | Joga uma carta (use o ID mostrado em /cartas) |
| `/cartas` | Mostra suas cartas na mão |
| `/ajuda` | Mostra lista de comandos |
| `/sair` | Sai do jogo |
| `<texto>` | Qualquer outro texto é enviado como chat |

## Monitoramento

### Ver logs dos servidores

```bash
# Todos os servidores
docker-compose logs -f servidor1 servidor2 servidor3

# Servidor específico
docker-compose logs -f servidor1
```

### Ver logs dos brokers MQTT

```bash
docker-compose logs -f broker1 broker2 broker3
```

### Verificar status do cluster

```bash
# Status do servidor 1
curl http://localhost:8080/servers

# Status do estoque (no líder)
curl http://localhost:8080/estoque/status
```

## API REST dos Servidores

### Endpoints de Descoberta
- `POST /register` - Registra um novo servidor no cluster
- `POST /heartbeat` - Envia heartbeat e informações de líder
- `GET /servers` - Lista todos os servidores conhecidos

### Endpoints de Eleição
- `POST /eleicao/solicitar_voto` - Solicita voto em uma eleição
- `POST /eleicao/declarar_lider` - Declara um novo líder

### Endpoints de Estoque (apenas líder)
- `POST /estoque/comprar_pacote` - Compra pacote de cartas do estoque
- `GET /estoque/status` - Verifica status do estoque

### Endpoints de Partida
- `POST /partida/encaminhar_comando` - Encaminha comando da Sombra ao Host
- `POST /partida/sincronizar_estado` - Sincroniza estado do Host para Sombra
- `POST /partida/notificar_jogador` - Notifica jogador via MQTT

## Testes de Tolerância a Falhas

### Teste 1: Falha do Líder (Guardião do Estoque)

```bash
# 1. Identifique o líder atual
curl http://localhost:8080/estoque/status

# 2. Pare o líder (exemplo: servidor1)
docker-compose stop servidor1

# 3. Aguarde ~10s e verifique nova eleição
curl http://localhost:8081/estoque/status

# 4. Tente comprar cartas - deve funcionar com novo líder
```

### Teste 2: Falha do Host da Partida

```bash
# Durante uma partida ativa
# 1. Identifique qual servidor é o Host (via logs)
# 2. Pare o servidor Host
docker-compose stop servidor1

# 3. A Sombra deve detectar a falha e se promover
# 4. A partida deve continuar (implementação futura)
```

### Teste 3: Falha de um Broker MQTT

```bash
# 1. Pare um broker
docker-compose stop broker1

# 2. Clientes conectados a esse broker perdem conexão
# 3. Servidores 2 e 3 continuam funcionando normalmente
# 4. Reinicie o broker
docker-compose start broker1
```

## Recursos Implementados

### ✅ Arquitetura Distribuída
- [x] 3 servidores independentes em containers
- [x] 1 broker MQTT dedicado por servidor
- [x] Docker Compose para orquestração

### ✅ Comunicação
- [x] API REST entre servidores (HTTP)
- [x] MQTT pub-sub entre clientes e servidores
- [x] Tópicos específicos para comandos e eventos

### ✅ Eleição de Líder (Guardião do Estoque)
- [x] Algoritmo de consenso baseado em Raft
- [x] Votação distribuída
- [x] Heartbeats periódicos
- [x] Timeout e nova eleição automática
- [x] Operações de estoque exclusivas do líder

### ✅ Replicação Host-Sombra
- [x] Designação de Host e Sombra por partida
- [x] Encaminhamento de comandos via API REST
- [x] Sincronização de estado após cada jogada
- [x] Re-publicação de eventos pela Sombra

### ✅ Lógica de Jogo
- [x] Matchmaking entre jogadores
- [x] Sistema de cartas com raridades (C, U, R, L)
- [x] Compra de pacotes do estoque distribuído
- [x] Jogadas e resolução de vencedores
- [x] Chat entre jogadores
- [x] Finalização de partidas

### ✅ Cliente
- [x] Escolha de servidor na conexão
- [x] Interface interativa via terminal
- [x] Subscrição a eventos MQTT
- [x] Publicação de comandos

## Melhorias Futuras

### Recuperação de Falhas
- [ ] Promoção automática da Sombra a Host
- [ ] Redistribuição de clientes em caso de falha de servidor
- [ ] Persistência de estado em disco

### Escalabilidade
- [ ] Balanceamento de carga entre servidores
- [ ] Sharding do estoque entre múltiplos líderes
- [ ] Cache distribuído para reduzir latência

### Observabilidade
- [ ] Métricas Prometheus
- [ ] Dashboard Grafana
- [ ] Tracing distribuído (Jaeger)
- [ ] Logs estruturados

### Funcionalidades
- [ ] Matchmaking por nível/ranking
- [ ] Torneios e ligas
- [ ] Troca de cartas entre jogadores (já definido no protocolo)
- [ ] Sistema de recompensas

## Tecnologias Utilizadas

- **Go 1.25**: Linguagem principal
- **Docker & Docker Compose**: Containerização e orquestração
- **Eclipse Mosquitto**: Broker MQTT
- **Gin**: Framework web para API REST
- **Paho MQTT**: Cliente MQTT em Go
- **UUID**: Geração de identificadores únicos

## Arquitetura Técnica

### Concorrência
- Uso extensivo de **goroutines** para paralelismo
- **Mutexes** (RWMutex) para sincronização de estado compartilhado
- **Channels** para comunicação assíncrona
- **Atomic operations** para contadores thread-safe

### Padrões de Design
- **Publisher-Subscriber**: Comunicação cliente-servidor via MQTT
- **Request-Response**: API REST entre servidores
- **Primary-Backup**: Replicação de estado de partidas
- **Leader Election**: Consenso para Guardião do Estoque
- **State Synchronization**: Sincronização periódica de estado

## Licença

Este projeto foi desenvolvido para fins acadêmicos como parte do curso de Engenharia de Computação da UEFS.

## Autores

Desenvolvido para o componente **MI - Concorrência e Conectividade** - Problema 2

---

**Nota**: Este sistema demonstra conceitos avançados de sistemas distribuídos, incluindo consenso, replicação, tolerância a falhas e comunicação assíncrona. É uma implementação didática e pode ser expandida para uso em produção com as melhorias sugeridas acima.

