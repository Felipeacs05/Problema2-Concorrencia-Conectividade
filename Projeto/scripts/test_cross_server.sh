 ..#!/bin/bash

# Script de teste automatizado para comunicação cross-server
# Testa se jogadores em servidores diferentes podem jogar juntos

set -e

echo "🧪 Teste de Comunicação Cross-Server"
echo "===================================="
echo ""

# Cores para output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Função para verificar se servidores estão rodando
check_servers() {
    echo "🔍 Verificando servidores..."
    
    for server in servidor1:8080 servidor2:8080 servidor3:8080; do
        if curl -s "http://$server/servers" > /dev/null 2>&1; then
            echo -e "${GREEN}✓${NC} $server está online"
        else
            echo -e "${RED}✗${NC} $server está offline"
            return 1
        fi
    done
    
    echo ""
}

# Função para testar descoberta de peers
test_peer_discovery() {
    echo "🔍 Testando descoberta de peers..."
    
    response=$(curl -s "http://servidor1:8080/servers")
    peer_count=$(echo "$response" | grep -o "servidor" | wc -l)
    
    if [ "$peer_count" -ge 3 ]; then
        echo -e "${GREEN}✓${NC} Descoberta de peers funcionando ($peer_count servidores descobertos)"
    else
        echo -e "${RED}✗${NC} Problemas na descoberta de peers"
        return 1
    fi
    
    echo ""
}

# Função para testar eleição de líder
test_leader_election() {
    echo "🗳️  Testando eleição de líder Raft..."
    
    # Aguarda eleição completar
    sleep 8
    
    response=$(curl -s "http://servidor1:8080/estoque/status")
    
    if echo "$response" | grep -q "lider"; then
        lider=$(echo "$response" | grep -o '"lider":[^,}]*' | head -1)
        echo -e "${GREEN}✓${NC} Líder eleito com sucesso"
        echo "   $lider"
    else
        echo -e "${RED}✗${NC} Eleição de líder falhou"
        return 1
    fi
    
    echo ""
}

# Função para testar autenticação JWT
test_jwt_auth() {
    echo "🔐 Testando autenticação JWT..."
    
    # Tenta acessar endpoint autenticado sem token
    status_code=$(curl -s -o /dev/null -w "%{http_code}" \
        -X POST "http://servidor1:8080/game/start" \
        -H "Content-Type: application/json" \
        -d '{}')
    
    if [ "$status_code" = "401" ]; then
        echo -e "${GREEN}✓${NC} Autenticação JWT funcionando (401 sem token)"
    else
        echo -e "${YELLOW}⚠${NC}  Resposta inesperada: $status_code"
    fi
    
    echo ""
}

# Função para testar endpoint /game/replicate
test_game_replicate() {
    echo "🔄 Testando endpoint /game/replicate..."
    
    # Gera um token JWT simples (apenas para teste)
    # Em produção, use o token gerado pelo servidor
    
    status_code=$(curl -s -o /dev/null -w "%{http_code}" \
        -X POST "http://servidor2:8080/game/replicate" \
        -H "Content-Type: application/json" \
        -H "Authorization: Bearer test_token" \
        -d '{
            "matchId": "test-match-123",
            "eventSeq": 1,
            "state": {
                "sala_id": "test-match-123",
                "estado": "JOGANDO",
                "cartas_na_mesa": {},
                "pontos_rodada": {},
                "pontos_partida": {},
                "numero_rodada": 1,
                "prontos": {},
                "eventSeq": 1,
                "eventLog": []
            },
            "token": "test_token",
            "signature": "test_signature"
        }')
    
    # Espera 401 (token inválido) ou 200 (endpoint funcional)
    if [ "$status_code" = "401" ] || [ "$status_code" = "200" ]; then
        echo -e "${GREEN}✓${NC} Endpoint /game/replicate está acessível (status: $status_code)"
    else
        echo -e "${RED}✗${NC} Endpoint /game/replicate com problema (status: $status_code)"
    fi
    
    echo ""
}

# Função para verificar logs de sincronização
test_logs() {
    echo "📋 Verificando logs de sincronização..."
    
    # Verifica se há logs de matchmaking global
    if docker compose logs servidor1 2>/dev/null | grep -q "MATCHMAKING"; then
        echo -e "${GREEN}✓${NC} Logs de matchmaking encontrados"
    else
        echo -e "${YELLOW}⚠${NC}  Nenhuma partida cross-server ainda"
    fi
    
    # Verifica se há logs de replicação
    if docker compose logs --tail=100 2>/dev/null | grep -q "REPLICATE\|SHADOW\|HOST"; then
        echo -e "${GREEN}✓${NC} Logs de replicação encontrados"
    else
        echo -e "${YELLOW}⚠${NC}  Nenhuma replicação de estado ainda"
    fi
    
    echo ""
}

# Função para exibir estatísticas
show_stats() {
    echo "📊 Estatísticas do Cluster"
    echo "=========================="
    
    for i in 1 2 3; do
        echo ""
        echo "Servidor $i:"
        response=$(curl -s "http://servidor$i:8080/estoque/status" 2>/dev/null || echo "{}")
        echo "$response" | grep -o '"[^"]*":[^,}]*' | sed 's/"//g' | sed 's/:/: /' | sed 's/^/  /'
    done
    
    echo ""
}

# Executa os testes
main() {
    check_servers || exit 1
    test_peer_discovery || exit 1
    test_leader_election || exit 1
    test_jwt_auth
    test_game_replicate
    test_logs
    show_stats
    
    echo ""
    echo -e "${GREEN}✅ Testes concluídos!${NC}"
    echo ""
    echo "Para testar partida cross-server completa, execute:"
    echo ""
    echo "  Terminal 1: docker compose run --name cliente_marcelo cliente"
    echo "              (Escolha servidor 1)"
    echo ""
    echo "  Terminal 2: docker compose run --name cliente_felipe cliente"
    echo "              (Escolha servidor 2)"
    echo ""
    echo "Os dois jogadores devem ser pareados e jogar juntos! 🎮"
    echo ""
}

# Executa
main

