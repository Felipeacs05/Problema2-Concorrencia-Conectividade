# 🎮 Jogo de Cartas Multiplayer Distribuído

Sistema de jogo de cartas multiplayer com arquitetura distribuída e tolerante a falhas, desenvolvido em Go.

## 🚀 Como Rodar (Modo Simples)

### ⚠️ Importante: Antes de Começar (Limpeza do Docker)
Para evitar erros comuns como address already in use (porta já em uso), é altamente recomendado limpar o ambiente Docker antes de iniciar os contêineres.

Execute o seguinte comando no diretório do projeto para parar e remover contêineres, redes e volumes de execuções anteriores:
```bash
docker compose down -v
```

Se o problema persistir, um comando mais agressivo para parar todos os contêineres em execução na sua máquina é:
```
docker stop $(docker ps -a -q)
```

### 1. Iniciar Tudo de Uma Vez
```bash
# No diretório do projeto
docker-compose up --build
```

Isso inicia automaticamente:
- ✅ 3 brokers MQTT (portas 1883, 1884, 1885)
- ✅ 3 servidores de jogo (portas 8080, 8081, 8082)
- ✅ Tudo conectado e funcionando

### 2. Conectar Clientes

#### Opção A: Cliente em Container (Recomendado)
```bash
# Terminal 1 - Cliente 1
docker-compose run --rm cliente

# Terminal 2 - Cliente 2  
docker-compose run --rm cliente

# Terminal 3 - Cliente 3
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

### 3. Jogar! 🎯

1. **Escolha um servidor** (1, 2 ou 3) quando solicitado
2. **Digite seu nome** de jogador
3. O sistema automaticamente coloca você na **fila de matchmaking**
4. Quando encontrar um oponente, use `/comprar` para adquirir cartas
5. Use `/cartas` para ver suas cartas
6. Use `/jogar <ID_da_carta>` para jogar uma carta
7. Use `/trocar` para trocar cartas com o oponente

## 📋 Comandos do Cliente

| Comando | Descrição |
|---------|-----------|
| `/comprar` | Compra um pacote de 5 cartas |
| `/jogar <ID>` | Joga uma carta (use o ID mostrado em /cartas) |
| `/cartas` | Mostra suas cartas na mão |
| `/trocar` | Propõe uma troca de cartas com o oponente |
| `/ajuda` | Mostra lista de comandos |
| `/sair` | Sai do jogo |
| `<texto>` | Qualquer outro texto é enviado como chat |

## 🔧 Comandos de Gerenciamento

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

### Verificar status do sistema
```bash
# Status do servidor 1
curl http://localhost:8080/servers

# Status do estoque (no líder)
curl http://localhost:8080/estoque/status
```

### Parar o sistema
```bash
docker-compose down
```

## 🧪 Testes de Tolerância a Falhas

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

# 3. A Sombra deve detectar a falha e se promover automaticamente
# 4. A partida deve continuar normalmente
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

## 🏗️ Arquitetura

### Componentes
- **3 Servidores de Jogo** (S1, S2, S3): Cada um rodando em contêiner Docker
- **3 Brokers MQTT** (B1, B2, B3): Um broker Mosquitto dedicado por servidor
- **Orquestração**: Docker Compose gerencia todos os 6 contêineres

### Comunicação
- **Servidor ↔ Servidor**: API REST (HTTP) para eleição de líder, estoque e sincronização
- **Cliente ↔ Servidor**: MQTT (Publisher-Subscriber) para comandos e eventos

### Recursos Implementados
- ✅ **Eleição de Líder**: Algoritmo Raft-like para consenso
- ✅ **Replicação Host-Sombra**: Tolerância a falhas de partidas
- ✅ **Matchmaking Global**: Busca oponentes em todos os servidores
- ✅ **Troca de Cartas**: Sistema completo de troca entre jogadores
- ✅ **Failover de Broker**: Reconexão automática em caso de falha
- ✅ **Testes Unitários**: Cobertura de testes para lógica crítica

## 📁 Estrutura do Projeto

```
Projeto/
├── servidor/
│   ├── main.go           # Servidor distribuído completo
│   ├── main_test.go      # Testes unitários
│   └── Dockerfile        # Container do servidor
├── cliente/
│   ├── main.go           # Cliente interativo
│   └── Dockerfile        # Container do cliente
├── protocolo/
│   └── protocolo.go      # Definições de mensagens
├── mosquitto/
│   └── config/
│       └── mosquitto.conf # Configuração dos brokers
├── docker-compose.yml    # Orquestração completa
├── go.mod               # Dependências Go
└── README.md            # Este arquivo
```

## 🛠️ Pré-requisitos

- **Docker** (versão 20.10+)
- **Docker Compose** (versão 1.29+)
- **Go** 1.25+ (apenas para desenvolvimento local)

## 🎯 Funcionalidades Principais

### Sistema de Cartas
- **4 Raridades**: Comum (70%), Incomum (20%), Rara (9%), Lendária (1%)
- **Valores**: 1-50 para cartas comuns, 51-100 para incomuns, etc.
- **Naipes**: ♠ ♥ ♦ ♣ com hierarquia para desempates

### Matchmaking
- **Local**: Busca oponentes no mesmo servidor primeiro
- **Global**: Se não encontrar localmente, busca em outros servidores
- **Distribuído**: Partidas podem ser entre jogadores de servidores diferentes

### Tolerância a Falhas
- **Eleição de Líder**: Consenso automático para gerenciar estoque
- **Promoção de Sombra**: Se o Host falhar, a Sombra assume automaticamente
- **Failover de Broker**: Clientes se reconectam automaticamente

## 🚨 Solução de Problemas

### Cliente não consegue conectar
```bash
# Verifique se os servidores estão rodando
docker-compose ps

# Verifique logs do broker
docker-compose logs broker1
```

### Partida não inicia
```bash
# Verifique se há jogadores na fila
curl http://localhost:8080/servers

# Verifique logs dos servidores
docker-compose logs servidor1
```

### Erro de compilação
```bash
# Reconstrua as imagens
docker-compose build --no-cache
```

## 📊 Monitoramento

### Status dos Servidores
```bash
# Lista todos os servidores
curl http://localhost:8080/servers

# Status do estoque (apenas no líder)
curl http://localhost:8080/estoque/status
```

### Logs em Tempo Real
```bash
# Todos os serviços
docker-compose logs -f

# Apenas servidores
docker-compose logs -f servidor1 servidor2 servidor3
```

## 🎮 Exemplo de Uso Completo

1. **Inicie o sistema**:
   ```bash
   docker-compose up --build
   ```

2. **Abra 2 terminais e conecte clientes**:
   ```bash
   # Terminal 1
   docker-compose run --rm cliente
   
   # Terminal 2  
   docker-compose run --rm cliente
   ```

3. **Jogue**:
   - Escolha servidor 1 em ambos
   - Digite nomes diferentes
   - Use `/comprar` para comprar cartas
   - Use `/jogar <ID>` para jogar
   - Use `/trocar` para trocar cartas

4. **Teste falhas**:
   - Pare um servidor: `docker-compose stop servidor1`
   - Veja a eleição de novo líder nos logs
   - Continue jogando normalmente

## 🏆 Tecnologias

- **Go 1.25**: Linguagem principal
- **Docker & Docker Compose**: Containerização
- **Eclipse Mosquitto**: Broker MQTT
- **Gin**: Framework web para API REST
- **Paho MQTT**: Cliente MQTT

---

**Desenvolvido para MI - Concorrência e Conectividade - UEFS** 🎓
