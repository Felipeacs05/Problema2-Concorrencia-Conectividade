# Changelog

Todas as mudanças notáveis neste projeto serão documentadas neste arquivo.

## [2.0.0] - 2025-10-11

### Adicionado
- Promoção automática da Sombra quando Host falha
- Failover de broker MQTT para clientes
- Matchmaking global entre servidores diferentes
- Sistema de troca de cartas entre jogadores
- Testes unitários básicos para o servidor
- Detecção de timeout em requisições HTTP

### Melhorado
- Tolerância a falhas do sistema Host-Sombra
- Reconexão automática de clientes MQTT
- Distribuição de partidas entre servidores

### Corrigido
- Problema de variável não utilizada em handleSolicitarOponente
- Sincronização de estado após falha do Host

## [1.0.0] - 2025-10-09

### Adicionado
- Arquitetura distribuída com 3 servidores
- API REST para comunicação entre servidores
- MQTT pub-sub para comunicação cliente-servidor
- Eleição de líder (Guardião do Estoque)
- Replicação Host-Sombra para partidas
- Sistema de estoque distribuído
- Lógica completa de jogo de cartas
- Cliente interativo via terminal
- Docker Compose para orquestração
- Documentação completa (README, ARQUITETURA, etc.)

